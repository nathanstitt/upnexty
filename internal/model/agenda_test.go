package model

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

func agEv(title string, start, end time.Time) calendar.Event {
	return calendar.Event{Title: title, Start: start, End: end, Color: "#4a90d9"}
}

// Overlapping in-progress events must collapse into ONE slot, so the single NOW
// bar bisects them all at the same elapsed point. Emitting a card each would put
// the bar inside only the first.
func TestBuildAgendaStacksSimultaneousEvents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 30, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("A", now.Add(-20*time.Minute), now.Add(40*time.Minute)),
		agEv("B", now.Add(-10*time.Minute), now.Add(20*time.Minute)),
	}
	row := BuildAgenda(evs, now, false)

	var stacks, events int
	for _, c := range row.Cards {
		switch c.Kind {
		case CardStack:
			stacks++
			if len(c.Stacked) != 2 {
				t.Errorf("stack holds %d events, want 2", len(c.Stacked))
			}
		case CardEvent:
			events++
		}
	}
	if stacks != 1 {
		t.Errorf("got %d stacks, want exactly 1", stacks)
	}
	if events != 0 {
		t.Errorf("got %d loose event cards, want 0 (both are in the stack)", events)
	}
}

// The elapsed shade spans the slot, not the individual card: the span runs from
// the earliest start to the latest end.
func TestBuildAgendaStackElapsedSpansTheWholeSlot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 30, 0, 0, time.UTC)
	// Slot spans 10:00..11:00; now is 10:30, so exactly half elapsed.
	evs := []calendar.Event{
		agEv("A", now.Add(-30*time.Minute), now.Add(30*time.Minute)),
		agEv("B", now.Add(-10*time.Minute), now.Add(10*time.Minute)),
	}
	row := BuildAgenda(evs, now, false)
	for _, c := range row.Cards {
		if c.Kind != CardStack {
			continue
		}
		if c.ElapsedPct < 49 || c.ElapsedPct > 51 {
			t.Errorf("ElapsedPct = %.1f, want ~50 (10:30 through a 10:00-11:00 slot)", c.ElapsedPct)
		}
		return
	}
	t.Fatal("no stack card produced")
}

// A day separator marks the crossing into a new day so a multi-day agenda does
// not read as one continuous run of cards.
func TestBuildAgendaEmitsDaySeparator(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("today", now.Add(time.Hour), now.Add(2*time.Hour)),
		agEv("tomorrow", now.AddDate(0, 0, 1), now.AddDate(0, 0, 1).Add(time.Hour)),
	}
	row := BuildAgenda(evs, now, false)

	for _, c := range row.Cards {
		if c.Kind == CardDaySep {
			if c.SepText != "Tomorrow" {
				t.Errorf("separator text = %q, want %q", c.SepText, "Tomorrow")
			}
			return
		}
	}
	t.Error("no day separator emitted for an agenda crossing midnight")
}

// The row only ever shifts left. A positive offset would open a blank strip at
// the agenda's left edge.
func TestBuildAgendaNeverShiftsRight(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	// A single event far in the future: nowOffset lands left of the bar.
	evs := []calendar.Event{agEv("later", now.Add(6*time.Hour), now.Add(7*time.Hour))}
	row := BuildAgenda(evs, now, false)
	if row.OffsetPx > 0 {
		t.Errorf("OffsetPx = %v, want <= 0", row.OffsetPx)
	}
}

func TestBuildAgendaMarksImminentAndPast(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("done", now.Add(-2*time.Hour), now.Add(-time.Hour)),
		agEv("soon", now.Add(10*time.Minute), now.Add(40*time.Minute)),
		agEv("later", now.Add(5*time.Hour), now.Add(6*time.Hour)),
	}
	row := BuildAgenda(evs, now, false)

	want := map[string]CardState{"done": StatePast, "soon": StateSoon, "later": StateFuture}
	for _, c := range row.Cards {
		if c.Kind != CardEvent {
			continue
		}
		if w, ok := want[c.Event.Title]; ok && c.State != w {
			t.Errorf("%q state = %v, want %v", c.Event.Title, c.State, w)
		}
	}
}

