package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
)

// The Google Calendar backend.
//
// Why this exists at all: the iCal export of these same calendars is 15.9MB
// across 5,091 VEVENTs, of which 43 fall in the 7-day window. Parsing it costs
// 241MB of allocation and leaves 84MB live, on a board with ~430MB usable.
// events.list with a time window returns the 44 occurrences directly, in 96KB
// -- 167x fewer bytes and 116x fewer events, measured against the real
// calendars on 2026-09-14.
//
// The decisive parameter is singleEvents=true: the server expands recurrence
// rules and returns concrete instances, so none of icalRecur/EXDATE/
// RECURRENCE-ID runs for these sources. 41 of those 44 events were recurring
// instances, which is the case that matters -- an unexpanded master is useless
// to a panel that shows "what is next".

// googleAPIBase is the API root.
//
// A var rather than a const purely so a test can point the real
// request-building code at an httptest server. The alternative -- a test that
// assembles its own URL -- passes while the production path sends something
// different, which is the failure this package has already been bitten by
// twice: the truncated iCal body and the iCal date parameters both looked
// correct until the actual bytes on the wire were checked.
var googleAPIBase = "https://www.googleapis.com/calendar/v3"

// googleMaxResults is the page size. The API caps it at 2500 and defaults to
// 250; asking for the maximum does not cost anything extra for a small window
// and makes a second page unlikely rather than merely uncommon.
//
// Unlikely is not never, so pagination is implemented -- see fetchGoogle. A
// dense week or a larger DaysAhead reaches it, and the failure mode of getting
// it wrong is a silently short agenda, which is the exact class of bug this
// whole migration was chasing.
const googleMaxResults = 2500

// googleMaxPages bounds the paging loop. Without it a server that always
// returns a nextPageToken -- a bug, or a redirect to something that is not the
// API -- would spin until the fetch context expires, holding the calendar
// goroutine and allocating the whole time.
const googleMaxPages = 20

// TokenSource hands out a bearer token for one Google account.
//
// An interface rather than a *googleauth.Store so this package does not import
// it: calendar is the lower layer, the token store owns file paths and refresh
// policy, and a test here wants a two-line fake rather than a temp directory
// and an httptest token endpoint.
type TokenSource interface {
	AccessToken(ctx context.Context, account string) (string, error)
}

// googleEvent is the subset of the events.list resource this panel renders.
// Everything omitted here is genuinely unused; adding a field is cheap, but
// decoding into a struct rather than map[string]any keeps the allocation
// profile predictable, which is the point of the whole exercise.
type googleEvent struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	Location string `json:"location"`

	Start googleTime `json:"start"`
	End   googleTime `json:"end"`

	// RecurringEventID identifies the series an expanded instance came from.
	// Unused for rendering; kept because it is the field that proves
	// singleEvents=true did its job, and a test asserts on it.
	RecurringEventID string `json:"recurringEventId"`

	Attendees []googleAttendee `json:"attendees"`
}

// googleTime is the API's start/end shape: exactly one of DateTime (a timed
// event, RFC3339 with an offset) or Date (an all-day event, YYYY-MM-DD).
type googleTime struct {
	DateTime string `json:"dateTime"`
	Date     string `json:"date"`
	TimeZone string `json:"timeZone"`
}

type googleAttendee struct {
	Email          string `json:"email"`
	Self           bool   `json:"self"`
	ResponseStatus string `json:"responseStatus"`
}

type googleEventsPage struct {
	Items         []googleEvent `json:"items"`
	NextPageToken string        `json:"nextPageToken"`
}

// googleAPIError is the error envelope: {"error":{"code":401,"message":"..."}}.
type googleAPIError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// fetchGoogle reads one calendar through the Calendar API.
//
// The window matches the iCal path exactly -- back pastWindow, forward
// daysAhead -- so a source that switches kind shows the same row. Trimming and
// MaxEvents still happen above this, on the merged set across all feeds, which
// is what keeps a mixed config from trimming two different ways.
func fetchGoogle(ctx context.Context, src config.CalendarSource, ts TokenSource,
	now time.Time, daysAhead int, loc *time.Location) ([]Event, error) {

	if loc == nil {
		loc = time.UTC
	}
	if src.CalID == "" {
		return nil, fmt.Errorf("calendar %q: google source has no cal_id", src.Name)
	}
	if ts == nil {
		return nil, fmt.Errorf("calendar %q: no google token source configured", src.Name)
	}

	token, err := ts.AccessToken(ctx, src.Account)
	if err != nil {
		// Wrapped, not replaced: the caller distinguishes ErrNeedsReauth from a
		// transient failure with errors.Is, and that decision drives whether it
		// retries in 30s or backs off until the user reconnects.
		return nil, fmt.Errorf("calendar %q: %w", src.Name, err)
	}

	var out []Event
	pageToken := ""
	for page := 0; page < googleMaxPages; page++ {
		evs, next, err := googleEventsPageFetch(ctx, src, token, now, daysAhead, loc, pageToken)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
		if next == "" {
			return out, nil
		}
		pageToken = next
	}
	return nil, fmt.Errorf("calendar %q: more than %d pages of events", src.Name, googleMaxPages)
}

