package chart

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestIconReturnsSVGForKnownCodes(t *testing.T) {
	for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 75, 82, 95} {
		got := Icon(code, 40)
		if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
			t.Errorf("Icon(%d) = %.40q, want inline <svg>", code, got)
		}
	}
}

func TestIconFallsBackForUnknownCode(t *testing.T) {
	got := Icon(9999, 40)
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Errorf("Icon(unknown) = %.40q, want a fallback <svg>", got)
	}
}

// The icons ship at width="24"; those attributes override the CSS box on this
// engine, so an unsized icon rendered as a dot in the middle of a 50px slot.
// Nothing on screen flags it, hence the assertion.
func TestIconIsSizedToTheRequestedBox(t *testing.T) {
	for _, code := range []int{0, 2, 61, 9999} {
		got := Icon(code, 50)
		if !strings.Contains(got, `width="50"`) || !strings.Contains(got, `height="50"`) {
			t.Errorf("Icon(%d, 50) = %.80q, want width/height of 50", code, got)
		}
		if strings.Contains(got, `"24"`) {
			t.Errorf("Icon(%d, 50) still carries the authored 24px size: %.80q", code, got)
		}
		// Dropping viewBox would crop the art instead of scaling it.
		if !strings.Contains(got, `viewBox="0 0 24 24"`) {
			t.Errorf("Icon(%d, 50) lost its viewBox: %.80q", code, got)
		}
	}
}

// omnidoc does not inherit stroke properties from the root <svg> down to
// its children: a path relying on an inherited stroke paints nothing at all.
// The icons were authored that way, so every ray, raindrop and fog line was
// invisible while the filled shapes rendered -- the "sun" was a bare dot.
// Verified with a minimal probe (see docs/omnidoc-gaps.md). Each stroked
// element must therefore carry its own stroke.
func TestIconStrokesAreNotInheritedFromRoot(t *testing.T) {
	entries, err := iconFS.ReadDir("icons")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := iconFS.ReadFile("icons/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		svg := string(b)
		root := svg[:strings.Index(svg, ">")+1]
		if strings.Contains(root, "stroke=") {
			t.Errorf("%s: root <svg> carries stroke=; children do not inherit it "+
				"on this engine, so the stroke must sit on each element", e.Name())
		}
		// A body that strokes must say so itself, not lean on the root.
		body := svg[len(root):]
		if strings.Contains(body, "stroke-width=") && !strings.Contains(body, "stroke=") {
			t.Errorf("%s: has stroke-width but no stroke on any child", e.Name())
		}
	}
}

func TestIconNeverReturnsEmoji(t *testing.T) {
	// The board cannot render these; a regression here is invisible on screen.
	for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 75, 82, 95} {
		for _, r := range Icon(code, 40) {
			if r > 0x2000 {
				t.Errorf("Icon(%d) contains non-ASCII rune %q", code, r)
			}
		}
	}
}

func hourlyFixture() *weather.Weather {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w := &weather.Weather{Current: weather.Conditions{TempF: 70}}
	temps := []float64{68, 70, 73, 77, 80, 82, 81, 78}
	probs := []int{0, 10, 20, 40, 60, 30, 10, 0}
	for i := range temps {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: base.Add(time.Duration(i) * time.Hour), TempF: temps[i], PrecipProb: probs[i],
		})
	}
	return w
}

func TestHourlyProducesSVG(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(hourlyFixture(), win, 104)
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Fatalf("Hourly = %.60q, want <svg>", got)
	}
	if !strings.Contains(got, "<path") {
		t.Error("expected a <path> for the temperature curve")
	}
	if !strings.Contains(got, "<rect") {
		t.Error("expected <rect> bars for precipitation")
	}
}

func TestHourlyHandlesNoData(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(&weather.Weather{}, win, 104)
	if strings.Contains(got, "NaN") {
		t.Error("empty weather produced NaN coordinates")
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Error("expected a valid empty <svg>, not a panic or blank string")
	}
}

func TestHourlyNilWeather(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(nil, win, 104)
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") || !strings.HasSuffix(strings.TrimSpace(got), "</svg>") {
		t.Errorf("Hourly(nil, ...) = %q, want a valid empty <svg>", got)
	}
}

