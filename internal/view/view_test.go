package view

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// fixtureVM returns a populated view model. ViewModel now carries Hourly
// directly (Contract 1), so Render only ever needs the one value.
func fixtureVM(t *testing.T) model.ViewModel {
	t.Helper()
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Name = "Home"
	c.Location.Timezone = "UTC"

	evs := []calendar.Event{
		{Title: "Standup", Color: "#4f9cff", Start: now.Add(18 * time.Minute), End: now.Add(33 * time.Minute)},
		{Title: "Design Review", Color: "#ff7a59", Location: "Room B",
			Start: now.Add(90 * time.Minute), End: now.Add(150 * time.Minute)},
		{Title: "Company Holiday", AllDay: true, Color: "#4f9cff",
			Start: now.Truncate(24 * time.Hour), End: now.Add(24 * time.Hour)},
	}
	w := &weather.Weather{
		Current: weather.Conditions{TempF: 72, Code: 2, Time: now},
		Daily: []weather.DayPoint{
			{Date: now, HiF: 88, LoF: 64, PrecipProb: 20, Code: 2},
			{Date: now.AddDate(0, 0, 1), HiF: 90, LoF: 66, PrecipProb: 0, Code: 0},
		},
	}
	for i := 0; i < 8; i++ {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: now.Add(time.Duration(i) * time.Hour), TempF: 70 + float64(i), PrecipProb: i * 5,
		})
	}
	return model.Build(now, c, evs, w, nil)
}

func TestRenderProducesCompleteDocument(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<!DOCTYPE html>", "<html", "</html>", "<style>"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// CSS must be inlined; the board loads no external resources.
	if strings.Contains(got, `<link`) {
		t.Error("output has a <link> tag; CSS must be inlined")
	}
	if strings.Contains(got, "<script") {
		t.Error("output has a <script> tag; doctaculous discards scripts")
	}
}

func TestRenderIncludesEventTitlesAndClock(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Standup", "Design Review", "Company Holiday", "10:42"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderEscapesTitles(t *testing.T) {
	vm := fixtureVM(t)
	vm.Labels[0].Text = `Tom & Jerry <script>`
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<script>") {
		t.Error("title was not HTML-escaped")
	}
	if !strings.Contains(got, "&amp;") {
		t.Error("ampersand was not escaped")
	}
}

func TestRenderHandlesEmptyModel(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	got, err := Render(model.Build(now, c, nil, nil, []string{"weather: timeout"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "10:42") {
		t.Error("clock must render even with no data")
	}
}

func TestRenderMatchesGolden(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/dashboard.html", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile("testdata/dashboard.html")
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if got != string(want) {
		t.Error("HTML differs from golden; re-run with UPDATE_GOLDEN=1 if intended")
	}
}
