package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// Agenda geometry, ported from the pi-dashboard reference (app.js/style.css).
//
// The agenda is a departure board, not a proportional timeline: every card is a
// fixed width regardless of duration, laid out left to right in time order. The
// exception is the current slot, which IS duration-scaled so the NOW bar maps
// elapsed progress across it.
//
// This replaces an earlier proportional-timeline model, where card X and width
// came from the event's position in a time window. That put every event in a
// thin strip, merged short adjacent events into one shape, and needed floating
// labels with collision avoidance -- all of which the card layout removes by
// construction.
const (
	CardWidthPx = 196 // fixed width of a non-current card
	CardGapPx   = 14  // gap between cards, chips, and separators in #agenda-row
	CardPadPx   = 18  // #agenda-row horizontal padding

	// Vertical geometry of the agenda band, needed to hit-test a tap against a
	// card. These mirror style.css and are pinned to it by
	// TestStylesheetMatchesModelGeometry -- a card's rect is wrong in a way
	// nothing else notices if they drift.
	RibbonHeightPx  = 40 // .ad-ribbon, above the row
	AgendaHeightPx  = 240
	AgendaRowPadTop = 10 // #agenda-row padding-top
	AgendaRowPadBot = 12 // #agenda-row padding-bottom

	GapChipWidthPx = 86 // width of a free-time chip
	DaySepWidthPx  = 45 // day separator: 1px rule plus its 20/18px margins

	// The current slot is duration-scaled so the NOW bar's position within it
	// tracks elapsed time. 6px/min makes a 1h meeting ~360px.
	AgendaPxPerMin = 6.0
	AgendaMinCurW  = 150.0 // floor so a 5-minute current event still reads
	GapMinutes     = 10    // free time shorter than this is not worth a chip
	// ImminentMinutes is the "soon" threshold, used for two things: the left
	// panel switches from FREE FOR to a countdown inside it (nowblock.go), and a
	// card gets the amber "IN 5M" badge. Lowering it widens the FREE FOR window
	// and narrows the countdown -- at 10, a gap only stops reading as free time
	// in the last ten minutes.
	ImminentMinutes = 10

	// AgendaViewportPx is the visible width of the agenda (panel minus the
	// left block). Layout is computed against this because the panel has no
	// client-side scrolling -- see CardRow.OffsetPx.
	AgendaViewportPx = PanelWidth - NowBlockW // 1540

	// CornerTapPx is the side of the square in the panel's top-left corner that
	// raises the device sheet. It sits over the clock, which has no other tap
	// behaviour, so nothing is displaced.
	//
	// 100px is a deliberate compromise: large enough to hit reliably with a
	// fingertip on glass (the digitizer reports ~5px of jitter), small enough
	// that it does not reach the weather widget below or the agenda to its
	// right. It is also not signposted -- this is a maintenance affordance, not
	// a feature, and a visible control would invite taps that raise a sheet
	// showing a password on a wall.
	CornerTapPx = 100.0

	// NowBarMarginPx keeps the sweeping bar off the right edge. The current
	// slot is duration-scaled at AgendaPxPerMin, so a long meeting is wider
	// than the viewport (4h is 1440px) and the bar would otherwise sweep off
	// the panel entirely partway through it.
	NowBarMarginPx = 24.0
)

// CardKind distinguishes the entry types in the agenda row.
type CardKind int

const (
	CardEvent  CardKind = iota // a single event
	CardGap                    // free time between events
	CardStack                  // one or more in-progress events sharing a slot
	CardDaySep                 // a rule marking the crossing into a new day
)

// CardState drives the card's visual treatment.
type CardState int

const (
	StateFuture CardState = iota
	StatePast
	StateCurrent
	StateSoon
)

// Card is one entry in the agenda row.
type Card struct {
	Kind  CardKind
	State CardState

	Event calendar.Event // zero for non-event kinds

	// XPx and WidthPx are position and width within the rail, before the row
	// is shifted by CardRow.OffsetPx.
	XPx     float64
	WidthPx float64

	Ghost bool   // a tentative or unaccepted invite
	Badge string // "29M LEFT" on a current card, "IN 5M" when imminent

	GapText string // duration on a free-time chip, e.g. "20m"; empty when Overnight
	// Overnight marks a gap that runs to the next day. Such a span is labelled
	// rather than measured: "19h59m free" is arithmetic nobody reads, and the
	// useful fact is simply that the day is over.
	Overnight bool
	SepText   string // "Tomorrow", "Monday, Sep 1" on a day separator

	// Stacked holds the in-progress events when Kind is CardStack. They share
	// the slot's width and split its height, so one NOW bar bisects all of them
	// at the same elapsed boundary.
	Stacked []Card

	// ElapsedPct is how much of a CardStack's span has passed, as a percentage
	// width for the shade overlay.
	ElapsedPct float64

	startText string // start time in the configured clock format
}

