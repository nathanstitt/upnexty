// Package config loads and validates the dashboard's config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Save writes the config atomically: a temp file in the same directory, fsynced,
// then renamed over the target. /root is UBI on NAND, and a torn write during a
// power cut would leave a board that cannot parse its own config at boot.
// The previous file is kept as <path>.bak for the same reason.
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')

	// Same directory, so the rename below stays within one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	// fsync before rename: rename is atomic, but without the sync the rename
	// can land before the contents on a power loss.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	// 0600: this file holds the WiFi password. os.CreateTemp already uses 0600
	// and os.Rename preserves it, but set it explicitly so the intent is clear
	// and does not depend on CreateTemp's default.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	// Back up the current config by copying, not renaming. A rename would
	// briefly leave no file at path, and if the install below then failed the
	// board would have no config at all -- Load has no .bak fallback and main
	// exits when Load fails. A copy leaves the original in place until the
	// atomic rename replaces it.
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return fmt.Errorf("back up config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config for backup: %w", err)
	}

	// The commit point. Until this succeeds, path still holds the old config.
	// After this succeeds, path holds the new config. There is no window where
	// it is missing. Directory entry fsync is not done (dir inode is not synced);
	// on UBIFS the realistic worst case after a power loss is reverting to the
	// previous config, which is safe.
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}
