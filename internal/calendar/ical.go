// Package calendar parses iCal (RFC 5545) feeds into concrete event
// occurrences, expanding RRULE recurrence within a bounded time window. Pure
// stdlib — no external iCal library.
package calendar

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

// Event is one concrete calendar occurrence within the requested window.
type Event struct {
	Calendar    string
	Color       string
	Title       string
	Location    string
	Description string
	Start       time.Time
	End         time.Time
	AllDay      bool
	// Owner's RSVP status from the iCal ATTENDEE PARTSTAT: "accepted",
	// "needs-action", "tentative", "declined", or "" when not an attendee
	// (e.g. events you organize / personal calendars). Drives ghosting in the UI.
	Status string
}

type icalProp struct {
	params map[string]string
	value  string
}

// icalVEvent holds the raw parsed properties of a single VEVENT. Multi-valued
// properties (EXDATE, RDATE) keep all their occurrences.
type icalVEvent struct {
	single map[string]icalProp   // last-wins scalar props (SUMMARY, DTSTART, RRULE, ...)
	multi  map[string][]icalProp // accumulating props (EXDATE, RDATE)
}

func (e *icalVEvent) get(key string) (icalProp, bool) { p, ok := e.single[key]; return p, ok }
func (e *icalVEvent) val(key string) string           { return e.single[key].value }

// httpGet fetches a URL, capping the response body at 8MB.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// Fetch retrieves one iCal feed and parses it. The owner email is derived from
// the feed URL so ATTENDEE PARTSTAT can be matched.
func Fetch(ctx context.Context, src config.CalendarSource, now time.Time, daysAhead int) ([]Event, error) {
	body, err := httpGet(ctx, src.URL)
	if err != nil {
		return nil, err
	}
	return Parse(body, src.Name, src.Color, now, daysAhead, icalOwnerFromURL(src.URL))
}

