package config

import (
	"os"
	"path/filepath"
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
	if c.Display.Brightness != 120 {
		t.Errorf("Brightness = %d, want 120", c.Display.Brightness)
	}
}

func TestBrightnessDefaultsAndClamps(t *testing.T) {
	// Unset -> a sane default rather than a black screen.
	c, err := Load(writeTemp(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Brightness != 200 {
		t.Errorf("default Brightness = %d, want 200", c.Display.Brightness)
	}
	// Out of range -> clamped to the panel's 0-255.
	c, err = Load(writeTemp(t, `{"display":{"brightness":999}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Brightness != 255 {
		t.Errorf("clamped Brightness = %d, want 255", c.Display.Brightness)
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