// pathYs extracts the y-coordinates from a "M x y L x y L x y ..." path `d`
// attribute produced by Hourly.
func pathYs(t *testing.T, svg string) []float64 {
	t.Helper()
	// Located by the curve colour, not the whole attribute string: matching on
	// stroke-width too made this fail whenever the curve was restyled, which
	// says nothing about the geometry these tests actually check.
	i := strings.Index(svg, `stroke="`+curveColor+`"`)
	if i < 0 {
		t.Fatalf("no temperature path found in %.200q", svg)
	}
	const dAttr = ` d="`
	rel := strings.Index(svg[i:], dAttr)
	if rel < 0 {
		t.Fatalf("temperature path has no d attribute in %.200q", svg)
	}
	start := i + rel + len(dAttr)
	end := strings.Index(svg[start:], `"`)
	if end < 0 {
		t.Fatalf("unterminated path d attribute in %.200q", svg)
	}
	d := svg[start : start+end]
	// Fields alternate x, y (the leading M/L verb is glued to the x token),
	// so odd indices are the y coordinates.
	fields := strings.Fields(d)
	var ys []float64
	for i := 1; i < len(fields); i += 2 {
		var y float64
		if _, err := fmt.Sscanf(fields[i], "%f", &y); err != nil {
			t.Fatalf("could not parse y coordinate %q: %v", fields[i], err)
		}
		ys = append(ys, y)
	}
	return ys
}

// TestHourlyIgnoresOutOfWindowOutlierForScale is a regression test: min/max
// for the y-axis must be derived only from the points actually plotted
// (inside the window). Seeding the range from w.Hourly[0] before filtering
// let an overnight low well before the window own the scale and flatten the
// visible curve, even though that point was never drawn. See task 8 review.
func TestHourlyIgnoresOutOfWindowOutlierForScale(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(4 * time.Hour), WidthPx: 1540}

	w := &weather.Weather{}
	// Hourly[0]: a 35F overnight low ten hours before the window starts.
	// Never plotted, but if it seeds min/max it flattens everything below.
	w.Hourly = append(w.Hourly, weather.HourPoint{
		Time: base.Add(-10 * time.Hour), TempF: 35,
	})
	// In-window points with real variation: 70.0 -> 74.5F.
	inWindow := []float64{70.0, 71.5, 73.0, 74.5}
	for i, temp := range inWindow {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: base.Add(time.Duration(i) * time.Hour), TempF: temp,
		})
	}

	got := Hourly(w, win, 104)
	ys := pathYs(t, got)
	if len(ys) != len(inWindow) {
		t.Fatalf("plotted %d points, want %d (outlier should be filtered out): %v", len(ys), len(inWindow), ys)
	}

	minY, maxY := ys[0], ys[0]
	for _, y := range ys[1:] {
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	spread := maxY - minY

	const heightPx = 104
	// The hour axis is separate HTML now, so the whole SVG height is curve band.
	const chartH = heightPx
	// The drawing area reserves the top/bottom 15% as margin, so the usable
	// band is chartH*0.7. A real 4.5F climb across 4 points should span a
	// meaningful fraction of that, not be squashed to a few px by an
	// out-of-window outlier owning the scale.
	if spread < (chartH*0.7)/2 {
		t.Errorf("y spread = %.1fpx, want > half the drawing area (%.1fpx); curve looks flat: ys=%v", spread, (chartH*0.7)/2, ys)
	}
}

func TestHourlyMatchesGolden(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(hourlyFixture(), win, 104)

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/hourly.svg", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile("testdata/hourly.svg")
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if got != string(want) {
		t.Errorf("SVG differs from golden.\n got: %.200q\nwant: %.200q", got, string(want))
	}
}

// The chart's "now" must land under the NOW bar.
//
// The bar spans the agenda and the weather strip, so it asserts one instant for
// the whole panel. When the chart was anchored at a fixed ChartPastHours behind
// now, and the bar began sweeping the current entry instead of sitting at a
// fixed fraction, the two disagreed by 246px on a real frame -- the bar crossed
// the curve about 2.9 hours from the present, pointing at a temperature that
// was not the current one.
func TestChartNowLandsUnderTheNowBar(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 49, 0, 0, time.UTC)
	agenda := model.Window{WidthPx: model.TrackWidth}

	// Across the bar's whole travel, including the edges.
	for _, barX := range []float64{0, 18, 267, 513, 900, 1400, model.TrackWidth} {
		win := SpanWindow(agenda, now, barX)

		if got := win.End.Sub(win.Start); got != ChartSpan {
			t.Errorf("barX=%v: span = %v, want %v", barX, got, ChartSpan)
		}
		// X(now) is where the chart puts the present moment.
		if got := win.X(now); got < barX-1 || got > barX+1 {
			t.Errorf("barX=%v: the chart puts now at x=%.1f, %.1f px from the bar",
				barX, got, got-barX)
		}
	}
}