// The NOW bar is drawn at NowBarXPx against #timeline-zone, while the cards are
// laid out by flex inside #agenda-row. Nothing in either package forces those
// two coordinate systems to agree, and when they drifted the bar sat beside the
// current card instead of through it -- with no test failing.
//
// This walks the cards the way the template's flex row does (each card followed
// by CardGapPx, the whole row translated by OffsetPx and sitting inside
// CardPadPx of padding) and checks that the moment "now" lands under the bar.
func TestNowBarLandsOnTheCurrentCard(t *testing.T) {
	now := time.Date(2026, 8, 28, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		evs  []calendar.Event
	}{
		{"inside an event", []calendar.Event{
			{Title: "Past", Start: now.Add(-90 * time.Minute), End: now.Add(-60 * time.Minute)},
			{Title: "Current", Start: now.Add(-10 * time.Minute), End: now.Add(20 * time.Minute)},
			{Title: "Later", Start: now.Add(90 * time.Minute), End: now.Add(120 * time.Minute)},
		}},
		{"inside a gap", []calendar.Event{
			{Title: "Before", Start: now.Add(-60 * time.Minute), End: now.Add(-30 * time.Minute)},
			{Title: "After", Start: now.Add(30 * time.Minute), End: now.Add(60 * time.Minute)},
		}},
		{"first card of the day still ahead", []calendar.Event{
			{Title: "Later", Start: now.Add(120 * time.Minute), End: now.Add(150 * time.Minute)},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := BuildAgenda(tc.evs, now, false)

			// Reproduce the template's flex packing: each card followed by
			// CardGapPx, with the whole row translated by OffsetPx and sitting
			// inside CardPadPx of padding.
			x := 0.0
			var nowScreenX float64
			found := false
			for _, c := range row.Cards {
				left := x + row.OffsetPx + CardPadPx
				switch {
				case c.Kind == CardStack:
					nowScreenX = left + c.ElapsedPct/100*c.WidthPx
					found = true
				case c.Kind == CardGap && !found:
					// The bar may fall inside a free-time chip.
					if row.NowBarXPx >= left && row.NowBarXPx <= left+c.WidthPx {
						nowScreenX = row.NowBarXPx
						found = true
					}
				}
				x += c.WidthPx + CardGapPx
			}
			if !found {
				t.Skip("no current card or gap in this fixture")
			}
			if diff := nowScreenX - row.NowBarXPx; diff < -1.5 || diff > 1.5 {
				t.Errorf("now lands at x=%.1f but the bar is drawn at %.1f (off by %.1f)",
					nowScreenX, row.NowBarXPx, diff)
			}
		})
	}
}

// screenLeft is where a card's left edge lands on the panel, in the same
// viewport coordinates NowBarXPx uses.
func screenLeft(row CardRow, c Card) float64 { return c.XPx + row.OffsetPx + CardPadPx }

