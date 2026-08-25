// Package config loads and validates the dashboard's config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// CalendarSource is one iCal feed.
type CalendarSource struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	URL   string `json:"url"`
}

// Config mirrors config.json. Zero values are replaced by defaults in Load.
type Config struct {
	Location struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Timezone  string  `json:"timezone"`
	} `json:"location"`
	Units struct {
		Temperature   string `json:"temperature"`
		WindSpeed     string `json:"wind_speed"`
		Precipitation string `json:"precipitation"`
		Clock24h      bool   `json:"clock_24h"`
	} `json:"units"`
	Refresh struct {
		WeatherMinutes  int `json:"weather_minutes"`
		CalendarMinutes int `json:"calendar_minutes"`
	} `json:"refresh"`
	Agenda struct {
		DaysAhead int `json:"days_ahead"`
		MaxEvents int `json:"max_events"`
	} `json:"agenda"`
	Calendars []CalendarSource `json:"calendars"`
}

// Load reads config.json and fills in defaults for unset fields.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Location.Timezone == "" {
		c.Location.Timezone = "UTC"
	}
	if c.Units.Temperature == "" {
		c.Units.Temperature = "fahrenheit"
	}
	if c.Units.WindSpeed == "" {
		c.Units.WindSpeed = "mph"
	}
	if c.Units.Precipitation == "" {
		c.Units.Precipitation = "inch"
	}
	if c.Refresh.WeatherMinutes <= 0 {
		c.Refresh.WeatherMinutes = 15
	}
	if c.Refresh.CalendarMinutes <= 0 {
		c.Refresh.CalendarMinutes = 10
	}
	if c.Agenda.DaysAhead <= 0 {
		c.Agenda.DaysAhead = 7
	}
	if c.Agenda.MaxEvents <= 0 {
		c.Agenda.MaxEvents = 40
	}
}

// TimeLocation resolves the configured timezone, falling back to UTC when the
// name is unknown. The board has no tzdata guarantee, so this must not panic.
func (c *Config) TimeLocation() *time.Location {
	loc, err := time.LoadLocation(c.Location.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