// CardRow is the laid-out agenda.
type CardRow struct {
	Cards []Card

	// OffsetPx is how far the row is scrolled left, as a negative number. The
	// template applies it as a transform, reproducing the reference's
	// scrollLeft in a static render.
	OffsetPx float64

	// NowBarXPx is where the NOW bar is drawn, in viewport coordinates. It is
	// not a constant: the bar sweeps across whichever entry contains now, and
	// the row shifts a whole entry at a time. See BuildAgenda.
	NowBarXPx float64
}

// BuildAgenda lays out timed events as cards, scrolls the row so the entry
// containing the present moment is in view, and places the NOW bar within it.
//
// The bar is not pinned to a fixed fraction of the viewport. It sweeps
// left-to-right across whichever entry contains now -- the in-progress meeting
// or the free-time chip -- tracking elapsed time, and when that entry ends the
// row shifts by a whole entry and the bar resets to the left edge of the next
// one. So the bar's motion means something: its position within a card is how
// much of that card has passed.
//
// Exactly one entry before the anchor stays on screen for context. When the
// anchor is the day's first entry there is nothing to show, and it sits flush
// at the left edge rather than being pushed right by blank rail -- an earlier
// layout reserved a fixed 462px lead pad for that case, which rendered as dead
// space across nearly a third of the panel on a quiet morning.
//
// events must be sorted by start time and must exclude all-day entries (those
// render in the ribbon above the row). clock24 selects the card time format.
func BuildAgenda(events []calendar.Event, now time.Time, clock24 bool) CardRow {
	var row CardRow
	x := 0.0

	// The anchor is the entry containing now. anchorIdx indexes row.Cards;
	// anchorSweepPx is how far into that entry the bar sits. Both are resolved
	// while walking the cards, then turned into the row offset and bar
	// position once the whole row is laid out.
	anchorIdx := -1
	anchorSweepPx := 0.0

	// Simultaneous in-progress events collapse into one stacked slot so the
	// single NOW bar bisects them all at the same point.
	current := currentEvents(events, now)
	stackPlaced := false

	// Seed the walk from the present rather than from the first event, so the
	// span between now and whatever is next is an entry like any other.
	//
	// Gating on i > 0 meant the row opened flush against the first card
	// whenever today's events had all passed: the parser drops events that
	// ended over an hour ago, so tomorrow morning's 08:30 became index 0 and
	// the sixteen hours in front of it produced neither a gap chip nor a day
	// separator. At 16:25 the panel showed "Exercise class 08:30" hard against
	// the NOW bar, which reads as starting imminently.
	//
	// lastDay is seeded the same way, so the crossing into tomorrow is marked
	// even when nothing today survived.
	// prevEnd walks the boundary between entries. It starts at the first
	// event's end when that event is already finished -- the parser keeps the
	// last one however long ago it ended -- and at now otherwise, so a day with
	// no history still gets a chip for the span in front of it.
	prevEnd := now
	if len(events) > 0 && !events[0].End.After(now) {
		prevEnd = events[0].End
	}
	// Seeded from now, not prevEnd: the separator marks the crossing out of
	// today, and it must be emitted before the overnight chip rather than
	// after it.
	lastDay := now.Format("2006-01-02")
	for _, e := range events {
		isCurrent := !e.Start.After(now) && e.End.After(now)

		// Free-time chip between entries separated by more than GapMinutes.
		if !isCurrent {
			gap := e.Start.Sub(prevEnd)
			split := false
			if gap >= GapMinutes*time.Minute {
				// A gap straddling the present is two entries, not one: the
				// part already spent and the part still to come. Rendering it
				// as a single chip put the whole span on one side of the NOW
				// bar, so an event that finished hours ago appeared to have
				// just ended.
				if !now.Before(prevEnd) && now.Before(e.Start) {
					// The elapsed half only when there is something elapsed to
					// show. With no history prevEnd is now, and emitting it
					// anyway produced a "1m free" chip for a span of zero.
					//
					// Deliberately unlabelled. How long ago the last event
					// finished is true but not useful -- nobody acts on "3h30m
					// free" for time already spent, and printing it invites
					// reading the number as something upcoming. The striped box
					// alone says "nothing here", which is the whole message.
					if now.Sub(prevEnd) >= GapMinutes*time.Minute {
						row.Cards = append(row.Cards, Card{
							Kind: CardGap, XPx: x, WidthPx: GapChipWidthPx,
						})
						x += GapChipWidthPx + CardGapPx
					}

					// The bar sits between the two halves.
					anchorIdx = len(row.Cards)
					anchorSweepPx = 0

					ahead := Card{Kind: CardGap, XPx: x, WidthPx: GapChipWidthPx}
					if !sameDay(now, e.Start) {
						ahead.Overnight = true
					} else {
						ahead.GapText = shortDuration(e.Start.Sub(now))
					}
					row.Cards = append(row.Cards, ahead)
					x += GapChipWidthPx + CardGapPx
					split = true
				}

				if !split {
					chip := Card{Kind: CardGap, XPx: x, WidthPx: GapChipWidthPx}
					// A gap that crosses into another day reads as "overnight"
					// rather than as a duration: the span is long enough that
					// its exact length is not information anyone acts on.
					if !sameDay(prevEnd, e.Start) {
						chip.Overnight = true
					} else {
						chip.GapText = shortDuration(gap)
					}
					// The bar sweeps a chip it sits inside, in proportion to
					// how much of the span has elapsed. Only reachable for a
					// gap that does not straddle now -- the straddling case is
					// split above and anchors between the two halves.
					if !now.Before(prevEnd) && now.Before(e.Start) {
						anchorIdx = len(row.Cards) - 1
						anchorSweepPx = (now.Sub(prevEnd).Seconds() / gap.Seconds()) * GapChipWidthPx
					}
					x += GapChipWidthPx + CardGapPx
				}
			}
		}

		// Day separator when this event crosses into a new day. Emitted after
		// the free time leading up to it: the elapsed half of a straddling gap
		// belongs to today, so a label placed before it would put "Tomorrow"
		// ahead of hours that are still today's.
		day := e.Start.Format("2006-01-02")
		if day != lastDay {
			row.Cards = append(row.Cards, Card{
				Kind: CardDaySep, XPx: x, WidthPx: DaySepWidthPx,
				SepText: dayHeading(e.Start, now),
			})
			x += DaySepWidthPx + CardGapPx
		}
		lastDay = day
		prevEnd = e.End

		if isCurrent {
			// Emit the whole stack once, in place of the first current event.
			if stackPlaced {
				continue
			}
			stackPlaced = true
			slot := buildStack(current, now, clock24)
			slot.XPx = x
			anchorIdx = len(row.Cards)
			anchorSweepPx = slot.ElapsedPct / 100 * slot.WidthPx
			row.Cards = append(row.Cards, slot)
			x += slot.WidthPx + CardGapPx
			continue
		}

		c := Card{Kind: CardEvent, Event: e, XPx: x, WidthPx: CardWidthPx}
		c.Ghost = isGhost(e)
		c.startText = formatClock(e.Start, clock24)
		switch {
		case !e.End.After(now):
			c.State = StatePast
		case e.Start.Sub(now) <= ImminentMinutes*time.Minute:
			c.State = StateSoon
			c.Badge = fmt.Sprintf("IN %s", shortDuration(e.Start.Sub(now)))
		default:
			c.State = StateFuture
		}

		// The first upcoming card anchors "now" when it is not inside an event
		// or a gap chip (e.g. a day that has not started yet). There is nothing
		// to sweep, so the bar parks at that card's left edge.
		if anchorIdx < 0 && e.Start.After(now) {
			anchorIdx = len(row.Cards)
			anchorSweepPx = 0
		}

		row.Cards = append(row.Cards, c)
		x += c.WidthPx + CardGapPx
	}

	// x is now the rail width. Nothing ahead means the day is over: there is no
	// entry to sweep, so show its tail with the bar sitting past the last card.
	if anchorIdx < 0 {
		row.OffsetPx = min(0, AgendaViewportPx-x)
		row.NowBarXPx = clampBarX(x - CardGapPx + row.OffsetPx + CardPadPx)
		return row
	}

	// Scroll so exactly one entry precedes the anchor, or so the anchor is
	// flush left when it is the day's first. Quantizing to an entry boundary
	// rather than tracking now continuously is what makes the row hold still
	// while the bar sweeps, then shift once when the entry ends.
	// Walk back to the last event card rather than a fixed one entry: the
	// entries before the anchor may be chips rather than cards, and keeping
	// only the nearest one scrolls the event itself off the left edge -- which
	// is the context the row is reaching back for. Stops at the first card
	// found, so at most one past event is shown.
	leftmost := row.Cards[anchorIdx]
	for i := anchorIdx - 1; i >= 0; i-- {
		leftmost = row.Cards[i]
		if row.Cards[i].Kind == CardEvent || row.Cards[i].Kind == CardStack {
			break
		}
	}
	// Never shift right: that would open a blank strip at the left edge.
	// The + 0 normalizes negative zero, which -0.0 formats as "-0.0px" in the
	// template -- valid CSS, but it reads as a bug in the golden.
	row.OffsetPx = min(0, -leftmost.XPx) + 0

	// The bar is positioned against #timeline-zone, which has no padding, while
	// the cards sit inside #agenda-row's CardPadPx. Without that term every card
	// renders CardPadPx right of where this arithmetic assumes and the bar
	// misses the entry it is meant to be sweeping.
	anchor := row.Cards[anchorIdx]
	row.NowBarXPx = clampBarX(anchor.XPx + row.OffsetPx + CardPadPx + anchorSweepPx)
	return row
}

