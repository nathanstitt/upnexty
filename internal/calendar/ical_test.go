package calendar

import (
	"os"
	"testing"
	"time"
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
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 1, "")
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
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
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
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "owner@example.com")
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
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "owner@example.com")
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
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
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

func TestParseUnescapesText(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
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
