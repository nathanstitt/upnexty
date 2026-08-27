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
	c.Location.Timezone = "UTC"

	evs := []calendar.Event{
		// Standup and Design Review Sync start only 6 minutes apart. Design
		// Review Sync's long title collides with Standup's label and gets
		// pushed ~41px right of its own block (drift threshold is 80px), so
		// PlaceLabels marks it Drifted — this fixture exists specifically to
		// exercise the leader-mark ({{if .Drifted}}) template branch, which
		// two well-separated events would never trigger.
		{Title: "Standup", Color: "#4f9cff", Start: now.Add(10 * time.Minute), End: now.Add(20 * time.Minute)},
		{Title: "Design Review Sync", Color: "#ff7a59", Location: "Room B",
			Start: now.Add(16 * time.Minute), End: now.Add(45 * time.Minute)},
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
	return model.Build(now, c, evs, w, nil, nil)
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
	for _, want := range []string{"Standup", "Design Review Sync", "Company Holiday", "10:42"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

// TestRenderDrawsLeaderForDriftedLabel exercises the {{if .Drifted}} template
// branch. fixtureVM's Standup/Design Review Sync pair is deliberately close
// enough together (see the comment there) that PlaceLabels pushes the second
// label right of its own block's X — the exact clustered-events case Task 12
// hits every minute against live calendar data. Without this, no fixture ever
// set Drifted true and a regression in the leader-mark rendering would go
// unnoticed.
func TestRenderDrawsLeaderForDriftedLabel(t *testing.T) {
	vm := fixtureVM(t)

	// Mirrors the Drifted threshold Render applies (view.go: X-Anchor > 2).
	var sawDrift bool
	for _, l := range vm.Labels {
		if l.X-l.Anchor > 2 {
			sawDrift = true
		}
	}
	if !sawDrift {
		t.Fatal("fixture does not produce a drifted label; test no longer exercises the leader mark")
	}

	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `class="leader"`) {
		t.Error(`output missing class="leader" for a drifted label`)
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
	got, err := Render(model.Build(now, c, nil, nil, []string{"weather: timeout"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "10:42") {
		t.Error("clock must render even with no data")
	}
}

// TestRenderShowsStaleFlagWhenStale and TestRenderHidesStaleFlagWhenFresh
// assert both directions of the {{if .VM.Stale}} branch. Stale is meant to
// mean "this refresh cycle had a failure, so what's on screen may be
// last-good rather than current" — an indicator stuck on is exactly as
// wrong as one stuck off, and on an unattended kiosk nobody is watching for
// either failure mode, so both are asserted rather than just the happy path.
func TestRenderShowsStaleFlagWhenStale(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, []string{"weather: timeout"}, nil)
	if !vm.Stale {
		t.Fatal("fixture does not set Stale; test no longer exercises this branch")
	}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `id="stale-flag"`) {
		t.Error(`output missing id="stale-flag" when Stale is true`)
	}
}

// TestRenderShowsErrorTextWhenStale guards against the bug where the
// template's {{if .VM.Stale}}...{{else if .VM.Errors}}... branch made the
// Errors arm provably unreachable (model.Build sets Stale := len(errs) > 0,
// so the two conditions are always equivalent) — the actual diagnostic text,
// e.g. "ical(Personal): GET ...: 401 Unauthorized", was computed, carried
// through three layers, and then silently discarded, leaving only the bare
// word "Stale" on an unattended panel with no way to tell what actually
// failed. Both #stale-flag and #errors must render together.
func TestRenderShowsErrorTextWhenStale(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	wantErr := "ical(Personal): GET https://example.com/cal.ics: 401 Unauthorized"
	vm := model.Build(now, c, nil, nil, []string{wantErr}, nil)
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `id="errors"`) {
		t.Error(`output missing id="errors" when Errors is non-empty`)
	}
	if !strings.Contains(got, wantErr) {
		t.Errorf("output missing error text %q", wantErr)
	}
	// Stale and Errors must render together, not as alternatives.
	if !strings.Contains(got, `id="stale-flag"`) {
		t.Error(`output missing id="stale-flag" alongside errors`)
	}
}

func TestRenderHidesStaleFlagWhenFresh(t *testing.T) {
	vm := fixtureVM(t)
	if vm.Stale {
		t.Fatal("fixture unexpectedly sets Stale; test no longer exercises this branch")
	}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `id="stale-flag"`) {
		t.Error(`output has id="stale-flag" when Stale is false`)
	}
}

func TestRenderShowsSetupHint(t *testing.T) {
	vm := fixtureVM(t)
	vm.Setup = &model.SetupHint{APName: "upnext-1bfd", Password: "4c1bfd"}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"upnext-1bfd", "4c1bfd", "Set me up"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderOmitsSetupHintWhenConfigured(t *testing.T) {
	vm := fixtureVM(t)
	vm.Setup = nil
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	// Match the div, not the bare substring "setup-hint": that string also
	// appears in the inlined <style> block's #setup-hint selector, which is
	// always present regardless of whether the {{with .VM.Setup}} div rendered.
	if strings.Contains(got, `id="setup-hint"`) {
		t.Error("setup hint rendered when the board is configured")
	}
}

// TestRenderSetupHintShowsUnavailablePasswordWhenEmpty guards resolution #2:
// an empty Password (DefaultPassword's return when the MAC is unreadable or
// malformed) must never render as a blank credential -- that reads as a real
// but invisible password, which is worse than no hint at all. The template
// must show explicit "unavailable" text instead.
func TestRenderSetupHintShowsUnavailablePasswordWhenEmpty(t *testing.T) {
	vm := fixtureVM(t)
	vm.Setup = &model.SetupHint{APName: "upnext-setup", Password: ""}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "upnext-setup") {
		t.Error("output missing the fallback AP name")
	}
	if !strings.Contains(got, "unavailable") {
		t.Error(`output missing "unavailable" when Password is empty`)
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
