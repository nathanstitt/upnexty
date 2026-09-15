package calendar

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
)

// ref is a fixed "now" inside the fixture window: Mon 2026-08-24 08:00 UTC.
var ref = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func titles(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Title
	}
	return out
}

func TestParseSingleEvent(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var got *Event
	for i := range evs {
		if evs[i].Title == "Design Review" {
			got = &evs[i]
		}
	}
	if got == nil {
		t.Fatalf("Design Review not found in %v", titles(evs))
	}
	want := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", got.Start, want)
	}
	if got.Location != "Conference Room B" {
		t.Errorf("Location = %q", got.Location)
	}
	if got.AllDay {
		t.Error("AllDay = true, want false")
	}
	if got.Calendar != "Work" || got.Color != "#fff" {
		t.Errorf("Calendar/Color = %q/%q", got.Calendar, got.Color)
	}
}

func TestParseAllDayEvent(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Title == "Company Holiday" {
			if !e.AllDay {
				t.Error("AllDay = false, want true")
			}
			return
		}
	}
	t.Fatalf("Company Holiday not found in %v", titles(evs))
}

func TestParseUnfoldsLongLines(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := "A meeting with a very long title that RFC 5545 requires be folded across lines"
	for _, e := range evs {
		if e.Title == want {
			return
		}
	}
	t.Fatalf("unfolded title not found; got %v", titles(evs))
}

func TestParseExpandsWeeklyByDay(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var days []time.Weekday
	for _, e := range evs {
		if e.Title == "Standup" {
			days = append(days, e.Start.Weekday())
		}
	}
	if len(days) < 3 {
		t.Fatalf("expected >=3 Standup occurrences in 7 days, got %d", len(days))
	}
	for _, d := range days {
		if d != time.Monday && d != time.Wednesday && d != time.Friday {
			t.Errorf("Standup on %v, want only Mon/Wed/Fri", d)
		}
	}
}

func TestParseHonorsExdate(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	excluded := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	for _, e := range evs {
		if e.Title == "Daily Sync" && e.Start.Equal(excluded) {
			t.Fatal("EXDATE occurrence was not excluded")
		}
	}
}

func TestParseAppliesRecurrenceOverride(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	moved := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	var foundMoved bool
	for _, e := range evs {
		if e.Start.Equal(moved) && e.Title == "Daily Sync (moved)" {
			foundMoved = true
		}
		// The original 09:00 slot on the 28th must not also appear.
		if e.Title == "Daily Sync" &&
			e.Start.Equal(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)) {
			t.Error("overridden occurrence still present at its original time")
		}
	}
	if !foundMoved {
		t.Error("RECURRENCE-ID override not found at its new time")
	}
}

func TestParseNoDuplicateOccurrences(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// Keyed on Title|Start rather than UID|Start: Event has no exported UID
	// field (the brief specifies its exact public shape), so UID isn't
	// reachable here. This means the key can't distinguish the
	// RECURRENCE-ID override (title "Daily Sync (moved)") from the series it
	// replaces (title "Daily Sync") if they ever shared a Start — they don't
	// in this fixture, so the check still catches same-title duplicates.
	seen := map[string]bool{}
	for _, e := range evs {
		k := e.Title + "|" + e.Start.Format(time.RFC3339)
		if seen[k] {
			t.Errorf("duplicate occurrence: %s", k)
		}
		seen[k] = true
	}
}

func TestParseRespectsWindow(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 1, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// At daysAhead=1 from ref (Mon 2026-08-24 08:00 UTC) only the Standup at
	// 2026-08-24 13:00 UTC falls in [ref-1h, ref+1d]: Standup's next
	// Wed/Fri occurrences and both Daily Sync events start after the cutoff.
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event within a 1-day window, got %d: %v", len(evs), titles(evs))
	}
	if evs[0].Title != "Standup" {
		t.Errorf("Title = %q, want %q", evs[0].Title, "Standup")
	}
	cutoff := ref.AddDate(0, 0, 1)
	for _, e := range evs {
		if e.Start.After(cutoff) {
			t.Errorf("event %q at %v is beyond the 1-day window", e.Title, e.Start)
		}
	}
}

func TestParseSortsChronologically(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) < 2 {
		t.Fatalf("expected multiple interleaved occurrences, got %d", len(evs))
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Start.Before(evs[i-1].Start) {
			t.Errorf("events out of order at index %d: %v (%q) before %v (%q)",
				i, evs[i].Start, evs[i].Title, evs[i-1].Start, evs[i-1].Title)
		}
	}
}

func TestParseDropsDeclinedEvents(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "owner@example.com", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Title == "Skipped Sync" {
			t.Fatal("declined event was not dropped")
		}
	}
}

