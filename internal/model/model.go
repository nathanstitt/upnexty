package model

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nathanstitt/upnexty/internal/calendar"
	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/weather"
)

// Layout constants for the 1920x480 panel. The left "now block" is fixed width;
// the timeline occupies the rest.
const (
	PanelWidth  = 1920
	PanelHeight = 480
	NowBlockW   = 380
	TrackWidth  = PanelWidth - NowBlockW // 1540
)

// defaultWindow is the weather chart's time axis. The agenda has no time scale
// of its own -- cards are fixed-width and time-ordered (see agenda.go).
var defaultWindow = WindowOpts{
	FitEvents:   4,
	PastContext: 20 * time.Minute,
	MinSpan:     4 * time.Hour,
	MaxSpan:     12 * time.Hour,
}

// SetupHint is shown on the panel while the board is not associated with a
// network -- which covers a fresh board, but also wrong credentials, a router
// that went away, and a move out of range. It carries the setup AP's name and
// the admin password so first-run needs no documentation. Anyone who can see
// the panel learns the password; that trade is accepted for a home display.
type SetupHint struct {
	APName   string
	Password string

	// URL is the portal address to type in, shown because the captive-portal
	// sheet cannot be relied on: a device may probe an endpoint we do not
	// answer, have the check disabled, or simply be a laptop that never shows
	// one. The redirect handles the common case and this covers the rest.
	URL string

	// Connecting is the network an in-flight association attempt is joining,
	// or "" when none is running. While it is set the panel reports progress
	// instead of the join instructions: association tears down the AP the
	// user's browser is on, so the panel is the only surface that can still
	// tell them what is happening.
	Connecting string

	// PairCode and PairURL are the Google device-flow code to type and where
	// to type it, or "" when no flow is running.
	//
	// Same reasoning as Connecting, for a different reason: the flow runs for
	// as long as it takes someone to pick up their phone and sign in, so the
	// browser that started it was answered minutes ago. The panel is where the
	// code has to appear, and it is the one surface guaranteed to be in front
	// of the person who just pressed the button.
	PairCode string
	PairURL  string
}

// Pairing reports whether a Google device-flow code is waiting to be entered.
func (s *SetupHint) Pairing() bool { return s != nil && s.PairCode != "" }

// PairHost is PairURL without its scheme, for display on the panel. Keeping
// the full URL in the model and trimming for presentation means the value the
// code came with is never rewritten -- only how it is shown.
func (s *SetupHint) PairHost() string {
	if s == nil {
		return ""
	}
	u := strings.TrimPrefix(s.PairURL, "https://")
	return strings.TrimPrefix(u, "http://")
}

// ViewModel is everything the template needs. No method on it may consult the
// clock; every time-dependent value is resolved in Build.
type ViewModel struct {
	Now       time.Time
	ClockTime string
	ClockDate string

	Current  *weather.Conditions
	Forecast []weather.DayPoint
	Hourly   []weather.HourPoint

	NextEvent *calendar.Event
	UntilNext string

	// Agenda is the card row and its pre-scroll offset.
	Agenda CardRow
	// NowBlock is the left panel's resolved headline.
	NowBlock NowBlock

	// Window is the weather chart's time axis. The agenda no longer uses it:
	// cards are fixed-width and time-ordered, not positioned by time.
	Window Window
	AllDay []calendar.Event

	// Stale reports that this refresh cycle had at least one failure, so some
	// of what is displayed may be last-good data rather than current. A
	// calendar timeout with working weather still sets it — render the
	// indicator on Stale, not on whether a particular section is empty.
	Stale  bool
	Errors []string

	// Loading reports that no fetch has completed yet, so an empty agenda means
	// "not known" rather than "nothing scheduled". Without it the first frames
	// after boot claim "No more events today" with authority, which is a
	// statement about the calendar the panel has not yet earned the right to
	// make. Callers set it (see cmd/dashboard); Build cannot infer it, because
	// no-events-yet and genuinely-no-events look identical from here.
	Loading bool

	// Setup is non-nil only while the board is not associated with a network,
	// and carries the setup AP name and admin password so first-run needs no
	// documentation. Callers must compute it themselves (see cmd/dashboard's
	// setupHintTracker) -- Build must not reach into wifi/portal to derive it,
	// so the template stays independent of whether those packages are reachable.
	Setup *SetupHint

	// Dialog is the modal sheet, non-nil only while one is open. It is set by
	// the touch layer rather than derived from data, which is why Build leaves
	// it alone: a dialog is a statement about what the user is looking at, not
	// about what the calendar contains.
	Dialog *Dialog
}

