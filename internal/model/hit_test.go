package model

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

func hitEvents(now time.Time) []calendar.Event {
	return []calendar.Event{
		{Title: "Standup", UID: "a", Color: "#4f9cff",
			Start: now.Add(30 * time.Minute), End: now.Add(45 * time.Minute)},
		{Title: "Design Review", UID: "b", Color: "#ff7a59",
			Start: now.Add(90 * time.Minute), End: now.Add(150 * time.Minute)},
	}
}

// A tap on a card must return that card's event. The centre of each rect is
// the least interesting point to test but the one a user actually hits, so it
// is the baseline every other case is measured against.
func TestEventAtHitsTheCardUnderTheTap(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	row := BuildAgenda(hitEvents(now), now, false)

	rects := row.CardRects()
	if len(rects) == 0 {
		t.Fatal("no card rects produced")
	}
	for _, cr := range rects {
		cx := cr.Rect.X + cr.Rect.W/2
		cy := cr.Rect.Y + cr.Rect.H/2
		got, ok := row.EventAt(cx, cy)
		if !ok {
			t.Fatalf("centre of %q card (%v,%v) hit nothing", cr.Event.Title, cx, cy)
		}
		if got.Title != cr.Event.Title {
			t.Errorf("tap at (%v,%v) returned %q, want %q", cx, cy, got.Title, cr.Event.Title)
		}
	}
}

// Above and below the agenda band are other surfaces -- the all-day ribbon and
// the weather chart. A tap there must not open an event sheet.
func TestEventAtIgnoresTapsOutsideTheBand(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	row := BuildAgenda(hitEvents(now), now, false)
	rects := row.CardRects()
	if len(rects) == 0 {
		t.Fatal("no card rects produced")
	}
	cx := rects[0].Rect.X + rects[0].Rect.W/2

	for _, tc := range []struct {
		name string
		y    float64
	}{
		{"in the all-day ribbon", 10},
		{"below the agenda band", RibbonHeightPx + AgendaHeightPx + 20},
	} {
		if _, ok := row.EventAt(cx, tc.y); ok {
			t.Errorf("tap %s (y=%v) hit a card", tc.name, tc.y)
		}
	}
}

// The left panel is the clock and headline, not the agenda. A tap there must
// never reach a card -- cards are laid out from x=NowBlockW, but the row is
// scrolled left by a negative offset, so an off-by-one in the offset maths
// would put card rects under the left panel where they are not visible.
func TestEventAtIgnoresTheLeftPanel(t *testing.T) {
	now := time.Date(2026, 8, 25, 15, 0, 0, 0, time.UTC)
	// Events earlier in the day force a real negative scroll offset.
	var evs []calendar.Event
	for i := range 6 {
		s := now.Add(time.Duration(-5+i) * time.Hour)
		evs = append(evs, calendar.Event{
			Title: "E", UID: "u", Start: s, End: s.Add(30 * time.Minute),
		})
	}
	row := BuildAgenda(evs, now, false)
	if row.OffsetPx >= 0 {
		t.Fatalf("fixture does not scroll: OffsetPx = %v", row.OffsetPx)
	}
	for x := 0.0; x < NowBlockW; x += 20 {
		for y := 0.0; y < PanelHeight; y += 40 {
			if _, ok := row.EventAt(x, y); ok {
				t.Fatalf("tap on the left panel at (%v,%v) hit a card", x, y)
			}
		}
	}
}

// Muting must remove the event from every surface at once. A mute that hid the
// card but left the headline counting down to it would read as a bug.
func TestBuildDropsMutedEventsEverywhere(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := hitEvents(now)
	evs = append(evs, calendar.Event{
		Title: "Company Holiday", UID: "c", AllDay: true,
		Start: now.Truncate(24 * time.Hour), End: now.Add(24 * time.Hour),
	})

	cfg := &config.Config{}
	cfg.Location.Timezone = "UTC"

	full := Build(now, cfg, evs, nil, nil, nil)
	if len(full.Agenda.CardRects()) == 0 {
		t.Fatal("baseline has no cards")
	}
	if len(full.AllDay) != 1 {
		t.Fatalf("baseline has %d all-day events, want 1", len(full.AllDay))
	}

	// Mute the timed event the headline is pointing at, plus the all-day one.
	cfg.Mute(config.MutedEvent{Key: evs[0].Key()})
	cfg.Mute(config.MutedEvent{Key: evs[2].Key()})

	got := Build(now, cfg, evs, nil, nil, nil)
	for _, cr := range got.Agenda.CardRects() {
		if cr.Event.Title == "Standup" {
			t.Error("muted event still has a card in the agenda")
		}
	}
	if len(got.AllDay) != 0 {
		t.Errorf("muted all-day event still in the ribbon: %+v", got.AllDay)
	}
	if got.NextEvent != nil && got.NextEvent.Title == "Standup" {
		t.Error("muted event is still the NEXT headline")
	}
}

