// Package chart renders weather data as SVG. SVG rather than <canvas> because
// omnidoc has no JavaScript engine, and rather than emoji/PNG because the
// board has no emoji font and vector art scales cleanly.
package chart

import (
	"fmt"
	"strings"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// The chart covers a window around the present rather than one starting at the
// agenda's left edge, matching the reference (WX_PAST_HOURS / WX_FWD_HOURS in
// its app.js).
//
// A window anchored at the agenda's start ran 8 PM -> 6 PM: it opened at night,
// spent its first third on hours already gone, and pushed the afternoon peak off
// the right edge. Centring on now instead puts the waking day on screen.
//
// The span also has to be wide enough to show a curve. The agenda window is ~4h,
// which yields 4-5 hourly points across 1540px -- segments 385px apart read as a
// straight diagonal, and a real 78-113F swing was invisible. 18h gives ~18
// points at ~85px.
const (
	ChartPastHours = 6 * time.Hour
	ChartFwdHours  = 12 * time.Hour
	ChartSpan      = ChartPastHours + ChartFwdHours
)

// SpanWindow returns the chart's window: ChartSpan wide, positioned so that
// "now" falls under the NOW bar at nowBarXPx.
//
// The agenda and the chart do not share a scale -- the agenda is a card list
// and the chart is a time axis, and forcing them onto one scale is what put the
// chart in the wrong part of the day. They do share the NOW bar, which spans
// both bands and so asserts one instant for the whole panel. The window is
// therefore anchored to the bar rather than to a fixed fraction.
//
// Anchoring it at a fixed ChartPastHours behind now was wrong once the bar
// began to sweep: the bar sat wherever the current entry put it while the chart
// kept "now" at 6/18 of its width, so the bar crossed the curve at the wrong
// time -- measured at 246px, about 2.9 hours stale, and the error changed as
// the bar moved. The bar pointing at a temperature that is not the current one
// is worse than either row being slightly off on its own.
//
// The span stays fixed at ChartSpan, so the amount of history shown varies with
// the bar: near the left edge the curve is nearly all forecast, which is the
// correct trade -- the bar's position is the thing that must not lie.
func SpanWindow(agenda model.Window, now time.Time, nowBarXPx float64) model.Window {
	frac := 0.0
	if agenda.WidthPx > 0 {
		frac = nowBarXPx / agenda.WidthPx
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	start := now.Add(-time.Duration(frac * float64(ChartSpan)))
	return model.Window{
		Start:   start,
		End:     start.Add(ChartSpan),
		WidthPx: agenda.WidthPx,
	}
}

// Colours are emitted as SVG presentation attributes rather than CSS classes.
// Verified on the engine: a stylesheet rule does not cascade into an inline
// <svg>'s children -- a class-based stroke paints zero pixels even with a
// literal hex value -- so styling the curve from style.css silently produced an
// invisible chart. Keep these in sync with :root in style.css by eye; there is
// no way to share them.
const (
	curveColor  = "#7ec8f0" // --wx-curve
	precipColor = "rgba(74,144,217,0.55)"
	// Labels read from across a room, so they are the panel's text colour at a
	// size close to the body text -- not the dim 17px they were, which receded
	// into the background at any distance.
	labelColor = "#dde3ef" // --text
	labelSize  = 26

	// Vertical insets on the curve. topPadPx is what makes room for the label
	// drawn above each point, and it is not free to pick.
	//
	// SVG text grows upward from its baseline, and the baseline sits
	// labelOffsetPx above the point, so the inset has to cover
	// labelOffsetPx + labelInkAscentPx + clearance. The chart band butts
	// directly against the card row above it (#wx-zone is an ordinary in-flow
	// sibling of #agenda-row), so anything short of that puts the digits into
	// the cards.
	//
	// This has been wrong twice. At 26 the ascender clipped outside the viewBox
	// entirely; at 32 it stopped clipping but the ink still landed 5px into the
	// band, which reads as touching. 40 = 10 + 17 + 13 of clearance.
	//
	// Raise it whenever labelSize or labelOffsetPx goes up.
	topPadPx    = 40.0
	bottomPadPx = 6.0

	// How far above its point each temperature label's baseline sits.
	labelOffsetPx = 10.0

	// How far the digits' ink actually rises above their baseline at
	// labelSize. Measured by rasterizing "97" at a known baseline and reading
	// the topmost lit row -- 17px at 26, not the ~21 that 0.8em would suggest.
	// The font's declared ascent is not the ink's, and the difference is the
	// whole margin here.
	labelInkAscentPx = 17.0

	// The curve is the chart's subject: thicker and brighter than the 2.5 it
	// was, which read as a hairline beside 26px labels.
	curveWidth = 4.0
)

// The top inset must clear the label's ink with room to spare, or the digits
// render into the card row above. A compile error here means labelSize or
// labelOffsetPx moved without topPadPx following -- which is how this shipped
// broken twice. Re-measure labelInkAscentPx if labelSize changed.
const _ = uint(topPadPx - (labelOffsetPx + labelInkAscentPx + 8))

// Hourly renders the temperature curve and precipitation bars across the same
// time window the agenda uses, so the two rows align on one axis.
func Hourly(w *weather.Weather, win model.Window, heightPx float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" class="wx-chart">`,
		win.WidthPx, heightPx, win.WidthPx, heightPx)

	// Every return closes the element explicitly. A deferred WriteString would
	// not work: b.String() is evaluated before deferred calls run.
	//
	// The hour axis is separate HTML below this SVG (#wx-hours), so the whole
	// height is the curve band.
	chartH := heightPx
	if w == nil || len(w.Hourly) == 0 || chartH <= 0 {
		return b.String() + "</svg>"
	}

	// Only points inside the window matter. win.End itself is excluded: X(win.End)
	// == win.WidthPx, which would place its precip bar entirely past the right
	// edge of the viewBox.
	type pt struct {
		x, temp float64
		prob    int
		// edge marks a point interpolated onto the band's boundary rather than
		// a real reading. It is a curve vertex only: it carries no
		// precipitation, takes no label, and does not count toward the bar
		// width, all of which would otherwise claim an hour never measured.
		edge bool
	}
	var pts []pt
	for _, h := range w.Hourly {
		if h.Time.Before(win.Start) || !h.Time.Before(win.End) {
			continue
		}
		pts = append(pts, pt{x: win.X(h.Time), temp: h.TempF, prob: h.PrecipProb})
	}
	if len(pts) == 0 {
		return b.String() + "</svg>"
	}

	// Carry the curve out to both edges of the band.
	//
	// The data lands on hour boundaries but the window starts wherever the NOW
	// bar puts it, so the first in-window point is up to an hour in -- measured
	// at 64px of empty band before the curve began, which read as the chart
	// being inset from the card row above it rather than as missing data.
	//
	// The bracketing readings exist in the feed and were simply filtered out,
	// so the edge value is interpolated from the real neighbour rather than
	// invented: at worst it is the same linear segment the curve would have
	// drawn anyway, just clipped to the band. Only the curve is extended --
	// precipitation bars and labels stay on real readings, since a bar at a
	// synthetic point would claim an hour that was never measured.
	//
	// prob is deliberately zero on an edge point: it is a curve vertex only.
	edgeAt := func(at time.Time) (pt, bool) {
		var before, after *weather.HourPoint
		for i := range w.Hourly {
			h := &w.Hourly[i]
			if !h.Time.After(at) && (before == nil || h.Time.After(before.Time)) {
				before = h
			}
			if h.Time.After(at) && (after == nil || h.Time.Before(after.Time)) {
				after = h
			}
		}
		if before == nil || after == nil {
			return pt{}, false // no reading on both sides: nothing to interpolate
		}
		span := after.Time.Sub(before.Time).Seconds()
		if span <= 0 {
			return pt{}, false
		}
		f := at.Sub(before.Time).Seconds() / span
		return pt{x: win.X(at), temp: before.TempF + f*(after.TempF-before.TempF), edge: true}, true
	}
	if edge, ok := edgeAt(win.Start); ok && edge.x < pts[0].x {
		pts = append([]pt{edge}, pts...)
	}
	// win.End maps to exactly WidthPx, the band's right edge.
	if edge, ok := edgeAt(win.End); ok && edge.x > pts[len(pts)-1].x {
		pts = append(pts, edge)
	}
	// min/max must come from the plotted points only. Seeding from
	// w.Hourly[0] before filtering let an out-of-window outlier (e.g. an
	// overnight low hours before the window starts) own the scale and flatten
	// the visible curve even though it's never drawn.
	minT, maxT := pts[0].temp, pts[0].temp
	for _, p := range pts[1:] {
		if p.temp < minT {
			minT = p.temp
		}
		if p.temp > maxT {
			maxT = p.temp
		}
	}
	if maxT-minT < 1 { // avoid divide-by-zero on a flat forecast
		maxT = minT + 1
	}
	// Fixed insets rather than percentages, as in the reference (its TOP_PAD /
	// BOTTOM_PAD). The top inset is what leaves room for the temperature label
	// drawn above each point; without it the curve hugged the top of the band
	// with the labels clipped and all the empty space beneath.
	tempY := func(t float64) float64 {
		return topPadPx + (1-(t-minT)/(maxT-minT))*(chartH-topPadPx-bottomPadPx)
	}

	// Precipitation bars first so the curve draws over them. The divisor counts
	// real readings only: the interpolated edge vertices are not hours, and
	// including them would narrow every bar.
	real := 0
	for _, p := range pts {
		if !p.edge {
			real++
		}
	}
	if real == 0 {
		real = 1
	}
	barW := win.WidthPx / float64(real)
	for _, p := range pts {
		if p.prob <= 0 {
			continue
		}
		h := (float64(p.prob) / 100) * chartH * 0.5
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`,
			p.x, chartH-h, barW*0.6, h, precipColor)
	}

	// Temperature curve.
	fmt.Fprintf(&b, `<path fill="none" stroke="%s" stroke-width="%.1f" stroke-linecap="round" stroke-linejoin="round" d="`,
		curveColor, curveWidth)
	for i, p := range pts {
		verb := "L"
		if i == 0 {
			verb = "M"
		}
		fmt.Fprintf(&b, "%s%.1f %.1f ", verb, p.x, tempY(p.temp))
	}
	b.WriteString(`"/>`)

	// Inline temperature labels along the curve. The reference draws four
	// sparse ones; six 17px labels read as noise at a distance, so the count is
	// derived from the point total rather than a fixed stride -- the window can
	// change and this should stay at four.
	const wantLabels = 4
	// Stride over real readings only, and index them separately from pts: an
	// edge vertex is interpolated, so labelling one would print a temperature
	// that was never measured at a time that is not on the hour.
	realPts := make([]int, 0, len(pts))
	for i, p := range pts {
		if !p.edge {
			realPts = append(realPts, i)
		}
	}
	stride := max(1, len(realPts)/wantLabels)
	for n, i := range realPts {
		p := pts[i]
		// Offset by half a stride so the first label is not at x=0, where it
		// would be clipped by the viewBox.
		if (n+stride/2)%stride != 0 {
			continue
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-family="Roboto, sans-serif" font-size="%d" font-weight="600">%.0f&#176;</text>`,
			p.x, tempY(p.temp)-labelOffsetPx, labelColor, labelSize, p.temp)
	}

	return b.String() + "</svg>"
}

// Tick is one label on the weather strip's hour axis.
type Tick struct {
	XPx   float64
	Label string
}

// HourTicks returns axis labels across the chart window, one every third hour.
//
// At the 18h span an hour is ~85px. Two-hourly (~170px) was legible but gave 9
// labels across the strip, which competes with the temperature labels above it
// for attention; three-hourly gives 6 and reads as an axis rather than a ruler.
func HourTicks(win model.Window) []Tick {
	if win.End.Before(win.Start) || win.WidthPx <= 0 {
		return nil
	}
	var out []Tick
	// Start at the first whole hour inside the window.
	t := win.Start.Truncate(time.Hour)
	if t.Before(win.Start) {
		t = t.Add(time.Hour)
	}
	for !t.After(win.End) {
		if t.Hour()%3 == 0 {
			out = append(out, Tick{XPx: win.X(t), Label: t.Format("3 PM")})
		}
		t = t.Add(time.Hour)
	}
	return out
}
