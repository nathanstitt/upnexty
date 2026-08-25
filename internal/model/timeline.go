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

// ComputeWindow picks a visible span that fits the next FitEvents events,
// clamped to [MinSpan, MaxSpan] so neither an imminent meeting nor a distant
// one distorts the scale.
func ComputeWindow(events []calendar.Event, now time.Time, widthPx float64, opts WindowOpts) Window {
	start := now.Add(-opts.PastContext)
	end := start.Add(opts.MinSpan)

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

	if span := end.Sub(start); span < opts.MinSpan {
		end = start.Add(opts.MinSpan)
	} else if opts.MaxSpan > 0 && span > opts.MaxSpan {
		end = start.Add(opts.MaxSpan)
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
