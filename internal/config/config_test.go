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
	p := writeTemp(t, `{"location":{"name":"Home","latitude":38.5,"longitude":-92.1}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Location.Name != "Home" {
		t.Errorf("Name = %q, want Home", c.Location.Name)
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