func TestParseAcceptedEventCarriesStatus(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "owner@example.com", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Title == "Confirmed Sync" {
			if e.Status != "accepted" {
				t.Errorf("Status = %q, want %q", e.Status, "accepted")
			}
			return
		}
	}
	t.Fatalf("Confirmed Sync not found in %v", titles(evs))
}

func TestParseDefaultsDurationWithoutDtend(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Title == "Open-Ended Chat" {
			want := e.Start.Add(time.Hour)
			if !e.End.Equal(want) {
				t.Errorf("End = %v, want %v (Start + 1h)", e.End, want)
			}
			return
		}
	}
	t.Fatalf("Open-Ended Chat not found in %v", titles(evs))
}

// TestParseAllDayEventHonorsConfiguredLocation guards against the bug where
// Parse ignored the caller's configured timezone entirely and always fell
// back to time.Local for any value without an explicit TZID — which is every
// VALUE=DATE all-day event, since iCal's DATE type carries no zone of its
// own. On the board time.Local is UTC, so an all-day event configured for
// e.g. America/Chicago would render 5-6 hours off from where it belongs
// relative to the NOW line. Company Holiday (basic.ics) is
// "DTSTART;VALUE=DATE:20260827" with no TZID; parsed with America/Chicago it
// must resolve to midnight *Chicago* time, i.e. 2026-08-27 00:00 CDT, whose
// UTC instant is 2026-08-27 05:00Z (CDT is UTC-5 in August).
func TestParseAllDayEventHonorsConfiguredLocation(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("LoadLocation(America/Chicago): %v", err)
	}
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "", chicago)
	if err != nil {
		t.Fatal(err)
	}
	var got *Event
	for i := range evs {
		if evs[i].Title == "Company Holiday" {
			got = &evs[i]
		}
	}
	if got == nil {
		t.Fatalf("Company Holiday not found in %v", titles(evs))
	}
	if got.Start.Location().String() != "America/Chicago" {
		t.Errorf("Start.Location() = %v, want America/Chicago", got.Start.Location())
	}
	wantUTC := time.Date(2026, 8, 27, 5, 0, 0, 0, time.UTC)
	if !got.Start.UTC().Equal(wantUTC) {
		t.Errorf("Start (UTC instant) = %v, want %v (midnight America/Chicago)", got.Start.UTC(), wantUTC)
	}
}