// A recurring series shares one UID across every occurrence. Keying a mute on
// UID alone would hide the whole series from one tap, which is not what
// "hide this event" says.
func TestKeyDistinguishesOccurrencesOfASeries(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	a := calendar.Event{Title: "Standup", UID: "series-1", Start: base}
	b := calendar.Event{Title: "Standup", UID: "series-1", Start: base.AddDate(0, 0, 1)}
	if a.Key() == b.Key() {
		t.Errorf("two occurrences share a key (%q); muting one would mute the series", a.Key())
	}
}

// The key must not change when the display timezone does, or a timezone edit
// would silently unmute everything the user had hidden.
func TestKeyIsStableAcrossTimezones(t *testing.T) {
	utc := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	a := calendar.Event{Title: "Standup", UID: "u", Start: utc}
	b := calendar.Event{Title: "Standup", UID: "u", Start: utc.In(ny)}
	if a.Key() != b.Key() {
		t.Errorf("same instant produced different keys: %q vs %q", a.Key(), b.Key())
	}
}

// Each row of a stack is tappable on its own, so two conflicting meetings can
// be told apart by touching the one you mean.
func TestEventAtHitsIndividualStackRows(t *testing.T) {
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "JP office hours", UID: "a", Color: "#4f9cff",
			Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)},
		{Title: "Product leads", UID: "b", Color: "#ff7a59",
			Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)},
	}
	row := BuildAgenda(evs, now, false)

	rects := row.CardRects()
	if len(rects) != 2 {
		t.Fatalf("got %d rects, want 2 -- one per stacked row", len(rects))
	}
	// The rows must not overlap, or the upper one would swallow taps meant for
	// the lower.
	if rects[0].Rect.Y+rects[0].Rect.H > rects[1].Rect.Y {
		t.Errorf("row 0 ends at y=%v but row 1 starts at y=%v",
			rects[0].Rect.Y+rects[0].Rect.H, rects[1].Rect.Y)
	}
	for _, cr := range rects {
		cx := cr.Rect.X + cr.Rect.W/2
		cy := cr.Rect.Y + cr.Rect.H/2
		got, ok := row.EventAt(cx, cy)
		if !ok {
			t.Fatalf("centre of the %q row hit nothing", cr.Event.Title)
		}
		if got.Title != cr.Event.Title {
			t.Errorf("tap on the %q row returned %q", cr.Event.Title, got.Title)
		}
	}
}

// The summary row stands for several events, so it opens the conflict list
// rather than any one event's sheet. Returning the zero event from EventAt
// would raise a blank dialog, which is why the two lookups are separate.
func TestOverflowRowIsTappableAsAGroup(t *testing.T) {
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	start, end := now.Add(2*time.Hour), now.Add(3*time.Hour)
	var evs []calendar.Event
	for _, title := range []string{"One", "Two", "Three", "Four", "Five"} {
		evs = append(evs, calendar.Event{
			Title: title, UID: title, Color: "#4f9cff", Start: start, End: end,
		})
	}
	row := BuildAgenda(evs, now, false)

	rects := row.CardRects()
	if len(rects) != MaxStackRows {
		t.Fatalf("got %d rects, want %d", len(rects), MaxStackRows)
	}
	last := rects[MaxStackRows-1]
	if len(last.Overflow) != 3 {
		t.Fatalf("the last rect stands for %d events, want 3", len(last.Overflow))
	}

	cx := last.Rect.X + last.Rect.W/2
	cy := last.Rect.Y + last.Rect.H/2
	if _, ok := row.EventAt(cx, cy); ok {
		t.Error("EventAt claimed the summary row; it stands for several events")
	}
	got, ok := row.OverflowAt(cx, cy)
	if !ok {
		t.Fatal("OverflowAt missed the summary row")
	}
	if len(got) != 3 || got[0].Title != "Three" {
		t.Errorf("summary row returned %d events starting %q, want 3 from \"Three\"",
			len(got), got[0].Title)
	}

	// A tap on the rows above it still opens a single event.
	if _, ok := row.OverflowAt(cx, rects[0].Rect.Y+rects[0].Rect.H/2); ok {
		t.Error("OverflowAt claimed an ordinary event row")
	}
}
