package model

import "github.com/nathanstitt/luckfox-dashboard/internal/calendar"

// CardRect is where a card lands on the panel, in landscape coordinates, along
// with the event it stands for.
type CardRect struct {
	Rect  Rect
	Event calendar.Event

	// Overflow is set instead of Event on a capped stack's summary row, which
	// stands for several events rather than one. A tap there opens a sheet
	// listing them; see ConflictDialog.
	Overflow []calendar.Event
}

// CardRects returns the tappable rectangle of every event card in the row.
//
// The geometry is derived from the same values that lay the row out -- card X
// and width from BuildAgenda, the row's own offset and padding -- rather than
// re-measured from the rendered page. A tap must hit what the eye sees, and
// the only way to guarantee that without reading back pixels is to compute
// both from one source.
//
// Only CardEvent and CardStack produce rects. A free-time chip and a day
// separator have nothing to show in a dialog, so tapping them does nothing;
// that is deliberate, not an oversight.
//
// A stack's children each get their own rect, splitting the slot's height, so
// two overlapping meetings are independently tappable -- the same split the
// renderer draws. A capped stack's last child is a summary row, which carries
// Overflow instead of Event.
func (r CardRow) CardRects() []CardRect {
	top := float64(RibbonHeightPx + AgendaRowPadTop)
	height := float64(AgendaHeightPx - AgendaRowPadTop - AgendaRowPadBot)

	var out []CardRect
	add := func(rc Rect, c CardRect) {
		if rc, ok := clipToAgenda(rc); ok {
			c.Rect = rc
			out = append(out, c)
		}
	}
	for _, c := range r.Cards {
		// Cards are positioned in rail space; the row is then shifted left by
		// OffsetPx and sits after the left block.
		x := NowBlockW + c.XPx + r.OffsetPx

		switch c.Kind {
		case CardEvent:
			add(Rect{X: x, Y: top, W: c.WidthPx, H: height}, CardRect{Event: c.Event})
		case CardStack:
			n := len(c.Stacked)
			if n == 0 {
				continue
			}
			// The stack splits its height evenly, matching the render. The gaps
			// between rows are excluded rather than divided into them, so a tap
			// landing in one hits nothing -- the same choice the row makes
			// horizontally, where the 14px between cards is not snapped to a
			// neighbour.
			//
			// The row height is the inset band less the gaps, divided by the row
			// count -- the same arithmetic the .evt-stack.nN rules state. Pinned
			// against the rendered rows by TestStackRectsMatchTheRender, because
			// a stack whose rows paint somewhere other than their rects is
			// invisible: the cards look right and the taps land one row off.
			gaps := float64(n-1) * StackRowGapPx
			h := (height - gaps) / float64(n)
			for i, s := range c.Stacked {
				y := top + float64(i)*(h+StackRowGapPx)
				add(Rect{X: x, Y: y, W: c.WidthPx, H: h},
					CardRect{Event: s.Event, Overflow: s.Overflow})
			}
		}
	}
	return out
}

// clipToAgenda trims a card rect to the agenda's visible viewport, dropping it
// if nothing remains.
//
// The row is pre-scrolled by a negative offset, so a card that has passed can
// sit at a negative x -- fully or partly underneath the left panel. On screen
// #timeline-zone's overflow:hidden clips it away, but a rect does not know
// that: without this, a tap on the clock would open the detail sheet for an
// event the user cannot see. Found by TestEventAtIgnoresTheLeftPanel, not by
// inspection.
func clipToAgenda(r Rect) (Rect, bool) {
	const left = float64(NowBlockW)
	right := float64(PanelWidth)

	if r.X+r.W <= left || r.X >= right {
		return Rect{}, false
	}
	if r.X < left {
		r.W -= left - r.X
		r.X = left
	}
	if r.X+r.W > right {
		r.W = right - r.X
	}
	return r, r.W > 0
}

// EventAt returns the event whose card contains the point, if any.
//
// Cards do not overlap horizontally, so the first hit is the only hit; the
// scan stops there. A tap landing in the gap between two cards returns
// nothing rather than snapping to the nearest -- with 14px gaps and a
// fingertip, snapping would fire the wrong card often enough to erode trust in
// the whole interaction.
//
// Rects partly scrolled off the left edge are still tested against their
// visible part, because Rect.Contains is evaluated in panel coordinates: a
// card at x=-50 with width 196 is hit only between 0 and 146, which is exactly
// the part the user can see.
func (r CardRow) EventAt(x, y float64) (calendar.Event, bool) {
	for _, cr := range r.CardRects() {
		if cr.Rect.Contains(x, y) {
			// A summary row stands for several events, so there is no single one
			// to return. It is not a miss either -- the tap belongs to
			// OverflowAt, and falling through to the cards behind it would open
			// the wrong sheet.
			if len(cr.Overflow) > 0 {
				return calendar.Event{}, false
			}
			return cr.Event, true
		}
	}
	return calendar.Event{}, false
}

// OverflowAt returns the events a capped stack's summary row stands for, when
// the point lands on one.
//
// Kept separate from EventAt rather than folded into it because the two open
// different sheets: one event's detail, or a list of the several this row hides.
func (r CardRow) OverflowAt(x, y float64) ([]calendar.Event, bool) {
	for _, cr := range r.CardRects() {
		if cr.Rect.Contains(x, y) && len(cr.Overflow) > 0 {
			return cr.Overflow, true
		}
	}
	return nil, false
}