// The bar sweeps across the entry containing now rather than sitting at a fixed
// fraction of the viewport, and the row shifts a whole entry at a time. An
// earlier layout pinned the bar at 30% and scrolled the row continuously
// underneath it, which needed 462px of blank lead rail to work on a quiet
// morning -- dead space across nearly a third of the panel.
func TestNowBarSweepsTheAnchorEntry(t *testing.T) {
	base := time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Past", Start: base.Add(-90 * time.Minute), End: base.Add(-60 * time.Minute)},
		{Title: "Current", Start: base, End: base.Add(60 * time.Minute)},
		{Title: "Later", Start: base.Add(120 * time.Minute), End: base.Add(150 * time.Minute)},
	}

	// Quarter, half, and three-quarters through the current meeting: the bar
	// advances monotonically and the row does not move.
	var prevBar, prevOffset float64
	for i, frac := range []float64{0.25, 0.5, 0.75} {
		now := base.Add(time.Duration(frac * float64(60*time.Minute)))
		row := BuildAgenda(evs, now, false)

		var stack Card
		for _, c := range row.Cards {
			if c.Kind == CardStack {
				stack = c
			}
		}
		if stack.WidthPx == 0 {
			t.Fatalf("frac %v: no current stack", frac)
		}

		left := screenLeft(row, stack)
		want := left + frac*stack.WidthPx
		if diff := row.NowBarXPx - want; diff < -1.5 || diff > 1.5 {
			t.Errorf("frac %v: bar at %.1f, want %.1f (off by %.1f)", frac, row.NowBarXPx, want, diff)
		}
		if i > 0 {
			if row.NowBarXPx <= prevBar {
				t.Errorf("frac %v: bar did not advance (%.1f then %.1f)", frac, prevBar, row.NowBarXPx)
			}
			if row.OffsetPx != prevOffset {
				t.Errorf("frac %v: row moved mid-sweep (%.1f then %.1f)", frac, prevOffset, row.OffsetPx)
			}
		}
		prevBar, prevOffset = row.NowBarXPx, row.OffsetPx
	}

	// Once the meeting ends the row shifts left and the bar resets: it is no
	// longer further right than it was three-quarters of the way through.
	after := BuildAgenda(evs, base.Add(70*time.Minute), false)
	if after.OffsetPx >= prevOffset {
		t.Errorf("row did not shift after the entry ended (%.1f then %.1f)", prevOffset, after.OffsetPx)
	}
	if after.NowBarXPx >= prevBar {
		t.Errorf("bar did not reset after the entry ended (%.1f then %.1f)", prevBar, after.NowBarXPx)
	}
}

// Exactly one entry precedes the anchor, so the user keeps some context for
// what just finished -- and when the day has nothing before it, the anchor sits
// flush left instead of being pushed right by blank rail.
func TestAgendaShowsOnePrecedingEntry(t *testing.T) {
	now := time.Date(2026, 8, 28, 14, 30, 0, 0, time.UTC)

	t.Run("with history the preceding entry is leftmost", func(t *testing.T) {
		evs := []calendar.Event{
			{Title: "Older", Start: now.Add(-5 * time.Hour), End: now.Add(-4 * time.Hour)},
			{Title: "Past", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)},
			{Title: "Next", Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)},
		}
		row := BuildAgenda(evs, now, false)

		// The anchor is the gap chip spanning now; the entry before it should
		// be the leftmost thing on screen, at the row's padding.
		anchor := -1
		for i, c := range row.Cards {
			if c.Kind == CardGap && row.NowBarXPx >= screenLeft(row, c) {
				anchor = i
			}
		}
		if anchor <= 0 {
			t.Fatalf("expected a gap chip with a preceding entry, cards = %d", len(row.Cards))
		}
		if got := screenLeft(row, row.Cards[anchor-1]); got != CardPadPx {
			t.Errorf("preceding entry starts at %.1f, want %d", got, CardPadPx)
		}
		// Everything before it is scrolled off to the left.
		for _, c := range row.Cards[:anchor-1] {
			if screenLeft(row, c) >= CardPadPx {
				t.Errorf("entry at %.1f was not scrolled off", screenLeft(row, c))
			}
		}
	})

	t.Run("without history the anchor is flush left", func(t *testing.T) {
		evs := []calendar.Event{{Title: "First", Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)}}
		row := BuildAgenda(evs, now, false)

		if row.OffsetPx != 0 {
			t.Errorf("OffsetPx = %v, want 0", row.OffsetPx)
		}
		if got := screenLeft(row, row.Cards[0]); got != CardPadPx {
			t.Errorf("first card starts at %.1f, want %d -- no blank rail before it", got, CardPadPx)
		}
	})
}

// The current slot is duration-scaled, so a long meeting is wider than the
// panel; without a clamp the sweep carries the bar off the right edge and it
// vanishes partway through the meeting.
func TestNowBarStaysOnPanelDuringALongMeeting(t *testing.T) {
	base := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{{Title: "All morning", Start: base, End: base.Add(4 * time.Hour)}}

	for _, frac := range []float64{0.1, 0.5, 0.9, 0.99} {
		now := base.Add(time.Duration(frac * float64(4*time.Hour)))
		row := BuildAgenda(evs, now, false)
		if row.NowBarXPx < 0 || row.NowBarXPx > AgendaViewportPx {
			t.Errorf("frac %v: bar at %.1f is off the %d-wide panel",
				frac, row.NowBarXPx, AgendaViewportPx)
		}
	}
}