// clampBarX keeps the NOW bar inside the agenda viewport. The current slot is
// duration-scaled, so a long meeting is wider than the panel and an unclamped
// sweep would carry the bar off the right edge partway through it.
func clampBarX(x float64) float64 {
	return min(max(x, 0), AgendaViewportPx-NowBarMarginPx)
}

// currentEvents returns every event in progress at now, in input order.
func currentEvents(events []calendar.Event, now time.Time) []calendar.Event {
	var out []calendar.Event
	for _, e := range events {
		if !e.Start.After(now) && e.End.After(now) {
			out = append(out, e)
		}
	}
	return out
}

// buildStack packs the in-progress events into one duration-scaled slot.
//
// The slot spans from the earliest current start to the latest current end, so
// overlapping meetings share one elapsed boundary rather than each carrying
// their own NOW bar.
func buildStack(evs []calendar.Event, now time.Time, clock24 bool) Card {
	slot := Card{Kind: CardStack, State: StateCurrent}
	if len(evs) == 0 {
		return slot
	}
	start, end := evs[0].Start, evs[0].End
	for _, e := range evs[1:] {
		if e.Start.Before(start) {
			start = e.Start
		}
		if e.End.After(end) {
			end = e.End
		}
	}
	span := end.Sub(start)
	slot.WidthPx = max(span.Minutes()*AgendaPxPerMin, AgendaMinCurW)
	if span > 0 {
		slot.ElapsedPct = min(100, max(0, now.Sub(start).Seconds()/span.Seconds()*100))
	}
	for _, e := range evs {
		slot.Stacked = append(slot.Stacked, Card{
			Kind:      CardEvent,
			State:     StateCurrent,
			Event:     e,
			Ghost:     isGhost(e),
			Badge:     fmt.Sprintf("%s LEFT", shortDuration(e.End.Sub(now))),
			startText: formatClock(e.Start, clock24),
		})
	}
	return slot
}

