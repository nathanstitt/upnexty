package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// A Dialog is the panel's one modal surface: a full-height sheet anchored to
// the agenda's left edge, raised by a tap and dismissed by its close control.
//
// It is deliberately content-agnostic. The shell knows about an eyebrow, a
// title, a list of labelled rows, and a set of actions -- nothing about
// events. Forecast detail, a calendar-source summary, or a diagnostics sheet
// are all the same shape, so adding one means writing a constructor like
// EventDialog below rather than touching the shell or its stylesheet.
//
// The panel shows at most one dialog. There is no stacking and no history:
// with a single touch point and no keyboard, a second layer would be a way to
// get stuck rather than a feature.
type Dialog struct {
	// Eyebrow is the small uppercase kicker above the title, naming what kind
	// of thing this is ("EVENT", "FORECAST").
	Eyebrow string
	// Title is the headline. Long titles wrap to two lines, then clip.
	Title string
	// AccentColor tints the dialog's left rail and eyebrow. Carrying the
	// tapped card's own colour into the sheet is what makes the dialog read as
	// that card expanding rather than as a generic overlay. Empty falls back
	// to the panel's blue.
	AccentColor string

	// Rows are the labelled detail lines, in display order.
	Rows []DialogRow
	// Body is free prose shown under the rows (an event description). Clipped
	// to the space available rather than scrolled -- the panel has no scroll.
	Body string

	// Actions are the buttons along the sheet's bottom edge.
	Actions []DialogAction

	// DismissAction is the close control at the top right.
	DismissAction DialogAction
}

// DialogRow is one labelled line of detail.
type DialogRow struct {
	Label string
	Value string
	// Mono renders the value in the data face. Use it for times, durations,
	// and anything the eye scans in a column; leave it off for prose.
	Mono bool
}

// DialogAction is a tappable control. ID is what the touch layer dispatches
// on; the model never performs the action itself.
type DialogAction struct {
	ID    string
	Label string
	// Style selects the visual treatment: "" for the default quiet button,
	// "primary" for the one action the sheet exists to offer, "close" for the
	// dismiss affordance.
	Style string
	// HitRect is where this control lands on the panel, filled in by the view
	// layer's geometry so the touch layer can hit-test it without parsing HTML.
	HitRect Rect
}

// Rect is a hit-testable rectangle in panel coordinates (origin top-left,
// landscape 1920x480).
type Rect struct {
	X, Y, W, H float64
}

// Contains reports whether a point falls inside the rectangle.
func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Action IDs dispatched by the touch layer.
const (
	ActionClose = "close"
	ActionMute  = "mute"
)

// EventDialog builds the detail sheet for a tapped event.
//
// It shows what a card cannot: the full untruncated title, the calendar the
// event came from, its location, its RSVP status, and its description. A card
// is 196px wide and clamps its title to two lines, so "what IS this meeting"
// is exactly the question a tap is asking.
//
// Empty fields are omitted rather than rendered blank -- a sheet with three
// populated rows should look deliberate, not like a form with gaps.
func EventDialog(e calendar.Event, clock24 bool, muted bool) Dialog {
	d := Dialog{
		Eyebrow:     "Event",
		Title:       e.Title,
		AccentColor: e.Color,
		DismissAction: DialogAction{
			ID: ActionClose, Label: "Close", Style: "close",
		},
	}

	d.Rows = append(d.Rows, DialogRow{
		Label: "When", Value: eventWhen(e, clock24), Mono: true,
	})
	if !e.AllDay {
		d.Rows = append(d.Rows, DialogRow{
			Label: "Duration", Value: humanDuration(e.End.Sub(e.Start)), Mono: true,
		})
	}
	if e.Location != "" {
		d.Rows = append(d.Rows, DialogRow{Label: "Where", Value: e.Location})
	}
	if e.Calendar != "" {
		d.Rows = append(d.Rows, DialogRow{Label: "Calendar", Value: e.Calendar})
	}
	if s := rsvpLabel(e.Status); s != "" {
		d.Rows = append(d.Rows, DialogRow{Label: "RSVP", Value: s})
	}
	d.Body = e.Description

	// The label states what the tap will do, not what the current state is: a
	// control named for its outcome is the one people read correctly. A muted
	// event is only reachable here from the settings page, but the sheet still
	// has to offer the way back.
	mute := DialogAction{ID: ActionMute, Label: "Hide from panel", Style: "primary"}
	if muted {
		mute.Label = "Show on panel"
	}
	d.Actions = append(d.Actions, mute)
	return d
}

// eventWhen renders the time range the way the panel states times elsewhere:
// an all-day event has no clock, and an event ending on its start day shows
// the day once.
func eventWhen(e calendar.Event, clock24 bool) string {
	day := e.Start.Format("Mon, Jan 2")
	if e.AllDay {
		return day + " · All day"
	}
	start := formatClock(e.Start, clock24)
	end := formatClock(e.End, clock24)
	if e.End.YearDay() != e.Start.YearDay() || e.End.Year() != e.Start.Year() {
		return fmt.Sprintf("%s %s → %s %s", day, start, e.End.Format("Mon, Jan 2"), end)
	}
	return fmt.Sprintf("%s · %s – %s", day, start, end)
}

// humanDuration renders a span the way a person says it: "45m", "1h", "1h 30m".
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	total := int(d.Round(time.Minute) / time.Minute)
	h, m := total/60, total%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh %dm", h, m)
	}
}

// rsvpLabel turns an iCal PARTSTAT into words. An empty status means the owner
// is not an attendee (their own event, or a personal calendar), which is not a
// state worth a row -- it returns "" and the row is dropped.
func rsvpLabel(status string) string {
	switch strings.ToLower(status) {
	case "accepted":
		return "Going"
	case "declined":
		return "Not going"
	case "tentative":
		return "Maybe"
	case "needs-action":
		return "No reply yet"
	}
	return ""
}
