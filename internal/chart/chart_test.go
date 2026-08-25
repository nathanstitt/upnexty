package chart

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestIconReturnsSVGForKnownCodes(t *testing.T) {
	for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 75, 82, 95} {
		got := Icon(code)
		if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
			t.Errorf("Icon(%d) = %.40q, want inline <svg>", code, got)
		}
	}
}

func TestIconFallsBackForUnknownCode(t *testing.T) {
	got := Icon(9999)
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Errorf("Icon(unknown) = %.40q, want a fallback <svg>", got)
	}
}

func TestIconNeverReturnsEmoji(t *testing.T) {
	// The board cannot render these; a regression here is invisible on screen.
	for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 75, 82, 95} {
		for _, r := range Icon(code) {
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
	start := strings.Index(svg, `<path class="wx-temp" d="`)
	if start < 0 {
		t.Fatalf("no temperature path found in %.200q", svg)
	}
	start += len(`<path class="wx-temp" d="`)
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
	const chartH = heightPx - labelBandPx
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