func isGhost(e calendar.Event) bool {
	return e.Status == "tentative" || e.Status == "needs-action"
}

// dayHeading labels a day separator: "Today", "Tomorrow", or "Monday, Sep 1".
func dayHeading(d, now time.Time) string {
	if sameDay(d, now) {
		return "Today"
	}
	if sameDay(d, now.AddDate(0, 0, 1)) {
		return "Tomorrow"
	}
	return d.Format("Monday, Jan 2")
}

// shortDuration renders a duration the way the panel shows it: "45m", "2h",
// "1h30m". Durations under a minute read as "1m" rather than "0m", since a
// badge saying "0M LEFT" looks broken.
func shortDuration(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		m = 1
	}
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	h, rem := m/60, m%60
	if rem == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, rem)
}

// IsGap reports whether this entry is a free-time chip.
func (c Card) IsGap() bool { return c.Kind == CardGap }

// IsStack reports whether this entry is the current-slot stack.
func (c Card) IsStack() bool { return c.Kind == CardStack }

// IsDaySep reports whether this entry is a day separator.
func (c Card) IsDaySep() bool { return c.Kind == CardDaySep }

// StateClass is the CSS modifier for the card's state, with a leading space so
// the template can concatenate it directly.
func (c Card) StateClass() string {
	switch c.State {
	case StatePast:
		return " past"
	case StateCurrent:
		return " current"
	case StateSoon:
		return " soon"
	default:
		return ""
	}
}

// BadgeClass selects the badge treatment: filled for an in-progress card,
// outlined for an imminent one.
func (c Card) BadgeClass() string {
	if c.State == StateCurrent {
		return "live"
	}
	return "soon"
}

// StartText is the card's start time in the panel's clock format.
func (c Card) StartText() string { return c.startText }

// DurationText is the card's length, e.g. "30M".
func (c Card) DurationText() string {
	return strings.ToUpper(shortDuration(c.Event.End.Sub(c.Event.Start)))
}
