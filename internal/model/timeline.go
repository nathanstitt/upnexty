// Package model turns fetched data into a positioned view model. Everything
// here is pure: time is always a parameter, never time.Now().
package model

import (
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// Window is the visible time span mapped onto a pixel width.
type Window struct {
	Start, End time.Time
	WidthPx    float64
}

// X maps a time to its horizontal pixel offset. The mapping is strictly linear,
// so equal durations always occupy equal widths and every row (agenda, weather)
// can share one axis.
//
// If End <= Start (degenerate window), X returns 0 for all times.
func (w Window) X(t time.Time) float64 {
	span := w.End.Sub(w.Start).Seconds()
	if span <= 0 {
		return 0
	}
	return w.WidthPx * (t.Sub(w.Start).Seconds() / span)
}

// WindowOpts tunes how the visible span is chosen.
type WindowOpts struct {
	FitEvents   int           // aim to show this many upcoming events
	PastContext time.Duration // keep this much already-elapsed time visible
	MinSpan     time.Duration // never zoom in tighter than this
	MaxSpan     time.Duration // never zoom out wider than this
}

// ComputeWindow picks a visible span that fits the next FitEvents events within
// hard bounds [MinSpan, MaxSpan].
//
// Precedence (in order):
//   1. MaxSpan is a hard outer bound; the window will never exceed start+MaxSpan.
//      This ensures axis legibility. If MaxSpan > 0 and MaxSpan < MinSpan,
//      the effective span is MaxSpan (MaxSpan wins).
//   2. FitEvents is best-effort: the function aims to fit this many upcoming,
//      non-all-day events, but never exceeds MaxSpan to do so.
//   3. MinSpan is a floor: if the fitted span is less than MinSpan (and MaxSpan
//      permits), the window is extended to MinSpan.
//
// Callers must provide events sorted by Start time. Merging events from multiple
// calendar feeds requires re-sorting before calling ComputeWindow.
//
// FitEvents: 0 behaves like 1 (shows at least one event if any exist).
func ComputeWindow(events []calendar.Event, now time.Time, widthPx float64, opts WindowOpts) Window {
	start := now.Add(-opts.PastContext)
	end := start.Add(opts.MinSpan)

	// Compute the hard MaxSpan bound (may override MinSpan).
	var maxEnd time.Time
	if opts.MaxSpan > 0 {
		maxEnd = start.Add(opts.MaxSpan)
	}

	var seen int
	for _, e := range events {
		if e.AllDay || !e.End.After(now) {
			continue
		}
		seen++
		if e.End.After(end) {
			end = e.End
		}
		if seen >= opts.FitEvents {
			break
		}
	}

	// Apply MinSpan floor (respecting MaxSpan precedence).
	if span := end.Sub(start); span < opts.MinSpan {
		end = start.Add(opts.MinSpan)
	}

	// Unconditional MaxSpan clamp (final, always executes).
	// This ensures MaxSpan is never violated, whether the loop ran or not.
	if maxEnd != (time.Time{}) && end.After(maxEnd) {
		end = maxEnd
	}

	return Window{Start: start, End: end, WidthPx: widthPx}
}

// Block is one event positioned on the axis.
type Block struct {
	Event calendar.Event
	X, W  float64
}

// Blocks positions timed events that overlap the window. Widths are strictly
// duration-proportional except for minWidthPx, a visibility floor that keeps a
// very short event from rendering sub-pixel.
func Blocks(events []calendar.Event, w Window, minWidthPx float64) []Block {
	var out []Block
	for _, e := range events {
		if e.AllDay {
			continue
		}
		// Overlap test: any part of the event inside the window.
		if !e.End.After(w.Start) || !e.Start.Before(w.End) {
			continue
		}
		x := w.X(e.Start)
		width := w.X(e.End) - x
		if x < 0 { // in progress: clamp the left edge, keep the visible remainder
			width += x
			x = 0
		}
		if width < minWidthPx {
			width = minWidthPx
		}
		out = append(out, Block{Event: e, X: x, W: width})
	}
	return out
}
