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

	t.Run("with history the last event is leftmost", func(t *testing.T) {
		evs := []calendar.Event{
			{Title: "Older", Start: now.Add(-5 * time.Hour), End: now.Add(-4 * time.Hour)},
			{Title: "Past", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)},
			{Title: "Next", Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)},
		}
		row := BuildAgenda(evs, now, false)

		// The row scrolls back to the most recent event card, not to a fixed
		// number of entries: the entries between it and now are free-time
		// chips, and stopping at the nearest one scrolled the event itself off
		// the left edge -- which is the context the row exists to show.
		//
		// The first VISIBLE event card, not the first in the slice: earlier
		// ones are scrolled off to the left and are exactly what this rule is
		// meant to hide. (The parser trims to one past event before the model
		// sees it; this fixture passes several directly, which also proves the
		// scroll does the right thing if that trim ever changes.)
		var first *Card
		for i := range row.Cards {
			c := &row.Cards[i]
			if (c.Kind == CardEvent || c.Kind == CardStack) && screenLeft(row, *c) >= 0 {
				first = c
				break
			}
		}
		if first == nil {
			t.Fatal("no visible event card in the row")
		}
		if got := screenLeft(row, *first); got != CardPadPx {
			t.Errorf("the last event starts at %.1f, want %d -- it should be the "+
				"leftmost thing on screen", got, CardPadPx)
		}
		// And it is genuinely a past one, not the upcoming event.
		if !first.Event.End.Before(now) {
			t.Errorf("leftmost card is %q, which has not finished; want the last "+
				"completed event", first.Event.Title)
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

// The upcoming half of a gap shows how long it is; the elapsed half does not.
//
// Time still to come is something you act on. Time already spent is not -- and
// printing "3h30m free" for it invites reading the number as upcoming, which is
// exactly the confusion the split was meant to remove.
func TestSameDayGapKeepsItsDuration(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "A", Start: now.Add(-3 * time.Hour), End: now.Add(-time.Hour)},
		{Title: "B", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	}
	row := BuildAgenda(evs, now, false)

	var gaps []Card
	for _, c := range row.Cards {
		if c.Kind == CardGap {
			gaps = append(gaps, c)
		}
	}
	if len(gaps) != 2 {
		t.Fatalf("got %d gap chips, want the elapsed and upcoming halves", len(gaps))
	}
	if gaps[0].GapText != "" {
		t.Errorf("the elapsed chip reads %q; it should carry no text", gaps[0].GapText)
	}
	if gaps[1].GapText != "1h" {
		t.Errorf("the upcoming chip reads %q, want %q", gaps[1].GapText, "1h")
	}
	if gaps[1].Overnight {
		t.Error("a gap inside one day was marked Overnight")
	}
}

// The last finished event stays on the row however long ago it ended, with the
// elapsed free time beside it.
//
// Without the past card the panel showed only what is next, so at 16:42 a row
// reading "OVERNIGHT | Tomorrow | 08:30" gave no sense of when the day's work
// actually stopped. The elapsed chip is what makes the past card read as
// history rather than as something that just finished.
func TestLastEventStaysWithElapsedFreeTime(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 42, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Office hours", Start: now.Add(-4*time.Hour - 30*time.Minute), End: now.Add(-3*time.Hour - 30*time.Minute)},
		{Title: "Exercise class", Start: now.Add(16 * time.Hour), End: now.Add(17 * time.Hour)},
	}
	row := BuildAgenda(evs, now, false)

	if len(row.Cards) < 5 {
		t.Fatalf("row has %d cards, want past / elapsed / overnight / sep / next", len(row.Cards))
	}
	if row.Cards[0].Kind != CardEvent || row.Cards[0].Event.Title != "Office hours" {
		t.Errorf("first card is %v %q, want the last finished event",
			row.Cards[0].Kind, row.Cards[0].Event.Title)
	}
	// Bare on purpose: how long ago the last event finished is true but not
	// actionable, and a number there reads as something upcoming.
	if row.Cards[1].Kind != CardGap {
		t.Errorf("second card is %v, want the elapsed chip", row.Cards[1].Kind)
	}
	if row.Cards[1].GapText != "" {
		t.Errorf("the elapsed chip reads %q; it should carry no text",
			row.Cards[1].GapText)
	}
	if row.Cards[2].Kind != CardGap || !row.Cards[2].Overnight {
		t.Errorf("third card is %v (overnight=%v), want the overnight chip",
			row.Cards[2].Kind, row.Cards[2].Overnight)
	}
	if row.Cards[3].Kind != CardDaySep {
		t.Errorf("fourth card is %v, want the Tomorrow separator", row.Cards[3].Kind)
	}
}