// Parse expands an iCal feed into concrete occurrences within the window
// [now, now+daysAhead]. Handles folded lines, VALUE=DATE all-day events, RRULE
// expansion with EXDATE exclusions and RECURRENCE-ID overrides.
func Parse(body []byte, calName, color string, now time.Time, daysAhead int, ownerEmail string) ([]Event, error) {
	// Unfold folded lines (CRLF + whitespace → single line)
	body = bytes.ReplaceAll(body, []byte("\r\n "), []byte(""))
	body = bytes.ReplaceAll(body, []byte("\r\n\t"), []byte(""))
	body = bytes.ReplaceAll(body, []byte("\n "), []byte(""))
	body = bytes.ReplaceAll(body, []byte("\n\t"), []byte(""))

	cutoff := now.AddDate(0, 0, daysAhead)

	// Owner email — used to find the user's own ATTENDEE line for RSVP status.
	// Google iCal URLs embed it as /calendar/ical/<email>/<key>/basic.ics.
	owner := ownerEmail

	// Parse all VEVENT blocks first. VTIMEZONE blocks also contain RRULE/DTSTART
	// lines, so only collect lines while inside a VEVENT.
	var vevents []*icalVEvent
	var cur *icalVEvent

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "BEGIN:VEVENT":
			cur = &icalVEvent{single: map[string]icalProp{}, multi: map[string][]icalProp{}}
			continue
		case line == "END:VEVENT":
			if cur != nil {
				vevents = append(vevents, cur)
			}
			cur = nil
			continue
		}
		if cur == nil {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		rawKey, val := line[:idx], line[idx+1:]
		parts := strings.Split(rawKey, ";")
		base := strings.ToUpper(parts[0])
		params := map[string]string{}
		for _, p := range parts[1:] {
			if eq := strings.IndexByte(p, '='); eq >= 0 {
				params[strings.ToUpper(p[:eq])] = p[eq+1:] // value kept case-sensitive (TZID)
			}
		}
		prop := icalProp{params: params, value: val}
		cur.single[base] = prop
		cur.multi[base] = append(cur.multi[base], prop)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Index the override instances (VEVENTs carrying RECURRENCE-ID) by UID +
	// the specific occurrence time they replace, so we can skip the generated
	// instance and emit the override instead.
	overrides := map[string]bool{}
	for _, ve := range vevents {
		rid, ok := ve.get("RECURRENCE-ID")
		if !ok {
			continue
		}
		if t, allDay, ok := icalParseProp(rid); ok {
			overrides[ve.val("UID")+"|"+icalOccKey(t, allDay)] = true
		}
	}

	var events []Event
	for _, ve := range vevents {
		evs := icalExpand(ve, calName, color, now, cutoff, overrides, owner)
		events = append(events, evs...)
	}
	// The Pi's caller sorted at the DashData assembly step (main.go:1173), a
	// line outside the ported range. Parse now returns events in chronological
	// order directly since callers here need concrete Events, not a
	// pre-serialization struct. Sort by time, not the formatted string — RFC3339
	// lexicographic order breaks across mixed UTC offsets.
	sort.SliceStable(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events, nil
}

// icalOwnerFromURL extracts the calendar owner's email from a Google iCal URL of
// the form .../calendar/ical/<url-encoded-email>/<private-key>/basic.ics.
// Returns "" if the shape doesn't match (status detection is then skipped).
func icalOwnerFromURL(u string) string {
	const marker = "/ical/"
	i := strings.Index(u, marker)
	if i < 0 {
		return ""
	}
	rest := u[i+len(marker):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ""
	}
	email, err := url.QueryUnescape(rest[:slash]) // %40 → @
	if err != nil {
		return ""
	}
	return strings.ToLower(email)
}

// icalOwnerStatus finds the owner's ATTENDEE line and returns its PARTSTAT,
// lowercased ("accepted" | "needs-action" | "tentative" | "declined"), or ""
// when the owner isn't an attendee (events they organize, personal calendars).
func icalOwnerStatus(ve *icalVEvent, owner string) string {
	if owner == "" {
		return ""
	}
	for _, a := range ve.multi["ATTENDEE"] {
		// value is like "mailto:ns51@rice.edu"; CN param may also carry it.
		mail := strings.ToLower(strings.TrimPrefix(a.value, "mailto:"))
		cn := strings.ToLower(a.params["CN"])
		if mail == owner || cn == owner {
			return strings.ToLower(a.params["PARTSTAT"])
		}
	}
	return ""
}

// icalParseProp resolves a DTSTART/DTEND/RECURRENCE-ID property to a time,
// reporting whether it is a date-only (all-day) value.
func icalParseProp(p icalProp) (t time.Time, allDay bool, ok bool) {
	v := p.value
	if p.params["VALUE"] == "DATE" || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, time.Local)
		return t, true, err == nil
	}
	t, err := icalParseDateTime(v, p.params["TZID"])
	return t, false, err == nil
}

// icalOccKey is the canonical key for one occurrence instant, matching the
// representation used by RECURRENCE-ID lookups.
func icalOccKey(t time.Time, allDay bool) string {
	if allDay {
		return t.Format("20060102")
	}
	return t.UTC().Format("20060102T150405Z")
}