// An overnight span gets a free-time chip and a day separator, like any gap.
//
// The parser drops events that ended over an hour ago, so late in the day
// tomorrow morning's first meeting becomes index 0 of the row. Gating the chip
// and the separator on i > 0 meant that card landed flush against the NOW bar
// with nothing between them -- at 16:25 the panel showed "Exercise class
// 08:30" as if it were starting imminently. Found on hardware with a real
// calendar.
func TestOvernightGapIsAnEntry(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 25, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Exercise class", Start: now.Add(16 * time.Hour), End: now.Add(17 * time.Hour)},
		{Title: "Research", Start: now.Add(17 * time.Hour), End: now.Add(17*time.Hour + 30*time.Minute)},
	}
	row := BuildAgenda(evs, now, false)

	if len(row.Cards) < 3 {
		t.Fatalf("row has %d cards, want a gap and a separator before the events", len(row.Cards))
	}
	if row.Cards[0].Kind != CardGap {
		t.Errorf("first card is %v, want a free-time chip covering the overnight span",
			row.Cards[0].Kind)
	}
	// An overnight span is labelled, not measured: "19h59m free" is arithmetic
	// nobody reads, and the useful fact is that the day is over.
	if !row.Cards[0].Overnight {
		t.Error("the overnight chip is not marked Overnight, so it would print a duration")
	}
	if got := row.Cards[0].GapText; got != "" {
		t.Errorf("overnight chip carries the duration %q; it should carry none", got)
	}
	var sawSep bool
	for _, c := range row.Cards {
		if c.Kind == CardDaySep {
			sawSep = true
			if c.SepText != "Tomorrow" {
				t.Errorf("day separator reads %q, want %q", c.SepText, "Tomorrow")
			}
		}
	}
	if !sawSep {
		t.Error("no day separator: the row crosses into tomorrow without saying so")
	}
}

// The NOW bar sits inside the overnight chip, not against the first event.
func TestOvernightNowBarIsInTheGap(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 25, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Exercise class", Start: now.Add(16 * time.Hour), End: now.Add(17 * time.Hour)},
	}
	row := BuildAgenda(evs, now, false)

	gap := row.Cards[0]
	if gap.Kind != CardGap {
		t.Fatalf("first card is %v, want the gap", gap.Kind)
	}
	left := gap.XPx + row.OffsetPx + CardPadPx
	if row.NowBarXPx < left || row.NowBarXPx > left+gap.WidthPx {
		t.Errorf("NOW bar at %.1f is outside the gap chip spanning %.1f..%.1f",
			row.NowBarXPx, left, left+gap.WidthPx)
	}
}

// A gap only appears when there is one: back-to-back events still abut.
func TestNoSpuriousGapWhenAnEventIsImminent(t *testing.T) {
	now := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Soon", Start: now.Add(2 * time.Minute), End: now.Add(30 * time.Minute)},
	}
	row := BuildAgenda(evs, now, false)
	if len(row.Cards) > 0 && row.Cards[0].Kind == CardGap {
		t.Errorf("a %s chip was inserted before an event starting in 2 minutes",
			row.Cards[0].GapText)
	}
}

// A gap inside one day still shows how long it is: the label is only dropped
// when the span crosses midnight, where the number stops being useful.
func TestSameDayGapKeepsItsDuration(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "A", Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)},
		{Title: "B", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	}
	for _, c := range BuildAgenda(evs, now, false).Cards {
		if c.Kind != CardGap {
			continue
		}
		if c.Overnight {
			t.Error("a gap inside one day was marked Overnight")
		}
		if c.GapText == "" {
			t.Error("a same-day gap lost its duration")
		}
		return
	}
	t.Fatal("no gap chip emitted")
}
