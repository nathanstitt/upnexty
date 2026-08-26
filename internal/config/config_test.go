package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	p := writeTemp(t, `{"location":{"latitude":38.5,"longitude":-92.1}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Refresh.WeatherMinutes != 15 {
		t.Errorf("WeatherMinutes = %d, want default 15", c.Refresh.WeatherMinutes)
	}
	if c.Refresh.CalendarMinutes != 10 {
		t.Errorf("CalendarMinutes = %d, want default 10", c.Refresh.CalendarMinutes)
	}
	if c.Agenda.DaysAhead != 7 {
		t.Errorf("DaysAhead = %d, want default 7", c.Agenda.DaysAhead)
	}
	if c.Units.Temperature != "fahrenheit" {
		t.Errorf("Temperature = %q, want default fahrenheit", c.Units.Temperature)
	}
}

func TestLoadRespectsExplicitValues(t *testing.T) {
	p := writeTemp(t, `{"refresh":{"weather_minutes":30,"calendar_minutes":5},
		"units":{"temperature":"celsius"}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Refresh.WeatherMinutes != 30 {
		t.Errorf("WeatherMinutes = %d, want 30", c.Refresh.WeatherMinutes)
	}
	if c.Units.Temperature != "celsius" {
		t.Errorf("Temperature = %q, want celsius", c.Units.Temperature)
	}
}

func TestLoadTimezoneFallsBackToUTC(t *testing.T) {
	p := writeTemp(t, `{"location":{"timezone":"Not/AZone"}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.TimeLocation().String(); got != "UTC" {
		t.Errorf("TimeLocation = %s, want UTC", got)
	}
}

func TestLoadErrorsOnBadJSON(t *testing.T) {
	p := writeTemp(t, `{not json`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestLoadErrorsOnMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/config.json"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadNewSections(t *testing.T) {
	p := writeTemp(t, `{
		"wifi": {"ssid": "HomeNet", "password": "secret"},
		"portal": {"password_hash": "abc123"},
		"display": {"brightness": 120}
	}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.WiFi.SSID != "HomeNet" || c.WiFi.Password != "secret" {
		t.Errorf("WiFi = %+v", c.WiFi)
	}
	if c.Portal.PasswordHash != "abc123" {
		t.Errorf("PasswordHash = %q", c.Portal.PasswordHash)
	}
	if c.Display.Brightness == nil || *c.Display.Brightness != 120 {
		t.Errorf("Brightness = %v, want 120", c.Display.Brightness)
	}
}

func TestBrightnessDefaultsAndClamps(t *testing.T) {
	// Unset -> a sane default rather than a black screen.
	c, err := Load(writeTemp(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Brightness != nil {
		t.Errorf("unset Brightness pointer = %v, want nil", c.Display.Brightness)
	}
	if c.BrightnessValue() != 200 {
		t.Errorf("BrightnessValue() = %d, want 200", c.BrightnessValue())
	}

	// Explicit 0 -> stays 0 (the bug: must fail before fix, pass after).
	c, err = Load(writeTemp(t, `{"display":{"brightness":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Brightness == nil || *c.Display.Brightness != 0 {
		t.Errorf("explicit 0 Brightness = %v, want 0", c.Display.Brightness)
	}
	if c.BrightnessValue() != 0 {
		t.Errorf("BrightnessValue() for explicit 0 = %d, want 0", c.BrightnessValue())
	}

	// Negative -> clamped to 0.
	c, err = Load(writeTemp(t, `{"display":{"brightness":-10}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Brightness == nil || *c.Display.Brightness != 0 {
		t.Errorf("negative Brightness = %v, want 0", c.Display.Brightness)
	}

	// Out of range -> clamped to the panel's 0-255.
	c, err = Load(writeTemp(t, `{"display":{"brightness":999}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Brightness == nil || *c.Display.Brightness != 255 {
		t.Errorf("clamped Brightness = %v, want 255", c.Display.Brightness)
	}
	if c.BrightnessValue() != 255 {
		t.Errorf("BrightnessValue() for clamped = %d, want 255", c.BrightnessValue())
	}
}

func TestDeadFieldsAreGone(t *testing.T) {
	// A config carrying the removed keys must still load: existing boards have
	// these in their config.json and must not break on upgrade.
	p := writeTemp(t, `{
		"location": {"name": "Home", "timezone": "UTC"},
		"units": {"wind_speed": "mph", "precipitation": "inch"}
	}`)
	if _, err := Load(p); err != nil {
		t.Fatalf("removed keys must be ignored, not rejected: %v", err)
	}
}

func TestSaveRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")

	c := &Config{}
	c.Location.Timezone = "America/Chicago"
	c.Location.Latitude = 38.5
	c.WiFi.SSID = "HomeNet"
	b120 := 120
	c.Display.Brightness = &b120
	c.Calendars = []CalendarSource{{Name: "Work", Color: "#4f9cff", URL: "https://example.com/c.ics"}}

	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Location.Timezone != "America/Chicago" || got.WiFi.SSID != "HomeNet" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.BrightnessValue() != 120 {
		t.Errorf("Brightness = %d, want 120", got.BrightnessValue())
	}
	if len(got.Calendars) != 1 || got.Calendars[0].Name != "Work" {
		t.Errorf("Calendars = %+v", got.Calendars)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	c := &Config{}
	if err := c.Save(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the config plus its backup -- no stray *.tmp left behind.
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveKeepsPreviousAsBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")

	first := &Config{}
	first.Location.Timezone = "UTC"
	if err := first.Save(p); err != nil {
		t.Fatal(err)
	}
	second := &Config{}
	second.Location.Timezone = "America/Chicago"
	if err := second.Save(p); err != nil {
		t.Fatal(err)
	}

	bak, err := Load(p + ".bak")
	if err != nil {
		t.Fatalf("backup missing or unreadable: %v", err)
	}
	if bak.Location.Timezone != "UTC" {
		t.Errorf("backup holds %q, want the previous value UTC", bak.Location.Timezone)
	}
}