// icalExpand turns one VEVENT into the concrete Events that fall within
// [now-1h, cutoff]. Non-recurring events yield at most one; recurring events
// (RRULE) are expanded, with EXDATE exclusions and RECURRENCE-ID overrides applied.
func icalExpand(ve *icalVEvent, calName, color string, now, cutoff time.Time, overrides map[string]bool, owner string) []Event {
	title := icalUnescape(ve.val("SUMMARY"))
	if title == "" {
		title = "(no title)"
	}
	status := icalOwnerStatus(ve, owner)
	// Declined events are dropped entirely (not shown, not counted).
	if status == "declined" {
		return nil
	}
	dtstart, ok := ve.get("DTSTART")
	if !ok {
		return nil
	}
	start, allDay, ok := icalParseProp(dtstart)
	if !ok {
		return nil
	}

	// Duration = DTEND - DTSTART (fallbacks: 1 day all-day, 1 hour timed).
	var dur time.Duration
	if dtend, ok := ve.get("DTEND"); ok {
		if end, _, ok := icalParseProp(dtend); ok && end.After(start) {
			dur = end.Sub(start)
		}
	}
	if dur <= 0 {
		if allDay {
			dur = 24 * time.Hour
		} else {
			dur = time.Hour
		}
	}

	uid := ve.val("UID")
	mkEvent := func(s time.Time) Event {
		e := s.Add(dur)
		return Event{
			Calendar: calName, Color: color, Title: title,
			Location:    icalUnescape(ve.val("LOCATION")),
			Description: icalUnescape(ve.val("DESCRIPTION")),
			Start:       s, End: e, AllDay: allDay,
			Status: status,
		}
	}
	inWindow := func(s time.Time) bool {
		return !s.Add(dur).Before(now.Add(-time.Hour)) && !s.After(cutoff)
	}

	rrule, recurring := ve.get("RRULE")

	// A VEVENT carrying RECURRENCE-ID is an override for a single occurrence of
	// its series — emit it directly (already keyed out of the generated set).
	if _, isOverride := ve.get("RECURRENCE-ID"); isOverride {
		if inWindow(start) {
			return []Event{mkEvent(start)}
		}
		return nil
	}

	if !recurring {
		if inWindow(start) {
			return []Event{mkEvent(start)}
		}
		return nil
	}

	// Build the exclusion set (EXDATE) keyed like RECURRENCE-ID.
	excluded := map[string]bool{}
	for _, ex := range ve.multi["EXDATE"] {
		for _, v := range strings.Split(ex.value, ",") {
			if t, ad, ok := icalParseProp(icalProp{params: ex.params, value: v}); ok {
				excluded[icalOccKey(t, ad)] = true
			}
		}
	}

	var out []Event
	for _, occ := range icalRecur(rrule.value, start, now, cutoff, dur) {
		key := icalOccKey(occ, allDay)
		if excluded[key] {
			continue
		}
		if overrides[uid+"|"+key] {
			continue // replaced by a RECURRENCE-ID override VEVENT
		}
		if inWindow(occ) {
			out = append(out, mkEvent(occ))
		}
	}
	return out
}