// Build assembles the view model. It tolerates nil weather and no events so a
// boot with no network still renders a clock. setup is nil once the board is
// configured; see SetupHint.
func Build(now time.Time, c *config.Config, evs []calendar.Event, w *weather.Weather, errs []string, setup *SetupHint) ViewModel {
	loc := c.TimeLocation()
	local := now.In(loc)

	// 12-hour carries AM/PM: the panel is read at a glance from across a room,
	// where a bare "4:33" is ambiguous.
	clockLayout := "3:04 PM"
	if c.Units.Clock24h {
		clockLayout = "15:04"
	}

	vm := ViewModel{
		Now:       now,
		ClockTime: local.Format(clockLayout),
		ClockDate: local.Format("Mon, Jan 2"),
		Errors:    errs,
		// Stale means "something failed this fetch cycle, so what's on
		// screen may be older than it looks" — derived from errs, not from
		// whether both sources happened to fail together. A calendar
		// timeout with weather still succeeding is exactly the case this
		// must catch: the agenda shown is stale even though Current is
		// fresh.
		Stale: len(errs) > 0,
		Setup: setup,
	}

	if w != nil {
		cur := w.Current
		vm.Current = &cur
		vm.Forecast = w.Daily
		vm.Hourly = w.Hourly
	}

	// Build may receive events concatenated from multiple calendar feeds.
	// calendar.Parse sorts within a single feed, but concatenating two
	// sorted slices does not yield a sorted slice, so re-sort here.
	sorted := append([]calendar.Event(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })

	// Every event time is converted to the panel's timezone here, once, so no
	// downstream formatter has to remember to do it. A feed states times in
	// whatever zone it likes -- a DTSTART ending in Z parses as UTC -- and
	// time.Format renders in whatever zone the value carries, so formatting an
	// unconverted value printed UTC on the panel while the clock beside it read
	// local. Comparisons (Start.After(now)) are instant-based and were correct
	// throughout, which is why the layout was right and only the labels were
	// wrong: an off-by-a-timezone that shows up in exactly one of the two.
	//
	// This is safe for muting: Event.Key formats its start in UTC explicitly, so
	// a converted event yields the same key and existing mutes still match.
	//
	// All-day events are deliberately left alone. Their times are midnight
	// boundaries parsed in the configured zone already, and shifting them would
	// move an all-day entry onto the wrong date.
	for i := range sorted {
		if sorted[i].AllDay {
			continue
		}
		sorted[i].Start = sorted[i].Start.In(loc)
		sorted[i].End = sorted[i].End.In(loc)
	}

	// Muted events are dropped here, before the timed/all-day split, so a mute
	// removes the event from every surface at once: the card row, the all-day
	// ribbon, and the NOW/NEXT headline. Filtering later would hide the card
	// while the left panel still counted down to it.
	if c != nil && len(c.Muted) > 0 {
		kept := sorted[:0]
		for _, e := range sorted {
			if !c.IsMuted(e.Key()) {
				kept = append(kept, e)
			}
		}
		sorted = kept
	}

	var timed []calendar.Event
	for _, e := range sorted {
		if e.AllDay {
			vm.AllDay = append(vm.AllDay, e)
		} else {
			timed = append(timed, e)
		}
	}

	// Next up: an in-progress event wins over the next one to start.
	for i := range timed {
		if timed[i].End.After(now) {
			vm.NextEvent = &timed[i]
			vm.UntilNext = untilText(now, timed[i].Start)
			break
		}
	}

	clock24 := c != nil && c.Units.Clock24h
	// local, not now: these derive calendar dates ("Today"/"Tomorrow" separators,
	// the same-day guard on FREE FOR), and a date is a question about a wall
	// clock. Passing the UTC value rolled the day over at 7pm local here.
	vm.Agenda = BuildAgenda(timed, local, clock24)
	vm.NowBlock = BuildNowBlock(timed, local, clock24)

	// The weather chart keeps its own window: it spans hours, while the agenda
	// is a card row with no time scale at all.
	vm.Window = ComputeWindow(timed, now, TrackWidth, defaultWindow)
	return vm
}

// untilText renders the wait until an event starts; an already-started event
// reads as "now".
func untilText(now, start time.Time) string {
	d := start.Sub(now)
	// Sub-minute durations truncate to 0 under int(d.Minutes()), which would
	// otherwise print "0m" for the whole final minute before an event starts.
	// Treat anything under a minute as already-here.
	if d < time.Minute {
		return "now"
	}
	mins := int(d.Minutes())
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
