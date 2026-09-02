package model

import (
	"strconv"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// NowBlockMode selects which headline the left panel leads with.
type NowBlockMode int

const (
	// ModeInEvent: currently in a timed event. Leads with NOW + the title.
	ModeInEvent NowBlockMode = iota
	// ModeFree: between events with something later today. Leads with FREE FOR.
	ModeFree
	// ModeImminent: the next event starts within ImminentMinutes. Counts down.
	ModeImminent
	// ModeDone: nothing left today.
	ModeDone
)

// NowBlock is the left panel's headline content, resolved from the clock so the
// template does no time arithmetic.
type NowBlock struct {
	Mode NowBlockMode

	// Lead is the small uppercase label: "NOW", "FREE FOR", "NEXT", "".
	Lead string
	// Big is the headline: a duration for ModeFree/ModeImminent, otherwise the
	// current or next event's title.
	Big string
	// BigIsDuration renders Big as the large duration numeral rather than an
	// event title.
	BigIsDuration bool
	// BigUrgent colours that numeral amber instead of green: time until
	// something starts, not time free. Both are durations, so the colour is
	// what separates "you have 22 minutes" from "it starts in 4".
	BigUrgent bool

	// Sub is the supporting line under the headline, e.g. "ends in 25 min".
	Sub string
	// SubUrgent renders Sub in amber: an imminent countdown, not a calm one.
	SubUrgent bool

	// Accent is the current/next event's calendar colour, drawn as a bar to
	// the left of the title. Empty when the headline is a duration.
	Accent string

	// The "NEXT ... at 11:27 AM" block, when there is something to preview.
	NextLead  string // "NEXT" or "THEN"
	NextTitle string
	NextAt    string

	// Quote and QuoteAuthor are shown with the end-of-day mark (ModeDone
	// only). Empty when none has been fetched, in which case the block simply
	// omits them -- the panel never waits on the network to render.
	Quote       string
	QuoteAuthor string
}

// IsDone reports whether the day's events are finished, which the template
// uses to swap the headline for the end-of-day mark and its quote.
func (n NowBlock) IsDone() bool { return n.Mode == ModeDone }

// BuildNowBlock resolves the left panel's headline.
//
// events must be timed (no all-day), sorted by start. clock24 selects the time
// format for the "at ..." preview line.
//
// Free time is only claimed when the next event is later the SAME day: after the
// last meeting, "free for 14h" would be true but useless, so that case falls
// through to ModeDone.
func BuildNowBlock(events []calendar.Event, now time.Time, clock24 bool) NowBlock {
	current, next, afterNext := findCurrentAndNext(events, now)

	switch {
	case current != nil:
		nb := NowBlock{
			Mode:   ModeInEvent,
			Lead:   "NOW",
			Big:    current.Title,
			Accent: current.Color,
			Sub:    "ends in " + shortDuration(current.End.Sub(now)),
		}
		if next != nil {
			nb.NextLead = "NEXT"
			nb.NextTitle = next.Title
			nb.NextAt = "at " + formatClock(next.Start, clock24)
		}
		return nb

	case next != nil && sameDay(next.Start, now):
		until := next.Start.Sub(now)
		if until >= ImminentMinutes*time.Minute {
			nb := NowBlock{
				Mode:          ModeFree,
				Lead:          "FREE FOR",
				Big:           longDuration(until),
				BigIsDuration: true,
				NextLead:      "NEXT",
				NextTitle:     next.Title,
				NextAt:        "at " + formatClock(next.Start, clock24),
			}
			return nb
		}
		// Imminent: lead with the countdown, name the event beneath it.
		//
		// The countdown is the headline rather than the title because the
		// panel is answering "can I start something right now" -- "in 4 min"
		// is the answer, and the meeting's name is the detail. It is also the
		// same shape as ModeFree directly above ("FREE FOR" / "22 min"), so
		// the two states the user is most often in read as one pattern rather
		// than swapping which line is large.
		nb := NowBlock{
			Mode:          ModeImminent,
			Lead:          "STARTS IN",
			Big:           longDuration(until),
			BigIsDuration: true,
			BigUrgent:     true,
			Accent:        next.Color,
			Sub:           next.Title,
		}
		if afterNext != nil {
			nb.NextLead = "THEN"
			nb.NextTitle = afterNext.Title
			nb.NextAt = "at " + formatClock(afterNext.Start, clock24)
		}
		return nb

	default:
		nb := NowBlock{Mode: ModeDone, Lead: "", Big: "Done for the day"}
		// Preview tomorrow when there is something on it.
		for i := range events {
			if events[i].Start.After(now) {
				nb.NextLead = "TOMORROW"
				nb.NextTitle = events[i].Title
				nb.NextAt = "at " + formatClock(events[i].Start, clock24)
				break
			}
		}
		return nb
	}
}

// findCurrentAndNext returns the in-progress event (if any) and the next two
// upcoming ones. events must be sorted by start.
func findCurrentAndNext(events []calendar.Event, now time.Time) (current, next, afterNext *calendar.Event) {
	for i := range events {
		e := &events[i]
		if e.AllDay {
			continue
		}
		switch {
		case !e.Start.After(now) && e.End.After(now):
			if current == nil {
				current = e
			}
		case e.Start.After(now):
			if next == nil {
				next = e
			} else if afterNext == nil {
				afterNext = e
			}
		}
	}
	return current, next, afterNext
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// longDuration renders the FREE FOR headline: "34 min", "2 hr", "1 hr 20 min".
// Spelled out rather than "34m" because it is the largest text on the panel and
// reads from across a room.
func longDuration(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		m = 1
	}
	if m < 60 {
		return strconv.Itoa(m) + " min"
	}
	h, rem := m/60, m%60
	if rem == 0 {
		return strconv.Itoa(h) + " hr"
	}
	return strconv.Itoa(h) + " hr " + strconv.Itoa(rem) + " min"
}

// formatClock renders a wall time in the panel's configured format.
func formatClock(t time.Time, clock24 bool) string {
	if clock24 {
		return t.Format("15:04")
	}
	return t.Format("3:04 PM")
}

// formatRange renders a start and end as one span, e.g. "10:30 – 11:00 AM".
//
// The meridiem is dropped from the start when both ends share it, which is the
// common case and what keeps the line short enough for a stacked row. In 24h
// format there is no meridiem to elide.
func formatRange(start, end time.Time, clock24 bool) string {
	if clock24 {
		return start.Format("15:04") + " – " + end.Format("15:04")
	}
	s := start.Format("3:04 PM")
	if start.Format("PM") == end.Format("PM") {
		s = start.Format("3:04")
	}
	return s + " – " + end.Format("3:04 PM")
}
