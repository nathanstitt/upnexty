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

// WiFiConfig holds the credentials the board associates with. Written by the
// portal; also templated into /etc/wpa_supplicant.conf by the wifi package.
type WiFiConfig struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// PortalConfig holds the admin password hash. Empty means no password has been
// set; the portal then accepts a device-derived default.
type PortalConfig struct {
	PasswordHash string `json:"password_hash"`
}

// DisplayConfig holds panel settings applied via sysfs.
type DisplayConfig struct {
	// Brightness is 0-255, matching the panel's sysfs range. A pointer so an
	// explicit 0 ("screen off") is distinguishable from an absent key, which
	// defaults to 200 -- a plain int makes those two cases identical.
	Brightness *int `json:"brightness"`
}

// Config mirrors config.json. Zero values are replaced by defaults in Load.
type Config struct {
	Location struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Timezone  string  `json:"timezone"`
	} `json:"location"`
	Units struct {
		Temperature string `json:"temperature"`
		Clock24h    bool   `json:"clock_24h"`
	} `json:"units"`
	Refresh struct {
		WeatherMinutes  int `json:"weather_minutes"`
		CalendarMinutes int `json:"calendar_minutes"`
	} `json:"refresh"`
	Agenda struct {
		DaysAhead int `json:"days_ahead"`
		MaxEvents int `json:"max_events"`
	} `json:"agenda"`
	WiFi      WiFiConfig       `json:"wifi"`
	Portal    PortalConfig     `json:"portal"`
	Display   DisplayConfig    `json:"display"`
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
	// Handle brightness: clamp out-of-range values to 0-255. The BrightnessValue()
	// accessor handles the default (200) for absent keys.
	if c.Display.Brightness != nil {
		if *c.Display.Brightness < 0 {
			v := 0
			c.Display.Brightness = &v
		} else if *c.Display.Brightness > 255 {
			v := 255
			c.Display.Brightness = &v
		}
	}
}

// BrightnessValue returns the configured brightness, or the default when
// unset. Callers should use this rather than dereferencing the pointer.
func (c *Config) BrightnessValue() int {
	if c.Display.Brightness == nil {
		return 200
	}
	return *c.Display.Brightness
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