// TestParseHonorsExdateAcrossTZID guards icalOccKey's t.UTC() normalization,
// which is load-bearing for matching EXDATE/RECURRENCE-ID against generated
// occurrences whenever the two sides are expressed in different zone forms —
// every other fixture in this package uses Z (UTC) timestamps on both the
// RRULE/DTSTART and the EXDATE, so the two keys already share a location and
// a naive string-format match (with no .UTC() call) would pass those tests
// even if the normalization were deleted. tzid.ics's DTSTART/RRULE carry
// TZID=America/New_York (so generated occurrences are represented in that
// zone) while its EXDATE is instead given as a plain Z/UTC timestamp for the
// same instant — a real-world shape some feeds produce. Only t.UTC()
// normalization before formatting makes the two keys land on the same
// string; comparing raw wall-clock representations would miss the match and
// fail to exclude the occurrence.
func TestParseHonorsExdateAcrossTZID(t *testing.T) {
	evs, err := Parse(load(t, "tzid.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var got []time.Time
	for _, e := range evs {
		if e.Title == "TZID Daily Sync" {
			got = append(got, e.Start)
		}
	}
	if len(got) != 4 {
		t.Fatalf("got %d occurrences, want 4 (5 daily - 1 excluded): %v", len(got), got)
	}
	excluded := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC) // 09:00 EDT (UTC-4)
	for _, s := range got {
		if s.UTC().Equal(excluded) {
			t.Fatalf("EXDATE occurrence at %v (America/New_York) was not excluded; got occurrences %v", excluded, got)
		}
	}
}

func TestParseUnescapesText(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := "Retro, Planning, and Review"
	for _, e := range evs {
		if e.Title == want {
			if e.Location != "Building A\nRoom 200" {
				t.Errorf("Location = %q, want %q", e.Location, "Building A\nRoom 200")
			}
			return
		}
	}
	t.Fatalf("unescaped title not found; got %v", titles(evs))
}

// vevent builds a minimal timed VEVENT for the trim test.
func vevent(title string, start, end time.Time) string {
	f := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	return "BEGIN:VEVENT\r\nUID:" + title + "@t\r\nDTSTART:" + f(start) +
		"\r\nDTEND:" + f(end) + "\r\nSUMMARY:" + title + "\r\nEND:VEVENT\r\n"
}

// The last finished event survives however long ago it ended, and only that
// one: the panel shows it for context, not the whole morning.
//
// Parse keeps every occurrence inside pastWindow -- it sees one feed at a time,
// and trimming there would keep one past card per calendar. TrimPast is applied
// by the caller to the merged set.
func TestTrimPastKeepsOnlyTheLastPastEvent(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 42, 0, 0, time.UTC)
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		vevent("morning", now.Add(-8*time.Hour), now.Add(-7*time.Hour)) +
		vevent("midday", now.Add(-5*time.Hour), now.Add(-4*time.Hour)) +
		vevent("office hours", now.Add(-4*time.Hour), now.Add(-3*time.Hour-30*time.Minute)) +
		vevent("tomorrow", now.Add(16*time.Hour), now.Add(17*time.Hour)) +
		"END:VCALENDAR\r\n"

	parsed, err := Parse([]byte(ics), "Cal", "#4f9cff", now, 7, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// pastWindow is wide enough that the whole morning survives the parse --
	// that is what lets the last one be found at all.
	if len(parsed) != 4 {
		t.Fatalf("Parse returned %d events, want all 4 (it must not trim)", len(parsed))
	}

	evs := TrimPast(parsed, now, KeepPast)
	var past, future []string
	for _, e := range evs {
		if e.End.After(now) {
			future = append(future, e.Title)
		} else {
			past = append(past, e.Title)
		}
	}
	if len(past) != KeepPast {
		t.Errorf("kept %d past events %v, want %d", len(past), past, KeepPast)
	}
	if len(past) == 1 && past[0] != "office hours" {
		t.Errorf("kept %q, want the most recent past event", past[0])
	}
	if len(future) != 1 || future[0] != "tomorrow" {
		t.Errorf("future events = %v, want [tomorrow]", future)
	}
}

// Applied to a merged set, the trim keeps one past event overall -- not one per
// calendar. Two feeds each ending with a meeting is the case that put two past
// cards and a bare chip between them on the panel.
func TestTrimPastAcrossMergedFeeds(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 42, 0, 0, time.UTC)
	merged := []Event{
		{Title: "work-am", Start: now.Add(-6 * time.Hour), End: now.Add(-5 * time.Hour)},
		{Title: "personal-pm", Start: now.Add(-4 * time.Hour), End: now.Add(-3 * time.Hour)},
		{Title: "tomorrow", Start: now.Add(16 * time.Hour), End: now.Add(17 * time.Hour)},
	}
	got := TrimPast(merged, now, KeepPast)
	if len(got) != 2 {
		t.Fatalf("kept %d events, want the last past one plus the future one", len(got))
	}
	if got[0].Title != "personal-pm" {
		t.Errorf("kept %q, want the most recent past event across both feeds", got[0].Title)
	}
}

// A finished conflict survives the trim whole.
//
// KeepPast counts slots, not events: two meetings that overlapped share one
// card on the panel, and keeping only one of them makes the last thing that
// happened look like a single ordinary meeting. Found on the real calendar --
// "Product-leads" (11:00-12:00) and "JP office hours" (11:00-11:30) both ran
// from 11:00, the trim dropped one, and the row showed no conflict.
func TestTrimPastKeepsAWholePastConflict(t *testing.T) {
	now := time.Date(2026, 9, 2, 14, 34, 0, 0, time.UTC)
	merged := []Event{
		// An earlier, separate slot -- this is what should be dropped.
		{Title: "standup", Start: now.Add(-6 * time.Hour), End: now.Add(-5*time.Hour - 30*time.Minute)},
		// The last past slot: two overlapping events.
		{Title: "product-leads", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)},
		{Title: "jp-office-hours", Start: now.Add(-3 * time.Hour), End: now.Add(-2*time.Hour - 30*time.Minute)},
		{Title: "upcoming", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	}
	got := TrimPast(merged, now, KeepPast)

	var titles []string
	for _, e := range got {
		titles = append(titles, e.Title)
	}
	want := []string{"product-leads", "jp-office-hours", "upcoming"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v -- the past conflict must survive as a pair", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, titles[i], want[i])
		}
	}
}

