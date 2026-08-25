package model

import (
	"math"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

var base = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func ev(title string, startMin, durMin int) calendar.Event {
	s := base.Add(time.Duration(startMin) * time.Minute)
	return calendar.Event{
		Title: title,
		Start: s,
		End:   s.Add(time.Duration(durMin) * time.Minute),
	}
}

func closeTo(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestWindowXIsLinear(t *testing.T) {
	w := Window{Start: base, End: base.Add(4 * time.Hour), WidthPx: 1000}
	if got := w.X(base); !closeTo(got, 0, 0.001) {
		t.Errorf("X(start) = %v, want 0", got)
	}
	if got := w.X(base.Add(4 * time.Hour)); !closeTo(got, 1000, 0.001) {
		t.Errorf("X(end) = %v, want 1000", got)
	}
	if got := w.X(base.Add(2 * time.Hour)); !closeTo(got, 500, 0.001) {
		t.Errorf("X(midpoint) = %v, want 500", got)
	}
	// Equal durations must map to equal widths anywhere on the axis.
	d1 := w.X(base.Add(30*time.Minute)) - w.X(base)
	d2 := w.X(base.Add(3*time.Hour+30*time.Minute)) - w.X(base.Add(3*time.Hour))
	if !closeTo(d1, d2, 0.001) {
		t.Errorf("axis not linear: %v vs %v", d1, d2)
	}
}

func TestComputeWindowFitsRequestedEvents(t *testing.T) {
	evs := []calendar.Event{
		ev("a", 30, 30), ev("b", 120, 60), ev("c", 300, 30), ev("d", 600, 30),
	}
	opts := WindowOpts{
		FitEvents:   3,
		PastContext: 30 * time.Minute,
		MinSpan:     2 * time.Hour,
		MaxSpan:     12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if !w.Start.Equal(base.Add(-30 * time.Minute)) {
		t.Errorf("Start = %v, want now-30m", w.Start)
	}
	// Must cover the 3rd event's end (300+30 min).
	third := base.Add(330 * time.Minute)
	if w.End.Before(third) {
		t.Errorf("End = %v, does not cover 3rd event ending %v", w.End, third)
	}
}

func TestComputeWindowClampsToMinSpan(t *testing.T) {
	evs := []calendar.Event{ev("soon", 10, 10)}
	opts := WindowOpts{
		FitEvents: 3, PastContext: 15 * time.Minute,
		MinSpan: 3 * time.Hour, MaxSpan: 12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if got := w.End.Sub(w.Start); got < 3*time.Hour {
		t.Errorf("span = %v, want >= MinSpan 3h", got)
	}
}

func TestComputeWindowClampsToMaxSpan(t *testing.T) {
	evs := []calendar.Event{ev("far", 60*40, 30)} // ~40h out
	opts := WindowOpts{
		FitEvents: 3, PastContext: 15 * time.Minute,
		MinSpan: 2 * time.Hour, MaxSpan: 12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if got := w.End.Sub(w.Start); got > 12*time.Hour {
		t.Errorf("span = %v, want <= MaxSpan 12h", got)
	}
}

func TestComputeWindowHandlesNoEvents(t *testing.T) {
	opts := WindowOpts{
		FitEvents: 3, PastContext: 15 * time.Minute,
		MinSpan: 4 * time.Hour, MaxSpan: 12 * time.Hour,
	}
	w := ComputeWindow(nil, base, 1540, opts)
	if got := w.End.Sub(w.Start); got < 4*time.Hour {
		t.Errorf("empty-calendar span = %v, want >= MinSpan", got)
	}
	if w.WidthPx != 1540 {
		t.Errorf("WidthPx = %v, want 1540", w.WidthPx)
	}
}

func TestBlocksAreDurationProportional(t *testing.T) {
	w := Window{Start: base, End: base.Add(4 * time.Hour), WidthPx: 1000}
	// 60m and 120m events: the second must be exactly twice as wide.
	evs := []calendar.Event{ev("hour", 0, 60), ev("two", 120, 120)}
	bs := Blocks(evs, w, 4)
	if len(bs) != 2 {
		t.Fatalf("len(Blocks) = %d, want 2", len(bs))
	}
	if !closeTo(bs[1].W, bs[0].W*2, 0.001) {
		t.Errorf("widths %v and %v are not 1:2", bs[0].W, bs[1].W)
	}
	if !closeTo(bs[0].X, 0, 0.001) {
		t.Errorf("first block X = %v, want 0", bs[0].X)
	}
}

func TestBlocksApplyVisibilityFloor(t *testing.T) {
	w := Window{Start: base, End: base.Add(12 * time.Hour), WidthPx: 1000}
	// 5 minutes of a 12h window is ~0.7px — below the floor.
	bs := Blocks([]calendar.Event{ev("tiny", 60, 5)}, w, 4)
	if len(bs) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1", len(bs))
	}
	if bs[0].W < 4 {
		t.Errorf("W = %v, want >= floor 4", bs[0].W)
	}
}

func TestBlocksExcludeEventsOutsideWindow(t *testing.T) {
	w := Window{Start: base, End: base.Add(2 * time.Hour), WidthPx: 1000}
	evs := []calendar.Event{
		ev("before", -120, 30),
		ev("inside", 30, 30),
		ev("after", 300, 30),
	}
	bs := Blocks(evs, w, 4)
	if len(bs) != 1 || bs[0].Event.Title != "inside" {
		var got []string
		for _, b := range bs {
			got = append(got, b.Event.Title)
		}
		t.Fatalf("Blocks = %v, want only [inside]", got)
	}
}

func TestBlocksIncludeStraddlingEvent(t *testing.T) {
	w := Window{Start: base, End: base.Add(2 * time.Hour), WidthPx: 1000}
	// Started before the window, still running inside it.
	bs := Blocks([]calendar.Event{ev("running", -30, 90)}, w, 4)
	if len(bs) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1 (in-progress event)", len(bs))
	}
	if bs[0].X < 0 {
		t.Errorf("X = %v, want clamped to >= 0", bs[0].X)
	}
}
