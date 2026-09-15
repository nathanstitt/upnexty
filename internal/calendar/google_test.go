package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
)

// fakeTokens is a TokenSource that hands back a fixed token, or an error.
type fakeTokens struct {
	token string
	err   error
	calls []string // accounts asked for, in order
}

func (f *fakeTokens) AccessToken(_ context.Context, account string) (string, error) {
	f.calls = append(f.calls, account)
	if f.err != nil {
		return "", f.err
	}
	return f.token, nil
}

// googleTestServer starts a server and closes it with the test.
func googleTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// withBase points googleAPIBase at a test server for the duration of the test,
// so these cases exercise the real request-building code.
func withBase(t *testing.T, base string) {
	t.Helper()
	old := googleAPIBase
	googleAPIBase = base
	t.Cleanup(func() { googleAPIBase = old })
}

func TestFetchGoogleMapsTheFixture(t *testing.T) {
	var gotPath, gotAuth string
	var gotQuery url.Values
	srv := googleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write(load(t, "google_events.json"))
	})
	withBase(t, srv.URL)

	src := config.CalendarSource{
		Name: "Work", Color: "#4f9cff",
		Kind: config.KindGoogle, CalID: "me@example.com", Account: "me@example.com",
	}
	toks := &fakeTokens{token: "tok-abc"}
	loc, _ := time.LoadLocation("America/Chicago")

	evs, err := fetchGoogle(context.Background(), src, toks, ref, 7, loc)
	if err != nil {
		t.Fatal(err)
	}

	// The request itself: these parameters are the entire reason the backend
	// exists, so assert them rather than trusting they were sent.
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if !strings.Contains(gotPath, "me@example.com") {
		t.Errorf("path = %q, want it to name the calendar", gotPath)
	}
	if got := gotQuery.Get("singleEvents"); got != "true" {
		t.Errorf("singleEvents = %q, want true -- without it recurrences arrive unexpanded", got)
	}
	if got := gotQuery.Get("orderBy"); got != "startTime" {
		t.Errorf("orderBy = %q, want startTime", got)
	}
	if gotQuery.Get("timeMin") == "" || gotQuery.Get("timeMax") == "" {
		t.Errorf("window not sent: timeMin=%q timeMax=%q",
			gotQuery.Get("timeMin"), gotQuery.Get("timeMax"))
	}
	if toks.calls[0] != "me@example.com" {
		t.Errorf("asked for account %q, want the source's account", toks.calls[0])
	}

	// The fixture has 8 items; the cancelled one and the declined one are
	// dropped, leaving 6.
	byTitle := map[string]Event{}
	for _, e := range evs {
		byTitle[e.Title] = e
	}
	if len(evs) != 6 {
		t.Fatalf("got %d events, want 6\n%v", len(evs), byTitle)
	}

	if _, ok := byTitle["Cancelled Occurrence"]; ok {
		t.Error("a cancelled occurrence was rendered; it has no start time and would show a zero-time card")
	}
	if _, ok := byTitle["Meeting I Declined"]; ok {
		t.Error("a declined invitation was rendered; the iCal path drops these and the panel must match")
	}

	rec := byTitle["Nathan / Scott Weekly 1:1"]
	if rec.Calendar != "Work" || rec.Color != "#4f9cff" {
		t.Errorf("source name/colour not applied: %q %q", rec.Calendar, rec.Color)
	}
	if rec.Status != "accepted" {
		t.Errorf("Status = %q, want accepted from the self attendee", rec.Status)
	}
	// 09:30 -05:00 is 14:30 UTC.
	if got := rec.Start.UTC().Format(time.RFC3339); got != "2026-09-14T14:30:00Z" {
		t.Errorf("Start = %s, want the instance time with its offset applied", got)
	}
	if got := rec.End.Sub(rec.Start); got != 25*time.Minute {
		t.Errorf("duration = %v, want 25m", got)
	}
	if rec.AllDay {
		t.Error("a timed event was marked all-day")
	}

	// An expanded instance's id carries the occurrence timestamp, so Key() is
	// unique per occurrence and muting one does not mute the series.
	if !strings.Contains(rec.UID, "20260914T180000Z") {
		t.Errorf("UID = %q, want the per-instance id", rec.UID)
	}
	other := byTitle["Moved Instance"]
	if rec.Key() == other.Key() {
		t.Error("two different occurrences produced the same mute key")
	}

	ad := byTitle["Company Holiday"]
	if !ad.AllDay {
		t.Error("a date-only event was not marked all-day")
	}
	if ad.Start.Location() != loc {
		t.Errorf("all-day start in %v, want the configured location", ad.Start.Location())
	}

	if got := byTitle["Maybe Meeting"].Status; got != "tentative" {
		t.Errorf("tentative Status = %q; ghosting depends on this", got)
	}
	// No attendees means the user made it for themselves: no RSVP exists, and
	// "" is the honest answer rather than inventing "accepted".
	if got := byTitle["Solo Focus Block"].Status; got != "" {
		t.Errorf("Status for an event with no attendees = %q, want empty", got)
	}
	if _, ok := byTitle["(no title)"]; !ok {
		t.Error("an event with no summary should render as (no title), matching the iCal path")
	}
}