// A zero-width agenda must not divide by zero.
func TestSpanWindowHandlesZeroWidth(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	win := SpanWindow(model.Window{WidthPx: 0}, now, 400)
	if win.End.Sub(win.Start) != ChartSpan {
		t.Errorf("span = %v, want %v", win.End.Sub(win.Start), ChartSpan)
	}
}

// The curve must reach both edges of the band.
//
// Readings land on hour boundaries but the window starts wherever the NOW bar
// puts it, so the first in-window reading can be most of an hour in. That left
// 64px of empty band before the curve began, which read as the chart being
// inset from the card row above rather than as missing data.
func TestCurveReachesBothEdges(t *testing.T) {
	now := time.Date(2026, 9, 1, 15, 21, 0, 0, time.UTC)
	w := &weather.Weather{Current: weather.Conditions{TempF: 98, Time: now}}
	base := now.Truncate(time.Hour).Add(-10 * time.Hour)
	for i := range 34 {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: base.Add(time.Duration(i) * time.Hour), TempF: 80 + float64(i%12)})
	}

	// Several bar positions: the window start moves with it, so the offset
	// between the window edge and the first hourly reading varies.
	//
	// Only positions whose window is fully inside the feed are checked. A
	// window that opens before the earliest reading has nothing to interpolate
	// from, and drawing to the edge there would be inventing data rather than
	// extending a real segment -- a gap is the honest rendering.
	for _, barX := range []float64{100, 268, 513} {
		win := SpanWindow(model.Window{WidthPx: model.TrackWidth}, now, barX)
		if win.Start.Before(w.Hourly[0].Time) || win.End.After(w.Hourly[len(w.Hourly)-1].Time) {
			t.Fatalf("barX=%v: fixture does not bracket the window; widen the feed", barX)
		}
		svg := Hourly(w, win, 104)

		m := regexp.MustCompile(`d="M([0-9.]+) `).FindStringSubmatch(svg)
		if m == nil {
			t.Fatalf("barX=%v: no curve path", barX)
		}
		var startX float64
		fmt.Sscanf(m[1], "%f", &startX)
		if startX > 1 {
			t.Errorf("barX=%v: curve starts at x=%.1f, leaving a gap against the "+
				"left edge of the band", barX, startX)
		}

		// And the far end: the last vertex should reach the right edge.
		all := regexp.MustCompile(`[ML]([0-9.]+) [0-9.]+`).FindAllStringSubmatch(m[0][:0]+svg, -1)
		var lastX float64
		for _, g := range all {
			var x float64
			fmt.Sscanf(g[1], "%f", &x)
			if x > lastX {
				lastX = x
			}
		}
		if lastX < model.TrackWidth-1 {
			t.Errorf("barX=%v: curve ends at x=%.1f, short of the band's %d",
				barX, lastX, model.TrackWidth)
		}
	}
}

// Interpolated edge vertices are curve geometry only.
//
// A precipitation bar or a temperature label on one would claim a measurement
// at a time that is not on the hour and was never reported.
func TestEdgePointsCarryNoDataMarks(t *testing.T) {
	now := time.Date(2026, 9, 1, 15, 21, 0, 0, time.UTC)
	w := &weather.Weather{Current: weather.Conditions{TempF: 98, Time: now}}
	base := now.Truncate(time.Hour).Add(-10 * time.Hour)
	for i := range 34 {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time:       base.Add(time.Duration(i) * time.Hour),
			TempF:      80 + float64(i%12),
			PrecipProb: 50, // every real reading would draw a bar
		})
	}
	win := SpanWindow(model.Window{WidthPx: model.TrackWidth}, now, 268)
	svg := Hourly(w, win, 104)

	// No bar and no label may sit at the very edges, where the interpolated
	// vertices are.
	for _, re := range []struct{ name, pat string }{
		{"precip bar", `<rect x="(0\.0|1539\.\d|1540\.0)"`},
		{"label", `<text x="(0\.0|1539\.\d|1540\.0)"`},
	} {
		if regexp.MustCompile(re.pat).MatchString(svg) {
			t.Errorf("a %s was drawn on an interpolated edge vertex", re.name)
		}
	}
}