// googleEventsPageFetch retrieves and maps one page.
func googleEventsPageFetch(ctx context.Context, src config.CalendarSource, token string,
	now time.Time, daysAhead int, loc *time.Location, pageToken string) ([]Event, string, error) {

	q := url.Values{
		// RFC3339 in UTC. The API requires an offset; sending Z avoids any
		// dependence on the board's timezone, which is UTC anyway.
		"timeMin": {now.Add(-pastWindow).UTC().Format(time.RFC3339)},
		"timeMax": {now.AddDate(0, 0, daysAhead).UTC().Format(time.RFC3339)},
		// The parameter this migration exists for: expand recurrences server
		// side and return instances.
		"singleEvents": {"true"},
		// Requires singleEvents=true; the API rejects it otherwise.
		"orderBy":    {"startTime"},
		"maxResults": {fmt.Sprint(googleMaxResults)},
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}

	endpoint := googleAPIBase + "/calendars/" + url.PathEscape(src.CalID) + "/events?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("calendar %q: %w", src.Name, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("calendar %q: %w", src.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Decode the error envelope for the message, but never echo the body
		// wholesale: it can carry the calendar id and request context, and this
		// string reaches the settings page.
		var ae googleAPIError
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ae)
		msg := ae.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return nil, "", fmt.Errorf("calendar %q: google api: %s", src.Name, msg)
	}

	// Decoding from the stream rather than io.ReadAll: a window of events is
	// small, but the whole reason this backend exists is that the iCal path
	// buffered 16MB, and there is no reason to reintroduce the pattern.
	var page googleEventsPage
	if err := json.NewDecoder(io.LimitReader(resp.Body, googleMaxBody)).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("calendar %q: decoding events: %w", src.Name, err)
	}

	out := make([]Event, 0, len(page.Items))
	for _, it := range page.Items {
		ev, ok := googleToEvent(it, src, loc)
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out, page.NextPageToken, nil
}

// googleMaxBody caps one page of JSON. The real calendars measured 96KB for a
// full week across two feeds; 8MB is far above any plausible page and still
// bounds a server that streams forever.
const googleMaxBody = 8 << 20

// googleToEvent maps one API resource, reporting false for events the panel
// must not show.
//
// Two are dropped here rather than rendered:
//
//   - status "cancelled". With singleEvents=true a deleted occurrence normally
//     just does not appear, but it IS returned when the caller asks for deleted
//     items, and a cancelled instance of a series can surface either way. It
//     carries no start time of its own, so rendering it would put a card with a
//     zero time on the row.
//   - declined invitations, matching the iCal path exactly (see icalExpand).
//     Showing a meeting the user declined misstates their day.
func googleToEvent(it googleEvent, src config.CalendarSource, loc *time.Location) (Event, bool) {
	if strings.EqualFold(it.Status, "cancelled") {
		return Event{}, false
	}

	status := googleOwnerStatus(it)
	if status == "declined" {
		return Event{}, false
	}

	start, allDay, ok := googleParseTime(it.Start, loc)
	if !ok {
		return Event{}, false
	}
	end, _, ok := googleParseTime(it.End, loc)
	if !ok {
		// An event with a start and no usable end still belongs on the row; the
		// iCal path defaults the same way (1 day all-day, 1 hour timed).
		if allDay {
			end = start.AddDate(0, 0, 1)
		} else {
			end = start.Add(time.Hour)
		}
	}

	title := it.Summary
	if title == "" {
		title = "(no title)"
	}

	return Event{
		Calendar: src.Name,
		Color:    src.Color,
		Title:    title,
		// The instance id, not the series id. Event.Key combines this with the
		// start, and for an expanded instance the id is already unique per
		// occurrence -- so muting one occurrence of a series mutes only that
		// one, which is the same behaviour the iCal path gets from UID+start.
		UID:      it.ID,
		Location: it.Location,
		Start:    start,
		End:      end,
		AllDay:   allDay,
		Status:   status,
	}, true
}

// googleOwnerStatus returns the signed-in user's RSVP for an event, in the same
// vocabulary as the iCal PARTSTAT values that drive ghosting.
//
// The API marks the owner's own attendee record with self:true, which is more
// reliable than the iCal path's approach of parsing an email out of the feed
// URL and string-matching ATTENDEE lines.
//
// An event with no attendees -- something the user created for themselves --
// has no RSVP, and "" is correct there: it is neither accepted nor tentative,
// and the panel renders it as an ordinary event.
func googleOwnerStatus(it googleEvent) string {
	for _, a := range it.Attendees {
		if a.Self {
			return strings.ToLower(a.ResponseStatus)
		}
	}
	return ""
}

// googleParseTime resolves a start/end object, reporting whether it is all-day.
//
// The API sends exactly one of dateTime (RFC3339 with an offset) or date
// (YYYY-MM-DD). An all-day date carries no zone, so it is read in the
// configured location -- the same rule the iCal path applies to VALUE=DATE, and
// the reason both need loc at all.
func googleParseTime(gt googleTime, loc *time.Location) (t time.Time, allDay bool, ok bool) {
	if gt.DateTime != "" {
		parsed, err := time.Parse(time.RFC3339, gt.DateTime)
		return parsed, false, err == nil
	}
	if gt.Date != "" {
		parsed, err := time.ParseInLocation("2006-01-02", gt.Date, loc)
		return parsed, true, err == nil
	}
	return time.Time{}, false, false
}