// icalRecur expands an RRULE into occurrence start times that could fall within
// [now-1h, cutoff]. Supports FREQ DAILY/WEEKLY/MONTHLY/YEARLY with INTERVAL,
// COUNT, UNTIL, BYDAY (incl. ordinals like 3TU), and BYMONTHDAY. Iteration is
// bounded so a malformed/huge rule can't loop forever.
func icalRecur(rule string, dtstart, now, cutoff time.Time, dur time.Duration) []time.Time {
	freq := ""
	interval := 1
	count := -1
	var until time.Time
	var byday []string
	var bymonthday []int
	for _, part := range strings.Split(rule, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.ToUpper(kv[0]), kv[1]
		switch key {
		case "FREQ":
			freq = strings.ToUpper(val)
		case "INTERVAL":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(val); err == nil {
				count = n
			}
		case "UNTIL":
			if t, err := time.Parse("20060102T150405Z", val); err == nil {
				until = t
			} else if t, err := time.ParseInLocation("20060102", val, time.Local); err == nil {
				until = t
			}
		case "BYDAY":
			byday = strings.Split(strings.ToUpper(val), ",")
		case "BYMONTHDAY":
			for _, s := range strings.Split(val, ",") {
				if n, err := strconv.Atoi(s); err == nil {
					bymonthday = append(bymonthday, n)
				}
			}
		}
	}
	if freq == "" {
		return nil
	}

	// We only care about occurrences whose end is at/after now-1h and start is
	// before cutoff. To respect COUNT correctly we still iterate from dtstart,
	// but cap total iterations and stop once well past the window.
	const maxIter = 100000
	emitted := 0
	var out []time.Time

	emit := func(t time.Time) bool {
		emitted++
		if count >= 0 && emitted > count {
			return false
		}
		if !until.IsZero() && t.After(until) {
			return false
		}
		if t.After(cutoff) {
			return false
		}
		if !t.Add(dur).Before(now.Add(-time.Hour)) {
			out = append(out, t)
		}
		return true
	}

	switch freq {
	case "DAILY":
		for cur, i := dtstart, 0; i < maxIter; i++ {
			if !emit(cur) {
				break
			}
			cur = cur.AddDate(0, 0, interval)
		}
	case "WEEKLY":
		// Days within each week to fire on; default to DTSTART's weekday.
		days := bydayWeekdays(byday)
		if len(days) == 0 {
			days = []time.Weekday{dtstart.Weekday()}
		}
		weekStart := startOfWeek(dtstart)
		for w := 0; w < maxIter; w++ {
			base := weekStart.AddDate(0, 0, 7*interval*w)
			past := true
			for _, wd := range days {
				occ := dateWithClock(base.AddDate(0, 0, int(wd)), dtstart)
				if occ.Before(dtstart) {
					continue // before series start
				}
				if !emit(occ) {
					return out
				}
				if !occ.After(cutoff) {
					past = false
				}
			}
			if base.AddDate(0, 0, 7).After(cutoff) && past {
				break
			}
		}
	case "MONTHLY":
		for m := 0; m < maxIter; m++ {
			base := dtstart.AddDate(0, interval*m, 0)
			occs := monthlyOccurrences(base, dtstart, byday, bymonthday)
			for _, occ := range occs {
				if occ.Before(dtstart) {
					continue
				}
				if !emit(occ) {
					return out
				}
			}
			if base.After(cutoff) {
				break
			}
		}
	case "YEARLY":
		for y := 0; y < maxIter; y++ {
			occ := dtstart.AddDate(interval*y, 0, 0)
			if !emit(occ) {
				break
			}
		}
	default:
		// Unsupported FREQ (e.g. HOURLY/MINUTELY/SECONDLY): emit base only.
		emit(dtstart)
	}
	return out
}

// bydayWeekdays parses BYDAY tokens (SU,MO,...) into weekdays, ignoring any
// ordinal prefix (used by MONTHLY/YEARLY, not WEEKLY).
func bydayWeekdays(byday []string) []time.Weekday {
	var out []time.Weekday
	for _, tok := range byday {
		if wd, ok := weekdayCode(tok); ok {
			out = append(out, wd)
		}
	}
	return out
}

// weekdayCode parses a BYDAY token like "MO" or "3TU" or "-1FR" into a weekday,
// dropping any leading ordinal.
func weekdayCode(tok string) (time.Weekday, bool) {
	tok = strings.TrimSpace(tok)
	// strip leading sign/digits
	i := 0
	for i < len(tok) && (tok[i] == '+' || tok[i] == '-' || (tok[i] >= '0' && tok[i] <= '9')) {
		i++
	}
	code := tok[i:]
	switch code {
	case "SU":
		return time.Sunday, true
	case "MO":
		return time.Monday, true
	case "TU":
		return time.Tuesday, true
	case "WE":
		return time.Wednesday, true
	case "TH":
		return time.Thursday, true
	case "FR":
		return time.Friday, true
	case "SA":
		return time.Saturday, true
	}
	return time.Sunday, false
}

// bydayOrdinal parses the ordinal of a BYDAY token (e.g. "3TU" → 3, "-1FR" → -1,
// "FR" → 0 meaning every matching weekday).
func bydayOrdinal(tok string) int {
	tok = strings.TrimSpace(tok)
	i := 0
	for i < len(tok) && (tok[i] == '+' || tok[i] == '-' || (tok[i] >= '0' && tok[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0
	}
	n, err := strconv.Atoi(tok[:i])
	if err != nil {
		return 0
	}
	return n
}

func startOfWeek(t time.Time) time.Time {
	// Align to Sunday at midnight of t's day; WKST defaults to Monday in RFC but
	// only matters for INTERVAL>1 boundary placement — Sunday baseline is fine
	// for the common weekly meeting case.
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -int(d.Weekday()))
}

// dateWithClock takes a date (day) and applies the clock time + location of ref.
func dateWithClock(day, ref time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(),
		ref.Hour(), ref.Minute(), ref.Second(), 0, ref.Location())
}