// The NOW bar sits between the two halves of a straddling gap: the elapsed
// part is behind it, the part still to come ahead.
func TestNowBarSitsBetweenTheGapHalves(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 42, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Past", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)},
		{Title: "Next", Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)},
	}
	row := BuildAgenda(evs, now, false)

	var elapsed, ahead *Card
	for i := range row.Cards {
		if row.Cards[i].Kind != CardGap {
			continue
		}
		if elapsed == nil {
			elapsed = &row.Cards[i]
		} else if ahead == nil {
			ahead = &row.Cards[i]
		}
	}
	if elapsed == nil || ahead == nil {
		t.Fatalf("want two gap chips around now, got %d cards", len(row.Cards))
	}
	if got := row.NowBarXPx; got < screenLeft(row, *ahead)-1 || got > screenLeft(row, *ahead)+1 {
		t.Errorf("NOW bar at %.1f, want the left edge of the second chip at %.1f",
			got, screenLeft(row, *ahead))
	}
}

// With no history there is nothing elapsed to show, so no chip for it.
func TestNoElapsedChipWithoutHistory(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 42, 0, 0, time.UTC)
	evs := []calendar.Event{{Title: "Exercise", Start: now.Add(16 * time.Hour), End: now.Add(17 * time.Hour)}}
	row := BuildAgenda(evs, now, false)

	if row.Cards[0].Kind == CardGap && row.Cards[0].GapText != "" {
		t.Errorf("a %q elapsed chip was emitted with no preceding event",
			row.Cards[0].GapText)
	}
	if row.Cards[0].Kind != CardGap || !row.Cards[0].Overnight {
		t.Errorf("first card is %v, want the overnight chip", row.Cards[0].Kind)
	}
}

// Ordinary gaps between upcoming events must produce chips.
//
// This is the plain case -- neither straddling now nor crossing midnight --
// and it regressed silently: restructuring the gap block around a `split` flag
// dropped the append, so the chip was constructed, its width added to the row's
// x, and then discarded. Every break in the day vanished while the layout still
// reserved space for them.
//
// The straddling and overnight cases had tests; this one did not, which is why
// a whole day of missing free time reached the panel.
func TestGapsBetweenUpcomingEventsAppear(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 15, 0, 0, time.UTC)
	evs := []calendar.Event{
		// In progress, so the row opens with a stack rather than a gap.
		{Title: "Otter Team", Start: now.Add(-15 * time.Minute), End: now.Add(15 * time.Minute)},
		{Title: "JP office hours", Start: now.Add(45 * time.Minute), End: now.Add(75 * time.Minute)},
		{Title: "SafeInsights", Start: now.Add(4 * time.Hour), End: now.Add(5 * time.Hour)},
	}
	row := BuildAgenda(evs, now, false)

	var gaps []string
	for _, c := range row.Cards {
		if c.Kind == CardGap {
			gaps = append(gaps, c.GapText)
		}
	}
	want := []string{"30m", "2h45m"}
	if len(gaps) != len(want) {
		t.Fatalf("got %d gap chips %v, want %v -- the breaks between events are missing",
			len(gaps), gaps, want)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("gap %d reads %q, want %q", i, gaps[i], want[i])
		}
	}

	// And the chips are in the row, not merely accounted for in its width: a
	// card's XPx must not overlap the one before it.
	prevRight := -1.0
	for _, c := range row.Cards {
		if c.XPx < prevRight {
			t.Errorf("card at XPx=%.0f overlaps the previous entry ending at %.0f",
				c.XPx, prevRight)
		}
		prevRight = c.XPx + c.WidthPx
	}
}

// The case this feature exists for: two meetings booked at the same hour, both
// still ahead. Laid out side by side they read as one after the other, which is
// exactly the thing the row must not say.
func TestFutureConflictStacks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("JP office hours", now.Add(2*time.Hour), now.Add(3*time.Hour)),
		agEv("Product leads", now.Add(2*time.Hour), now.Add(3*time.Hour)),
	}
	row := BuildAgenda(evs, now, false)

	var stacks, loose int
	for _, c := range row.Cards {
		switch c.Kind {
		case CardStack:
			stacks++
			if len(c.Stacked) != 2 {
				t.Errorf("stack holds %d events, want 2", len(c.Stacked))
			}
			if c.State == StateCurrent {
				t.Error("a stack two hours out is painted as in-progress")
			}
			if c.ElapsedPct != 0 {
				t.Errorf("ElapsedPct is %v, want 0 -- nothing has elapsed in a "+
					"future conflict and the shade would be drawn over it",
					c.ElapsedPct)
			}
			if c.WidthPx != StackWidthPx {
				t.Errorf("stack is %vpx wide, want StackWidthPx (%v) -- only the "+
					"current slot is duration-scaled", c.WidthPx, StackWidthPx)
			}
		case CardEvent:
			loose++
		}
	}
	if stacks != 1 {
		t.Errorf("got %d stacks, want exactly 1", stacks)
	}
	if loose != 0 {
		t.Errorf("got %d loose cards, want 0 -- both events belong to the stack", loose)
	}
}

// A partial overlap is still a conflict: a short call inside a long block
// collides with it just as surely as two events sharing a start time.
func TestPartialOverlapStacks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("Deep work", now.Add(1*time.Hour), now.Add(3*time.Hour)),
		agEv("Quick sync", now.Add(90*time.Minute), now.Add(2*time.Hour)),
	}
	row := BuildAgenda(evs, now, false)

	for _, c := range row.Cards {
		if c.Kind == CardStack {
			if len(c.Stacked) != 2 {
				t.Errorf("stack holds %d events, want 2", len(c.Stacked))
			}
			return
		}
	}
	t.Error("the nested event did not stack with the block containing it")
}

