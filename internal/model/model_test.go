package model

import (
	"testing"
	"time"

	"github.com/nathanstitt/upnexty/internal/calendar"
	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/weather"
)

func testConfig() *config.Config {
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	c.Units.Clock24h = false
	return c
}

func TestBuildFormatsClock(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, nil, nil)
	// 12-hour must carry the meridiem: a bare "2:05" is ambiguous on a wall
	// display and does not match the reference design.
	if vm.ClockTime != "2:05 PM" {
		t.Errorf("ClockTime = %q, want \"2:05 PM\"", vm.ClockTime)
	}
	if vm.ClockDate != "Tue, Aug 25" {
		t.Errorf("ClockDate = %q, want \"Tue, Aug 25\"", vm.ClockDate)
	}
}

func TestBuildFormatsClock24h(t *testing.T) {
	c := testConfig()
	c.Units.Clock24h = true
	now := time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	vm := Build(now, c, nil, nil, nil, nil)
	if vm.ClockTime != "14:05" {
		t.Errorf("ClockTime = %q, want 14:05", vm.ClockTime)
	}
}

func TestBuildSurvivesNilWeatherAndEvents(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, []string{"weather: timeout"}, nil)
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
	vm := Build(now, testConfig(), evs, nil, nil, nil)
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
	vm := Build(now, testConfig(), evs, nil, nil, nil)
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
	vm := Build(now, testConfig(), evs, nil, nil, nil)
	if len(vm.AllDay) != 1 || vm.AllDay[0].Title != "holiday" {
		t.Errorf("AllDay = %v, want [holiday]", vm.AllDay)
	}
	for _, c := range vm.Agenda.Cards {
		if c.Kind == CardEvent && c.Event.AllDay {
			t.Error("all-day event leaked into the agenda card row")
		}
	}
}

func TestBuildPlacesNowBar(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{{Title: "e", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}
	vm := Build(now, testConfig(), evs, nil, nil, nil)
	// The only event is still ahead, so it is the anchor and the day's first
	// entry: it sits flush left and the bar parks at its left edge, which is
	// the row's own padding in.
	if want := float64(CardPadPx); vm.Agenda.NowBarXPx != want {
		t.Errorf("NowBarXPx = %v, want %v", vm.Agenda.NowBarXPx, want)
	}
	// The row only ever shifts left; a positive offset would open a blank
	// strip at the left edge of the agenda.
	if vm.Agenda.OffsetPx > 0 {
		t.Errorf("OffsetPx = %v, want <= 0", vm.Agenda.OffsetPx)
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
	vm := Build(now, testConfig(), nil, w, nil, nil)
	if vm.Current == nil || vm.Current.TempF != 72 {
		t.Errorf("Current = %v, want 72F", vm.Current)
	}
	if len(vm.Forecast) != 2 {
		t.Errorf("len(Forecast) = %d, want 2", len(vm.Forecast))
	}
}

func TestBuildPopulatesHourly(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w := &weather.Weather{
		Current: weather.Conditions{TempF: 72, Code: 1},
		Hourly: []weather.HourPoint{
			{Time: now, TempF: 72, PrecipProb: 10, Code: 1},
			{Time: now.Add(time.Hour), TempF: 74, PrecipProb: 20, Code: 1},
		},
	}
	vm := Build(now, testConfig(), nil, w, nil, nil)
	if len(vm.Hourly) != 2 {
		t.Fatalf("len(Hourly) = %d, want 2", len(vm.Hourly))
	}
	if vm.Hourly[1].TempF != 74 {
		t.Errorf("Hourly[1].TempF = %v, want 74", vm.Hourly[1].TempF)
	}
}

func TestBuildHourlyEmptyWhenNoWeather(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, nil, nil)
	if len(vm.Hourly) != 0 {
		t.Errorf("Hourly = %v, want empty when weather is nil", vm.Hourly)
	}
}

func TestBuildStaleWhenCalendarErrorsDespiteWeatherOk(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w := &weather.Weather{Current: weather.Conditions{TempF: 72}}
	vm := Build(now, testConfig(), nil, w, []string{"calendar: timeout"}, nil)
	if !vm.Stale {
		t.Error("Stale should be true when calendar errored, even though weather succeeded")
	}
}

func TestBuildStaleWhenWeatherErrorsDespiteCalendarOk(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{{Title: "e", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}
	vm := Build(now, testConfig(), evs, nil, []string{"weather: timeout"}, nil)
	if !vm.Stale {
		t.Error("Stale should be true when weather errored, even though calendar succeeded")
	}
}

func TestBuildNotStaleWhenNoErrors(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w := &weather.Weather{Current: weather.Conditions{TempF: 72}}
	evs := []calendar.Event{{Title: "e", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}
	vm := Build(now, testConfig(), evs, w, nil, nil)
	if vm.Stale {
		t.Error("Stale should be false when there are no errors")
	}
}

func TestBuildShowsSetupHintWhenUnconfigured(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	c := testConfig()
	vm := Build(now, c, nil, nil, nil, &SetupHint{APName: "upnext-1bfd", Password: "4c1bfd"})
	if vm.Setup == nil {
		t.Fatal("Setup = nil, want the hint")
	}
	if vm.Setup.APName != "upnext-1bfd" || vm.Setup.Password != "4c1bfd" {
		t.Errorf("Setup = %+v", vm.Setup)
	}
}

func TestBuildOmitsSetupHintWhenConfigured(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, nil, nil)
	if vm.Setup != nil {
		t.Errorf("Setup = %+v, want nil once configured", vm.Setup)
	}
}

func TestBuildUntilNextReadsNowInFinalMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "soon", Start: now.Add(30 * time.Second), End: now.Add(30 * time.Minute)},
	}
	vm := Build(now, testConfig(), evs, nil, nil, nil)
	if vm.UntilNext != "now" {
		t.Errorf("UntilNext = %q, want %q for an event 30s away", vm.UntilNext, "now")
	}
}