// monthlyOccurrences computes the day(s) a MONTHLY rule fires in the month of
// base, honoring BYDAY ordinals (e.g. 3TU = 3rd Tuesday) or BYMONTHDAY; if
// neither is set it uses DTSTART's day-of-month.
func monthlyOccurrences(base, dtstart time.Time, byday []string, bymonthday []int) []time.Time {
	year, month := base.Year(), base.Month()
	var out []time.Time

	if len(byday) > 0 {
		for _, tok := range byday {
			wd, ok := weekdayCode(tok)
			if !ok {
				continue
			}
			ord := bydayOrdinal(tok)
			if d := nthWeekdayOfMonth(year, month, wd, ord, dtstart); !d.IsZero() {
				out = append(out, d)
			}
		}
		return out
	}
	if len(bymonthday) > 0 {
		for _, dom := range bymonthday {
			if d := monthDay(year, month, dom, dtstart); !d.IsZero() {
				out = append(out, d)
			}
		}
		return out
	}
	if d := monthDay(year, month, dtstart.Day(), dtstart); !d.IsZero() {
		out = append(out, d)
	}
	return out
}

// nthWeekdayOfMonth returns the ord-th occurrence of weekday wd in the given
// month (ord>0 from start, ord<0 from end, ord==0 → every occurrence is not
// supported here so treated as 1st). Returns zero time if it doesn't exist.
func nthWeekdayOfMonth(year int, month time.Month, wd time.Weekday, ord int, ref time.Time) time.Time {
	if ord == 0 {
		ord = 1
	}
	first := time.Date(year, month, 1, ref.Hour(), ref.Minute(), ref.Second(), 0, ref.Location())
	last := first.AddDate(0, 1, -1)
	var days []time.Time
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == wd {
			days = append(days, d)
		}
	}
	if len(days) == 0 {
		return time.Time{}
	}
	if ord > 0 {
		if ord-1 < len(days) {
			return days[ord-1]
		}
		return time.Time{}
	}
	// negative: from end
	if idx := len(days) + ord; idx >= 0 {
		return days[idx]
	}
	return time.Time{}
}

// monthDay returns the given day-of-month (dom>0 from start, dom<0 from end) in
// year/month with ref's clock, or zero if it overflows the month.
func monthDay(year int, month time.Month, dom int, ref time.Time) time.Time {
	daysIn := time.Date(year, month+1, 0, 0, 0, 0, 0, ref.Location()).Day()
	day := dom
	if dom < 0 {
		day = daysIn + dom + 1
	}
	if day < 1 || day > daysIn {
		return time.Time{}
	}
	return time.Date(year, month, day, ref.Hour(), ref.Minute(), ref.Second(), 0, ref.Location())
}

// icalParseDateTime parses iCal DATETIME values: 20060102T150405Z or 20060102T150405
// tzid is the TZID parameter value (e.g. "America/New_York"), empty string to use local.
func icalParseDateTime(s, tzid string) (time.Time, error) {
	if strings.HasSuffix(s, "Z") {
		return time.Parse("20060102T150405Z", s)
	}
	loc := time.Local
	if tzid != "" {
		if l, err := time.LoadLocation(tzid); err == nil {
			loc = l
		}
	}
	return time.ParseInLocation("20060102T150405", s, loc)
}

// icalUnescape unescapes iCal text values (\n \, \; \,)
func icalUnescape(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\N`, "\n")
	s = strings.ReplaceAll(s, `\,`, ",")
	s = strings.ReplaceAll(s, `\;`, ";")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}
