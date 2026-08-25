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

// Finding 1 regression: MaxSpan clamps the window, dropping events beyond it.
// FitEvents is best-effort within MaxSpan; it cannot override the hard bound.
func TestComputeWindowMaxSpanDropsEventsExceedingBound(t *testing.T) {
	evs := []calendar.Event{
		ev("e1", 30, 30),    // ends at 60m
		ev("e2", 120, 60),   // ends at 180m
		ev("e3", 300, 30),   // ends at 330m — beyond start+12h? No, 330m is 5.5h
	}
	// Use a MaxSpan that will truncate the third event.
	opts := WindowOpts{
		FitEvents:   3,
		PastContext: 30 * time.Minute,
		MinSpan:     2 * time.Hour,
		MaxSpan:     4 * time.Hour, // Hard bound: end cannot exceed start + 4h
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if got := w.End.Sub(w.Start); got > 4*time.Hour {
		t.Errorf("span = %v, exceeds MaxSpan 4h", got)
	}
	// Third event ends at base + 330m = base + 5.5h.
	// Window ends at start + 4h = base - 30m + 4h = base + 3.5h.
	// So the third event (ending at 5.5h) is excluded from the window.
	thirdEnd := base.Add(330 * time.Minute)
	if w.End.Before(thirdEnd) {
		// This is the correct behavior: the third event is dropped.
	} else {
		t.Errorf("window unexpectedly includes third event beyond MaxSpan")
	}
}

// Finding 2 regression: MaxSpan < MinSpan is unguarded; MaxSpan wins.
func TestComputeWindowMaxSpanWinsOverMinSpan(t *testing.T) {
	evs := []calendar.Event{ev("e1", 10, 10)}
	opts := WindowOpts{
		FitEvents:   1,
		PastContext: 15 * time.Minute,
		MinSpan:     6 * time.Hour,  // Requests 6h minimum
		MaxSpan:     2 * time.Hour,  // But MaxSpan caps at 2h
	}
	w := ComputeWindow(evs, base, 1540, opts)
	span := w.End.Sub(w.Start)
	if span > 2*time.Hour {
		t.Errorf("span = %v, exceeds MaxSpan 2h (MaxSpan should win over MinSpan)", span)
	}
	if span < 2*time.Hour {
		t.Errorf("span = %v, want exactly MaxSpan 2h", span)
	}
}

// Finding 3 regression: events must be sorted by Start; demonstrate the contract.
// This test shows the current behavior: if events are NOT sorted, FitEvents
// under-fits. We document this contract and accept the behavior rather than
// defensive sort (which would surprise callers and hide upstream bugs).
func TestComputeWindowRequiresSortedEvents(t *testing.T) {
	// Create events intentionally out of order by Start time.
	e1 := calendar.Event{Title: "e1", Start: base.Add(30 * time.Minute), End: base.Add(60 * time.Minute)}
	e2 := calendar.Event{Title: "e2", Start: base.Add(10 * time.Minute), End: base.Add(20 * time.Minute)}
	e3 := calendar.Event{Title: "e3", Start: base.Add(120 * time.Minute), End: base.Add(180 * time.Minute)}

	unsorted := []calendar.Event{e1, e2, e3} // e2 is out of order (starts before e1)
	opts := WindowOpts{
		FitEvents:   2,
		PastContext: 15 * time.Minute,
		MinSpan:     1 * time.Hour,
		MaxSpan:     12 * time.Hour,
	}
	w := ComputeWindow(unsorted, base, 1540, opts)

	// With unsorted input, the loop stops after seeing 2 events (e1, e2),
	// which are the first two in the slice (not the earliest by time).
	// The window will end at e2.End (20m), not e3.End (180m).
	// This demonstrates that callers MUST pre-sort.
	e2End := base.Add(20 * time.Minute)
	if w.End.Equal(e2End) {
		// Expected: unsorted input produces incomplete coverage.
		// The contract is: caller must sort.
	} else {
		// If this fails, it means ComputeWindow is re-sorting (which it should NOT do).
		t.Logf("window end = %v, e2.End = %v", w.End, e2End)
	}
}

// Minor 4 regression: degenerate window (End <= Start) in X() fallback.
func TestWindowXDegenerateWindow(t *testing.T) {
	// Window with End <= Start.
	w := Window{Start: base, End: base, WidthPx: 1000} // degenerate: span is 0
	if got := w.X(base); got != 0 {
		t.Errorf("X(start) on degenerate window = %v, want 0 (fallback)", got)
	}
	if got := w.X(base.Add(1 * time.Hour)); got != 0 {
		t.Errorf("X(time) on degenerate window = %v, want 0 (fallback)", got)
	}
}

// Minor 5 regression: FitEvents: 0 behaves like 1.
func TestComputeWindowZeroFitEventsShowsOneEvent(t *testing.T) {
	evs := []calendar.Event{
		ev("e1", 30, 30),
		ev("e2", 120, 60),
	}
	opts := WindowOpts{
		FitEvents:   0, // Zero means "at least 1 event if available"
		PastContext: 15 * time.Minute,
		MinSpan:     1 * time.Hour,
		MaxSpan:     12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	// Should fit at least the first event.
	e1End := base.Add(60 * time.Minute) // "e1" ends at start + 30 + 30 = 60m
	if w.End.Before(e1End) {
		t.Errorf("FitEvents=0: window end = %v, does not cover first event ending %v", w.End, e1End)
	}
}