// Overlap chains: A and C never touch, but both touch B, so all three are one
// conflict. Grouping only the pairs that overlap directly would put A and C in
// separate slots and imply they are unrelated.
func TestOverlapChainsTransitively(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("A", now.Add(60*time.Minute), now.Add(90*time.Minute)),
		agEv("B", now.Add(75*time.Minute), now.Add(135*time.Minute)),
		agEv("C", now.Add(120*time.Minute), now.Add(150*time.Minute)),
	}
	row := BuildAgenda(evs, now, false)

	for _, c := range row.Cards {
		if c.Kind == CardStack {
			if len(c.Stacked) != 3 {
				t.Errorf("stack holds %d events, want 3 (A-B-C chained)", len(c.Stacked))
			}
			return
		}
	}
	t.Error("no stack: the chain was split into separate slots")
}

// Events that merely sit next to each other must NOT stack. Back-to-back is the
// common case, and collapsing it would turn an ordinary day into a wall of
// conflicts.
func TestAdjacentEventsDoNotStack(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		agEv("First", now.Add(1*time.Hour), now.Add(2*time.Hour)),
		// Starts exactly when the first ends: touching, not overlapping.
		agEv("Second", now.Add(2*time.Hour), now.Add(3*time.Hour)),
	}
	row := BuildAgenda(evs, now, false)

	var loose, stacks int
	for _, c := range row.Cards {
		switch c.Kind {
		case CardEvent:
			loose++
		case CardStack:
			stacks++
		}
	}
	if stacks != 0 {
		t.Errorf("got %d stacks, want 0 -- these events do not overlap", stacks)
	}
	if loose != 2 {
		t.Errorf("got %d loose cards, want 2", loose)
	}
}

// Past the cap the last row stops being an event and becomes a summary of the
// rest, so five conflicting events still fit three rows without dropping two of
// them silently.
func TestStackCapsRowsAndSummarizesTheRest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	start, end := now.Add(2*time.Hour), now.Add(3*time.Hour)
	evs := []calendar.Event{
		agEv("One", start, end),
		agEv("Two", start, end),
		agEv("Three", start, end),
		agEv("Four", start, end),
		agEv("Five", start, end),
	}
	row := BuildAgenda(evs, now, false)

	for _, c := range row.Cards {
		if c.Kind != CardStack {
			continue
		}
		if len(c.Stacked) != MaxStackRows {
			t.Fatalf("stack has %d rows, want %d", len(c.Stacked), MaxStackRows)
		}
		// The first two rows are events in their own right.
		for i, want := range []string{"One", "Two"} {
			if got := c.Stacked[i].Event.Title; got != want {
				t.Errorf("row %d is %q, want %q", i, got, want)
			}
			if c.Stacked[i].IsOverflow() {
				t.Errorf("row %d is a summary, want an event", i)
			}
		}
		last := c.Stacked[MaxStackRows-1]
		if !last.IsOverflow() {
			t.Fatal("the last row is an event, want the summary of the rest")
		}
		if len(last.Overflow) != 3 {
			t.Errorf("summary stands for %d events, want 3", len(last.Overflow))
		}
		// The titles are what the row shows -- a count alone would not say
		// which meetings were folded away.
		if got := last.OverflowText(); got != "Three · Four · Five" {
			t.Errorf("summary reads %q, want %q", got, "Three · Four · Five")
		}
		return
	}
	t.Error("no stack was produced")
}

// A short event nested inside a long one must not rewind the boundary the next
// gap is measured from. It used to: prevEnd took each event's end unconditially,
// so the nested event's earlier end became the start of the following gap and
// the chip over-reported the free time by the remainder of the block.
func TestNestedEventDoesNotRewindTheGap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		// In progress, so the row opens with a stack rather than a gap.
		agEv("Otter Team", now.Add(-15*time.Minute), now.Add(15*time.Minute)),
		agEv("Deep work", now.Add(1*time.Hour), now.Add(3*time.Hour)),
		// Ends 90 minutes before the block it sits inside does.
		agEv("Quick sync", now.Add(90*time.Minute), now.Add(2*time.Hour)),
		agEv("Retro", now.Add(4*time.Hour), now.Add(5*time.Hour)),
	}
	row := BuildAgenda(evs, now, false)

	var gaps []string
	for _, c := range row.Cards {
		if c.Kind == CardGap && c.GapText != "" {
			gaps = append(gaps, c.GapText)
		}
	}
	// The gap after the cluster runs from the block's end (+3h) to the retro
	// (+4h). Measured from the nested event's end it would read 2h.
	want := []string{"45m", "1h"}
	if len(gaps) != len(want) {
		t.Fatalf("got gaps %v, want %v", gaps, want)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("gap %d reads %q, want %q -- measured from the wrong boundary",
				i, gaps[i], want[i])
		}
	}
}
