package model

import (
	"fmt"
	"sort"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// Layout constants for the 1920x480 panel. The left "now block" is fixed width;
// the timeline occupies the rest.
const (
	PanelWidth  = 1920
	PanelHeight = 480
	NowBlockW   = 380
	TrackWidth  = PanelWidth - NowBlockW // 1540
)

// Timeline tuning. Starting values carried over from the Pi dashboard
// (GAP_MIN/IMMINENT_MIN); adjust against real renders.
var (
	defaultWindow = WindowOpts{
		FitEvents:   4,
		PastContext: 20 * time.Minute,
		MinSpan:     4 * time.Hour,
		MaxSpan:     12 * time.Hour,
	}
	defaultLabels = LabelOpts{
		MaxRows: 2, Gap: 10, MaxDrift: 80, TrackWidth: TrackWidth,
		// A title running modestly past the right edge is still readable and
		// better than dropping it, but a runaway is not: cap overflow at 120px.
		MaxOverflow: 120,
	}
	minBlockPx = 4.0 // visibility floor, not a layout min-width
)

// ViewModel is everything the template needs. No method on it may consult the
// clock; every time-dependent value is resolved in Build.
type ViewModel struct {
	Now       time.Time
	ClockTime string
	ClockDate string

	LocationName string
	Current      *weather.Conditions
	Forecast     []weather.DayPoint

	NextEvent *calendar.Event
	UntilNext string

	Window Window
	Blocks []Block
	// Labels is NOT index-aligned with Blocks: PlaceLabels may omit labels
	// that don't fit within TrackWidth+MaxOverflow on any row, so
	// len(Labels) can be less than len(Blocks). Correlate via Label.Anchor
	// (== the block's X), not by index.
	Labels []Label
	AllDay []calendar.Event
	NowX   float64

	Stale  bool
	Errors []string
}

// Build assembles the view model. It tolerates nil weather and no events so a
// boot with no network still renders a clock.
func Build(now time.Time, c *config.Config, evs []calendar.Event, w *weather.Weather, errs []string) ViewModel {
	loc := c.TimeLocation()
	local := now.In(loc)

	clockLayout := "3:04"
	if c.Units.Clock24h {
		clockLayout = "15:04"
	}

	vm := ViewModel{
		Now:          now,
		ClockTime:    local.Format(clockLayout),
		ClockDate:    local.Format("Monday, January 2"),
		LocationName: c.Location.Name,
		Errors:       errs,
		Stale:        w == nil && len(evs) == 0,
	}

	if w != nil {
		cur := w.Current
		vm.Current = &cur
		vm.Forecast = w.Daily
	}

	// Build may receive events concatenated from multiple calendar feeds.
	// calendar.Parse sorts within a single feed, but concatenating two
	// sorted slices does not yield a sorted slice, so re-sort here.
	sorted := append([]calendar.Event(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })

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

	vm.Window = ComputeWindow(timed, now, TrackWidth, defaultWindow)
	vm.Blocks = Blocks(timed, vm.Window, minBlockPx)
	vm.Labels = PlaceLabels(vm.Blocks, estimateTextWidth, defaultLabels)
	vm.NowX = vm.Window.X(now)
	return vm
}

// untilText renders the wait until an event starts; an already-started event
// reads as "now".
func untilText(now, start time.Time) string {
	d := start.Sub(now)
	if d <= 0 {
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

// estimateTextWidth approximates rendered label width. Labels are 19px and the
// UI font averages ~0.52em per character; exact metrics are not needed because
// placement only has to avoid visible collisions.
func estimateTextWidth(s string) float64 {
	const avgCharPx = 19 * 0.52
	return float64(len([]rune(s))) * avgCharPx
}
