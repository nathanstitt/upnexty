package model

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func testConfig() *config.Config {
	c := &config.Config{}
	c.Location.Name = "Home"
	c.Location.Timezone = "UTC"
	c.Units.Clock24h = false
	return c
}

func TestBuildFormatsClock(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, nil)
	if vm.ClockTime != "2:05" {
		t.Errorf("ClockTime = %q, want 2:05", vm.ClockTime)
	}
	if vm.ClockDate != "Tuesday, August 25" {
		t.Errorf("ClockDate = %q", vm.ClockDate)
	}
}

func TestBuildFormatsClock24h(t *testing.T) {
	c := testConfig()
	c.Units.Clock24h = true
	now := time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	vm := Build(now, c, nil, nil, nil)
	if vm.ClockTime != "14:05" {
		t.Errorf("ClockTime = %q, want 14:05", vm.ClockTime)
	}
}

func TestBuildSurvivesNilWeatherAndEvents(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, []string{"weather: timeout"})
	if vm.Current != nil {
		t.Error("Current should be nil when weather is nil")
	}
	if vm.NextEvent != nil {
		t.Error("NextEvent should be nil with no events")
	}
	if len(vm.Errors) != 1 {
		t.Errorf("Errors = %v, want the passed-in error", vm.Errors)
	}
	if vm.ClockTime == "" {
		t.Error("clock must still render when every fetch failed")
	}
}

func TestBuildPicksNextEvent(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "past", Start: now.Add(-2 * time.Hour), End: now.Add(-90 * time.Minute)},
		{Title: "next", Start: now.Add(20 * time.Minute), End: now.Add(50 * time.Minute)},
		{Title: "later", Start: now.Add(3 * time.Hour), End: now.Add(4 * time.Hour)},
	}
	vm := Build(now, testConfig(), evs, nil, nil)
	if vm.NextEvent == nil || vm.NextEvent.Title != "next" {
		t.Fatalf("NextEvent = %v, want 'next'", vm.NextEvent)
	}
	if vm.UntilNext != "20m" {
		t.Errorf("UntilNext = %q, want 20m", vm.UntilNext)
	}
}

func TestBuildTreatsInProgressEventAsNext(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "running", Start: now.Add(-10 * time.Minute), End: now.Add(20 * time.Minute)},
	}
	vm := Build(now, testConfig(), evs, nil, nil)
	if vm.NextEvent == nil || vm.NextEvent.Title != "running" {
		t.Fatalf("NextEvent = %v, want the in-progress event", vm.NextEvent)
	}
	if vm.UntilNext != "now" {
		t.Errorf("UntilNext = %q, want 'now'", vm.UntilNext)
	}
}

func TestBuildSeparatesAllDayEvents(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "holiday", AllDay: true, Start: now.Truncate(24 * time.Hour), End: now.Add(24 * time.Hour)},
		{Title: "timed", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	}
	vm := Build(now, testConfig(), evs, nil, nil)
	if len(vm.AllDay) != 1 || vm.AllDay[0].Title != "holiday" {
		t.Errorf("AllDay = %v, want [holiday]", vm.AllDay)
	}
	for _, b := range vm.Blocks {
		if b.Event.AllDay {
			t.Error("all-day event leaked into Blocks")
		}
	}
}

func TestBuildComputesNowX(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{{Title: "e", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}
	vm := Build(now, testConfig(), evs, nil, nil)
	if vm.NowX <= 0 {
		t.Errorf("NowX = %v, want > 0 (past context puts now inside the window)", vm.NowX)
	}
	if vm.NowX != vm.Window.X(now) {
		t.Errorf("NowX = %v, inconsistent with Window.X(now) = %v", vm.NowX, vm.Window.X(now))
	}
}

func TestBuildPopulatesForecast(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w := &weather.Weather{
		Current: weather.Conditions{TempF: 72, Code: 1},
		Daily: []weather.DayPoint{
			{Date: now, HiF: 88, LoF: 64}, {Date: now.AddDate(0, 0, 1), HiF: 90, LoF: 66},
		},
	}
	vm := Build(now, testConfig(), nil, w, nil)
	if vm.Current == nil || vm.Current.TempF != 72 {
		t.Errorf("Current = %v, want 72F", vm.Current)
	}
	if len(vm.Forecast) != 2 {
		t.Errorf("len(Forecast) = %d, want 2", len(vm.Forecast))
	}
}
