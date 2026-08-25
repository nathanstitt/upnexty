package chart

import (
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
