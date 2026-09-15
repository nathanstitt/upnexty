// Package config loads and validates the dashboard's config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CalendarSource is one calendar the panel shows, fetched either as an iCal
// feed or through the Google Calendar API.
//
// Kind selects the backend and is empty on every config written before the API
// path existed, so empty means "ical" -- see SourceKind. That keeps an existing
// config.json loading unchanged rather than needing a migration.
type CalendarSource struct {
	Name  string `json:"name"`
	Color string `json:"color"`

	// URL is the iCal feed address. Unused by Google sources.
	URL string `json:"url"`

	// Kind is "" or "ical" for an iCal feed, "google" for the Calendar API.
	Kind string `json:"kind,omitempty"`

	// CalID is the Google calendar id, usually an email address. Google only.
	CalID string `json:"cal_id,omitempty"`

	// Account names the authorised Google account whose token reads this
	// calendar, and is the filename under the token directory.
	//
	// A token authorises ONE account. That account often sees other calendars
	// -- this board's nas@stitt.org token lists ns51@rice.edu as owner, so one
	// authorisation covers both -- but that is a property of how those accounts
	// are shared, not a rule. A calendar belonging to an account the signed-in
	// user cannot see needs its own authorisation, which is why the token is
	// named here rather than assumed to be the board's only one.
	//
	// Empty on a board holding exactly one token means "that one", so the
	// common single-account setup needs no ceremony.
	Account string `json:"account,omitempty"`
}

// Calendar source kinds. The zero value is KindICal so a config predating the
// Google backend keeps working untouched.
const (
	KindICal   = "ical"
	KindGoogle = "google"
)

// SourceKind returns the source's backend, resolving the empty default.
func (c CalendarSource) SourceKind() string {
	if c.Kind == "" {
		return KindICal
	}
	return c.Kind
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

// MutedEvent is one event hidden from the panel by a tap on its card.
//
// Key is the identity (calendar.Event.Key); the rest is denormalized copy so
// the portal can show a human-readable unmute list. The panel never reads
// Title/Start -- only Key is load-bearing -- but without them the settings page
// could only offer a list of opaque UIDs, which nobody can act on.
type MutedEvent struct {
	Key   string    `json:"key"`
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	// End is when this occurrence finishes, used to prune the list: a mute for
	// an event that is over can never match again and is dead weight in a file
	// on NAND.
	End   time.Time `json:"end"`
	Muted time.Time `json:"muted_at"`
}

// DeviceConfig holds settings about the board itself rather than what it shows.
type DeviceConfig struct {
	// Hostname is the name the board answers to and, more usefully, the name it
	// announces over DHCP -- which is what puts it in the router's DNS, so it
	// can be reached at a stable name instead of a lease-dependent address.
	//
	// Empty means "whatever the image shipped with": the board already has a
	// hostname from its rootfs, and overwriting it with a derived default on
	// first boot would rename a device the user never asked to rename.
	Hostname string `json:"hostname,omitempty"`
}

// Config mirrors config.json. Zero values are replaced by defaults in Load.
type Config struct {
	Device   DeviceConfig `json:"device"`
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
	// Muted lists events hidden from the panel. Written by a tap on the panel,
	// cleared from the settings page.
	Muted []MutedEvent `json:"muted,omitempty"`
}

// HostnameMaxLen is the longest label DNS allows. The kernel would take more,
// but a name that cannot be resolved is not useful for the thing this setting
// exists to do.
const HostnameMaxLen = 63

// NormalizeHostname lower-cases and trims a hostname the way DNS treats it, so
// "UpNext " and "upnext" are not two different settings.
func NormalizeHostname(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateHostname checks that a name is a legal DNS label: letters, digits and
// interior hyphens only, 1-63 characters, not starting or ending with a hyphen.
//
// This is stricter than what the kernel accepts for sethostname, deliberately.
// The point of setting a hostname here is that the board announces it over DHCP
// and the router puts it in DNS; a name that is legal to the kernel but illegal
// as a label gets silently dropped or mangled by the DHCP server, which looks
// like the setting simply not working. Rejecting it here means the error lands
// on the person who can fix it, in the form they typed it.
//
// The returned error is phrased for that person -- it is rendered directly on
// the settings page.
func ValidateHostname(s string) error {
	if s == "" {
		return fmt.Errorf("hostname cannot be empty")
	}
	if len(s) > HostnameMaxLen {
		return fmt.Errorf("hostname is %d characters; the limit is %d",
			len(s), HostnameMaxLen)
	}
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return fmt.Errorf("hostname cannot start or end with a hyphen")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("hostname may only contain letters, numbers and "+
				"hyphens; %q is not allowed", string(r))
		}
	}
	return nil
}

// IsMuted reports whether an event key is in the muted list.
func (c *Config) IsMuted(key string) bool {
	for _, m := range c.Muted {
		if m.Key == key {
			return true
		}
	}
	return false
}

// Mute adds an event to the muted list. Muting an already-muted event is a
// no-op rather than a duplicate, so a double tap cannot corrupt the list.
func (c *Config) Mute(m MutedEvent) {
	if c.IsMuted(m.Key) {
		return
	}
	c.Muted = append(c.Muted, m)
}

// Unmute removes an event from the muted list, reporting whether it was there.
func (c *Config) Unmute(key string) bool {
	for i, m := range c.Muted {
		if m.Key == key {
			c.Muted = append(c.Muted[:i], c.Muted[i+1:]...)
			return true
		}
	}
	return false
}

// MuteGrace is how long a mute outlives the event it hides.
//
// Pruning on End alone is wrong: the agenda still shows events that have
// finished (they scroll left as "past" cards), and the in-progress event is
// the single most likely thing to be muted. Both would have their mute deleted
// the moment it was written, so the card would reappear on the next render and
// the button would look broken.
//
// A day covers the panel's whole visible window with room to spare, and an
// entry that outlives its usefulness by a day costs a few dozen bytes.
const MuteGrace = 24 * time.Hour

// PruneMuted drops mutes for occurrences that ended more than MuteGrace ago.
// Their keys embed a start time and can never match a future event, so keeping
// them only grows a file that lives on NAND and pads every settings page.
//
// Entries with a zero End are kept: they predate End being recorded, and
// guessing an expiry for them risks unmuting something the user muted.
func (c *Config) PruneMuted(now time.Time) int {
	cutoff := now.Add(-MuteGrace)
	kept := make([]MutedEvent, 0, len(c.Muted))
	for _, m := range c.Muted {
		if m.End.IsZero() || m.End.After(cutoff) {
			kept = append(kept, m)
		}
	}
	n := len(c.Muted) - len(kept)
	c.Muted = kept
	return n
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
