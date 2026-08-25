// Package chart renders weather data as SVG. SVG rather than <canvas> because
// doctaculous has no JavaScript engine, and rather than emoji/PNG because the
// board has no emoji font and vector art scales cleanly.
package chart

import (
	"fmt"
	"strings"

	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

const labelBandPx = 24 // reserved at the bottom for hour labels

// Hourly renders the temperature curve and precipitation bars across the same
// time window the agenda uses, so the two rows align on one axis.
func Hourly(w *weather.Weather, win model.Window, heightPx float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" class="wx-chart">`,
		win.WidthPx, heightPx, win.WidthPx, heightPx)

	// Every return closes the element explicitly. A deferred WriteString would
	// not work: b.String() is evaluated before deferred calls run.
	chartH := heightPx - labelBandPx
	if w == nil || len(w.Hourly) == 0 || chartH <= 0 {
		return b.String() + "</svg>"
	}

	// Only points inside the window matter.
	type pt struct {
		x, temp float64
		prob    int
	}
	var pts []pt
	minT, maxT := w.Hourly[0].TempF, w.Hourly[0].TempF
	for _, h := range w.Hourly {
		if h.Time.Before(win.Start) || h.Time.After(win.End) {
			continue
		}
		pts = append(pts, pt{x: win.X(h.Time), temp: h.TempF, prob: h.PrecipProb})
		if h.TempF < minT {
			minT = h.TempF
		}
		if h.TempF > maxT {
			maxT = h.TempF
		}
	}
	if len(pts) == 0 {
		return b.String() + "</svg>"
	}
	if maxT-minT < 1 { // avoid divide-by-zero on a flat forecast
		maxT = minT + 1
	}
	tempY := func(t float64) float64 {
		return chartH - ((t-minT)/(maxT-minT))*(chartH*0.7) - chartH*0.15
	}

	// Precipitation bars first so the curve draws over them.
	barW := win.WidthPx / float64(max(len(pts), 1))
	for _, p := range pts {
		if p.prob <= 0 {
			continue
		}
		h := (float64(p.prob) / 100) * chartH * 0.5
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="wx-precip"/>`,
			p.x, chartH-h, barW*0.6, h)
	}

	// Temperature curve.
	b.WriteString(`<path class="wx-temp" d="`)
	for i, p := range pts {
		verb := "L"
		if i == 0 {
			verb = "M"
		}
		fmt.Fprintf(&b, "%s%.1f %.1f ", verb, p.x, tempY(p.temp))
	}
	b.WriteString(`"/>`)

	return b.String() + "</svg>"
}
