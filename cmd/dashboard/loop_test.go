package main

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestNextTickAlignsToMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 17, 0, time.UTC)
	if got, want := nextTick(now), 43*time.Second; got != want {
		t.Errorf("nextTick = %v, want %v", got, want)
	}
}

func TestNextTickAtExactBoundaryWaitsFullMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	if got, want := nextTick(now), time.Minute; got != want {
		t.Errorf("nextTick = %v, want %v", got, want)
	}
}

func TestStoreKeepsLastGoodOnFailure(t *testing.T) {
	s := &Store{}
	evs := []calendar.Event{{Title: "kept"}}
	w := &weather.Weather{Current: weather.Conditions{TempF: 70}}
	s.SetEvents(evs, nil)
	s.SetWeather(w, nil)

	// A later failure must not clear what we already have.
	s.SetEvents(nil, []string{"ical: timeout"})
	s.SetWeather(nil, []string{"weather: timeout"})

	gotEvs, gotW, errs := s.Snapshot()
	if len(gotEvs) != 1 || gotEvs[0].Title != "kept" {
		t.Errorf("events = %v, want the last-good set", gotEvs)
	}
	if gotW == nil || gotW.Current.TempF != 70 {
		t.Error("weather = nil, want the last-good value")
	}
	if len(errs) != 2 {
		t.Errorf("errs = %v, want both failures reported", errs)
	}
}

func TestStoreReplacesOnSuccess(t *testing.T) {
	s := &Store{}
	s.SetEvents([]calendar.Event{{Title: "old"}}, nil)
	s.SetEvents([]calendar.Event{{Title: "new"}}, nil)
	evs, _, errs := s.Snapshot()
	if len(evs) != 1 || evs[0].Title != "new" {
		t.Errorf("events = %v, want the fresh set", evs)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none after success", errs)
	}
}