// A nested past event does not split the slot: the group's end is its maximum,
// so a short meeting inside a long one stays in the same slot rather than
// starting a new one and pushing the long one out.
func TestTrimPastNestedPastEventStaysInItsSlot(t *testing.T) {
	now := time.Date(2026, 9, 2, 14, 34, 0, 0, time.UTC)
	merged := []Event{
		{Title: "older", Start: now.Add(-8 * time.Hour), End: now.Add(-7 * time.Hour)},
		{Title: "block", Start: now.Add(-4 * time.Hour), End: now.Add(-2 * time.Hour)},
		{Title: "nested", Start: now.Add(-3 * time.Hour), End: now.Add(-150 * time.Minute)},
		{Title: "upcoming", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	}
	got := TrimPast(merged, now, KeepPast)
	if len(got) != 3 {
		var titles []string
		for _, e := range got {
			titles = append(titles, e.Title)
		}
		t.Fatalf("kept %v, want block+nested+upcoming", titles)
	}
	if got[0].Title != "block" || got[1].Title != "nested" {
		t.Errorf("kept %q,%q -- the nested event must stay with its block",
			got[0].Title, got[1].Title)
	}
}

// Non-overlapping past events are one slot each, so the original behaviour --
// keep the single most recent finished event -- is unchanged.
func TestTrimPastStillKeepsOneWhenNothingOverlaps(t *testing.T) {
	now := time.Date(2026, 9, 2, 14, 34, 0, 0, time.UTC)
	merged := []Event{
		{Title: "first", Start: now.Add(-6 * time.Hour), End: now.Add(-5 * time.Hour)},
		{Title: "second", Start: now.Add(-4 * time.Hour), End: now.Add(-3 * time.Hour)},
		{Title: "third", Start: now.Add(-2 * time.Hour), End: now.Add(-1 * time.Hour)},
		{Title: "upcoming", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	}
	got := TrimPast(merged, now, KeepPast)
	if len(got) != 2 || got[0].Title != "third" {
		t.Fatalf("kept %d events starting %q, want 2 starting \"third\"",
			len(got), got[0].Title)
	}
}

// A feed larger than the cap must be an error, not a silent trim.
//
// The original code read through an io.LimitReader and returned whatever came
// back. io.ReadAll stops at the limit WITHOUT an error, so an oversized feed
// arrived as a truncated-but-valid-looking iCal: the tail VEVENTs simply did
// not exist, Parse reported success, and the panel showed a calendar with its
// last events missing. Found in production with an 8.9MB feed against the
// then-8MB cap, which cut 559KB off every single fetch.
func TestHTTPGetRejectsOversizedFeed(t *testing.T) {
	big := bytes.Repeat([]byte("X"), maxFeedBytes+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(big)
	}))
	defer srv.Close()

	_, err := httpGet(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("oversized feed returned no error; it would parse as a truncated calendar")
	}
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("error is %v, want it to wrap ErrTruncated", err)
	}
}

// A feed exactly at the cap is fine: the extra byte read is what separates
// "exactly full" from "overflowing", and it must not trip the check.
func TestHTTPGetAcceptsFeedAtExactlyTheCap(t *testing.T) {
	exact := bytes.Repeat([]byte("Y"), maxFeedBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(exact)
	}))
	defer srv.Close()

	body, err := httpGet(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("feed at exactly the cap was rejected: %v", err)
	}
	if len(body) != maxFeedBytes {
		t.Errorf("got %d bytes, want %d", len(body), maxFeedBytes)
	}
}

// Fetch dispatches on the source's kind, and an unimplemented or unknown
// backend must ERROR rather than return zero events.
//
// Returning (nil, nil) would reach the panel as a calendar with nothing in it,
// which is indistinguishable from a genuinely empty day -- the same silent
// failure shape as a truncated feed. An error at least lights up
// ViewModel.Stale.
func TestFetchDispatchesOnKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     config.CalendarSource
		wantErr string
	}{
		{
			// Fetch (as opposed to FetchWith) has no token source, so a Google
			// source must say so rather than return an empty calendar.
			name:    "google without a token source",
			src:     config.CalendarSource{Name: "G", Kind: config.KindGoogle, CalID: "x@example.com"},
			wantErr: "no google token source",
		},
		{
			name:    "google without a cal_id",
			src:     config.CalendarSource{Name: "G", Kind: config.KindGoogle},
			wantErr: "no cal_id",
		},
		{
			name:    "an unknown kind is rejected",
			src:     config.CalendarSource{Name: "Q", Kind: "carrier-pigeon"},
			wantErr: "unknown kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs, err := Fetch(context.Background(), tc.src, ref, 7, time.UTC)
			if err == nil {
				t.Fatalf("got nil error and %d events, want an error", len(evs))
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if evs != nil {
				t.Errorf("got %d events alongside the error, want none", len(evs))
			}
		})
	}
}

// An empty Kind is what every config written before the Google backend has,
// and it must still take the iCal path.
func TestFetchTreatsEmptyKindAsICal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(load(t, "basic.ics"))
	}))
	defer srv.Close()

	evs, err := Fetch(context.Background(), config.CalendarSource{
		Name: "Legacy", URL: srv.URL,
	}, ref, 7, time.UTC)
	if err != nil {
		t.Fatalf("empty Kind did not take the iCal path: %v", err)
	}
	if len(evs) == 0 {
		t.Error("iCal path returned no events for a fixture that has them")
	}
}