// zonedConfig is deliberately not UTC. Every other test here runs at UTC, which
// is why a whole class of timezone bug rendered every event time five hours off
// on the panel while the suite stayed green.
func zonedConfig(t *testing.T) *config.Config {
	t.Helper()
	if _, err := time.LoadLocation("America/Chicago"); err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	c := &config.Config{}
	c.Location.Timezone = "America/Chicago"
	return c
}

// A feed states times in UTC (DTSTART ending in Z). The panel must show them on
// its own wall clock, not the feed's.
func TestBuildStatesEventTimesInConfiguredZone(t *testing.T) {
	cfg := zonedConfig(t)
	// 16:26 UTC is 11:26 CDT -- the case that surfaced this: an event starting
	// at 16:07Z was labelled "4:07 PM" beside a clock reading 11:26 AM.
	now := time.Date(2026, 8, 31, 16, 26, 0, 0, time.UTC)
	evs := []calendar.Event{{
		Title: "Design Review",
		Start: time.Date(2026, 8, 31, 16, 7, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 31, 16, 57, 0, 0, time.UTC),
	}}

	vm := Build(now, cfg, evs, nil, nil, nil)

	if len(vm.Agenda.Cards) == 0 {
		t.Fatal("no agenda cards")
	}
	got := vm.Agenda.Cards[0]
	if got.Kind == CardStack {
		if len(got.Stacked) == 0 {
			t.Fatal("stack slot has no events")
		}
		if s := got.Stacked[0].StartText(); s != "11:07 AM" {
			t.Errorf("card start = %q, want %q (11:07 CDT, not 4:07 UTC)", s, "11:07 AM")
		}
	}
	// The clock and the event must agree about what time zone the panel is in.
	if vm.ClockTime != "11:26 AM" {
		t.Errorf("ClockTime = %q, want %q", vm.ClockTime, "11:26 AM")
	}
}

// Day separators and the FREE FOR same-day guard ask a wall-clock question, so
// they must be resolved in the panel's zone. Late local evening is the case that
// breaks: 8pm CDT is already the next calendar day in UTC.
func TestBuildLabelsDayBoundaryInConfiguredZone(t *testing.T) {
	cfg := zonedConfig(t)
	// 01:30 UTC on Sep 1 is 20:30 CDT on Aug 31 -- still "today" locally.
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	evs := []calendar.Event{{
		Title: "Late Sync",
		Start: time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC), // 21:00 CDT, same local day
		End:   time.Date(2026, 9, 1, 2, 30, 0, 0, time.UTC),
	}}

	vm := Build(now, cfg, evs, nil, nil, nil)

	// Same local day and >30min out, so this is FREE FOR rather than ModeDone.
	// Under the UTC comparison the event looked like tomorrow and fell through.
	if vm.NowBlock.Mode != ModeFree {
		t.Errorf("Mode = %v, want ModeFree for an event later the same local day", vm.NowBlock.Mode)
	}
	if vm.NowBlock.NextAt != "at 9:00 PM" {
		t.Errorf("NextAt = %q, want %q", vm.NowBlock.NextAt, "at 9:00 PM")
	}
	for _, c := range vm.Agenda.Cards {
		if c.Kind == CardDaySep {
			t.Errorf("day separator emitted within a single local day: %q", c.SepText)
		}
	}
}

// Converting event zones must not change Event.Key, or a timezone edit would
// silently orphan every saved mute.
func TestBuildKeepsMutesStableAcrossZoneConversion(t *testing.T) {
	cfg := zonedConfig(t)
	now := time.Date(2026, 8, 31, 16, 26, 0, 0, time.UTC)
	e := calendar.Event{
		UID:   "evt-1",
		Title: "Design Review",
		Start: time.Date(2026, 8, 31, 16, 7, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 31, 16, 57, 0, 0, time.UTC),
	}
	cfg.Muted = []config.MutedEvent{{Key: e.Key(), Title: e.Title}}

	vm := Build(now, cfg, []calendar.Event{e}, nil, nil, nil)

	for _, c := range vm.Agenda.Cards {
		if c.Kind == CardEvent || c.Kind == CardStack {
			t.Fatalf("muted event still rendered as %v", c.Kind)
		}
	}
}

// An all-day event is a date, not an instant. Shifting it by a zone offset would
// move it onto the wrong day.
func TestBuildKeepsAllDayEventsOnTheirDate(t *testing.T) {
	cfg := zonedConfig(t)
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 8, 31, 16, 26, 0, 0, time.UTC)
	// Midnight local, as calendar.Parse yields for a VALUE=DATE entry.
	start := time.Date(2026, 8, 31, 0, 0, 0, 0, loc)
	evs := []calendar.Event{{
		Title: "Company Holiday", AllDay: true,
		Start: start, End: start.AddDate(0, 0, 1),
	}}

	vm := Build(now, cfg, evs, nil, nil, nil)

	if len(vm.AllDay) != 1 {
		t.Fatalf("AllDay = %d events, want 1", len(vm.AllDay))
	}
	if y, m, d := vm.AllDay[0].Start.Date(); y != 2026 || m != time.August || d != 31 {
		t.Errorf("all-day start = %v, want 2026-08-31", vm.AllDay[0].Start)
	}
}