// Pagination is the easiest thing to get wrong and never notice: with a light
// calendar one page is always enough, and the failure mode is a silently short
// agenda rather than an error.
func TestFetchGooglePaginates(t *testing.T) {
	var pages int
	srv := googleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		tok := r.URL.Query().Get("pageToken")
		w.Header().Set("Content-Type", "application/json")
		switch tok {
		case "":
			fmt.Fprint(w, `{"items":[
			  {"id":"a","status":"confirmed","summary":"First",
			   "start":{"dateTime":"2026-09-14T10:00:00Z"},"end":{"dateTime":"2026-09-14T10:30:00Z"}}
			],"nextPageToken":"page2"}`)
		case "page2":
			fmt.Fprint(w, `{"items":[
			  {"id":"b","status":"confirmed","summary":"Second",
			   "start":{"dateTime":"2026-09-14T11:00:00Z"},"end":{"dateTime":"2026-09-14T11:30:00Z"}}
			]}`)
		default:
			t.Errorf("unexpected pageToken %q", tok)
		}
	})
	withBase(t, srv.URL)

	evs, err := fetchGoogle(context.Background(),
		config.CalendarSource{Name: "P", Kind: config.KindGoogle, CalID: "c", Account: "a"},
		&fakeTokens{token: "t"}, ref, 7, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Errorf("fetched %d pages, want 2", pages)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events across pages, want 2", len(evs))
	}
	if evs[0].Title != "First" || evs[1].Title != "Second" {
		t.Errorf("page order lost: %q, %q", evs[0].Title, evs[1].Title)
	}
}

// A server that always returns a page token must not spin forever holding the
// calendar goroutine.
func TestFetchGoogleBoundsPagination(t *testing.T) {
	srv := googleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[],"nextPageToken":"always-more"}`)
	})
	withBase(t, srv.URL)

	_, err := fetchGoogle(context.Background(),
		config.CalendarSource{Name: "Loop", Kind: config.KindGoogle, CalID: "c", Account: "a"},
		&fakeTokens{token: "t"}, ref, 7, time.UTC)
	if err == nil {
		t.Fatal("an endless page token returned no error")
	}
	if !strings.Contains(err.Error(), "pages") {
		t.Errorf("error = %q, want it to mention the page bound", err)
	}
}

// A token failure must reach the caller with its sentinel intact: that is what
// decides between retrying in 30s and backing off until the user reconnects.
func TestFetchGooglePreservesTokenErrorSentinel(t *testing.T) {
	sentinel := errors.New("needs reauth")
	_, err := fetchGoogle(context.Background(),
		config.CalendarSource{Name: "X", Kind: config.KindGoogle, CalID: "c", Account: "a"},
		&fakeTokens{err: fmt.Errorf("refreshing: %w", sentinel)}, ref, 7, time.UTC)
	if err == nil {
		t.Fatal("no error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is lost the sentinel through the wrap: %v", err)
	}
}

// An API error must be an error, never an empty calendar -- an empty agenda is
// indistinguishable from a genuinely free day.
func TestFetchGoogleSurfacesAPIErrors(t *testing.T) {
	srv := googleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": 403, "message": "Calendar usage limits exceeded."},
		})
	})
	withBase(t, srv.URL)

	evs, err := fetchGoogle(context.Background(),
		config.CalendarSource{Name: "Q", Kind: config.KindGoogle, CalID: "c", Account: "a"},
		&fakeTokens{token: "t"}, ref, 7, time.UTC)
	if err == nil {
		t.Fatalf("403 returned %d events and no error", len(evs))
	}
	if !strings.Contains(err.Error(), "usage limits") {
		t.Errorf("error = %q, want the API's message", err)
	}
	if evs != nil {
		t.Errorf("got %d events alongside the error", len(evs))
	}
}
