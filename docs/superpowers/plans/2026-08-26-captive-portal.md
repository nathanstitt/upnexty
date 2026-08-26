# Captive Portal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Configure the dashboard from a phone — the board raises its own access point when WiFi is down, and keeps the same portal reachable on the LAN when it is up.

**Architecture:** A `portal` goroutine inside the existing `dashboard` binary, not a second process. Config moves behind the mutex-guarded `Store` that already holds weather and calendar data, so a handler validates, writes atomically, and swaps the pointer — the next tick renders the new settings with no restart and no dropped frame.

**Tech Stack:** Go stdlib `net/http` and `html/template` (no framework, no third-party modules). On-board `hostapd` 2.10 and `dnsmasq` 2.90 for AP mode. Plain HTML forms, progressively enhanced.

## Global Constraints

- **Module:** `github.com/nathanstitt/luckfox-dashboard`, `go 1.26.0`.
- **Dependencies:** stdlib only, plus the existing doctaculous dependency. **No new third-party modules** — not for routing, not for templating, not for password hashing (`golang.org/x/crypto` is NOT available; use `crypto/sha256` + `crypto/subtle`).
- **Cross-compile:** `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0`, `-ldflags="-s -w"`. Verify with `scripts/build.sh dashboard`.
- **No `time.Now()` in testable logic** — pass `now time.Time` as a parameter. The board has no RTC and boots at 1970.
- **gofmt clean.** The repo is currently clean and must stay so.
- **Board tooling that does NOT exist:** no `iptables`, no `iw`, no `udhcpd`, no `depmod`, no `apt`/`gcc`. Available: `hostapd`, `dnsmasq`, `wpa_supplicant`, `wpa_cli`, `udhcpc`, `ip`, `start-stop-daemon`, `rdate`.
- **Panel geometry:** 1920×480 landscape, blitted rotated 270° into a 480×1920 XR24 framebuffer.
- **Palette (from the spec, use these exact values):** `#0b0d12` ground, `#141924` raised, `#263041` hairline, `#e8ecf3` primary text, `#8b97ab` secondary, `#4f9cff` accent, `#ff7a59` error-only.
- **⚠️ Experiments touching `wlan0` must be self-restoring.** adb rides the USB gadget stack and the board's other route is WiFi; disrupting the interface can strand the board and require a physical power cycle. Always background such commands with an unconditional restore (see Task 1). Recovery order: `adb kill-server && adb start-server`, then SSH, then power cycle.

---

## File Structure

```
internal/portal/
  server.go        HTTP server lifecycle, routes, mode detection
  auth.go          admin password: MAC default, hashing, constant-time compare
  handlers.go      one handler per settings group
  render.go        template execution + the view types the templates consume
  templates/
    layout.html    shell: status strip + nav
    settings.html  the settings rows
    setup.html     first-run AP flow
  assets/
    portal.css     the 380:1540 hairline signature, phone-first
internal/wifi/
  wifi.go          scan, associate, status  (wraps wpa_cli)
  ap.go            hostapd/dnsmasq lifecycle for AP mode
  runner.go        command runner interface, so tests never touch the board
internal/config/
  config.go        + Save(), + WiFi/Portal/Display sections, - dead fields
cmd/dashboard/
  loop.go          Store gains cfg behind the existing mutex
  main.go          start the portal goroutine
board/etc/init.d/
  S99wlan0         + AP fallback when association fails
```

Rationale: `wifi` is separated from `portal` because the AP/STA lifecycle is the part that can strand the board and needs its own fake-runner tests. `runner.go` exists so every wifi test runs on the host with no hardware.

---

### Task 1: Confirm AP mode — ALREADY DONE

**Status: complete.** See `docs/ap-mode-probe.md` (committed).

This task existed to gate the rest of the plan: the captive portal's AP fallback
assumes the AIC8800DC supports AP mode, and nothing confirmed it. That question
is now answered on the running board, without disturbing the interface:

```
# wpa_cli -i wlan0 get_capability modes
AP
```

`get_capability modes` queries nl80211 for supported interface types. It is a
read-only query, safe on a live board — unlike running `hostapd` directly, which
in an earlier attempt tore down the association and, because adb rides the same
USB/WiFi path, stranded the board until it was power-cycled.

Corroborated by the driver binary, which implements the cfg80211 callbacks
hostapd needs (`start_ap`, `stop_ap`, `change_beacon`, `del_station`,
`change_station`) plus a full APM command set and `"AP started: ch=%d"` log
strings.

Also learned, and relevant to Task 7: the radio is **2.4 GHz only**, channels
1–14. Channel 6 is valid.

**Nothing to implement. Start at Task 2.**

Still unproven, and only testable by raising a real AP (Task 13 covers it):
whether the driver sustains a stable AP with clients attached. Concurrency of AP
and STA is *not* required — AP is a fallback for when STA has already failed.

---

### Task 2: Config — remove dead fields, add new sections

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.sample.json`
- Modify: `internal/model/model.go` (remove `LocationName`)
- Modify: `internal/model/model_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `config.Config` with `WiFi`, `Portal`, `Display` sections and without `Units.WindSpeed`, `Units.Precipitation`, `Location.Name`
  - `type WiFiConfig struct { SSID, Password string }`
  - `type PortalConfig struct { PasswordHash string }`
  - `type DisplayConfig struct { Brightness int }`

Three existing fields have **zero consumers** — verified by grep across the repo:
- `Units.WindSpeed` — no wind is displayed anywhere
- `Units.Precipitation` — precipitation renders as a bare `%`
- `Location.Name` — reaches `ViewModel.LocationName` but appears in no template

Deleting them keeps the portal honest: a control that does nothing is worse than no control.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/config/ -run 'TestLoadNewSections|TestBrightness|TestDeadFields'`
Expected: FAIL — `c.WiFi undefined`

- [ ] **Step 3: Update the Config struct**

In `internal/config/config.go`, delete `Name` from the `Location` struct and delete `WindSpeed`/`Precipitation` from `Units`. Then add the three new sections:

```go
// WiFiConfig holds the credentials the board associates with. Written by the
// portal; also templated into /etc/wpa_supplicant.conf by the wifi package.
type WiFiConfig struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// PortalConfig holds the admin password hash. Empty means "not set" -- the
// portal then accepts the MAC-derived default (see internal/portal/auth.go).
type PortalConfig struct {
	PasswordHash string `json:"password_hash"`
}

// DisplayConfig holds panel settings applied via sysfs.
type DisplayConfig struct {
	Brightness int `json:"brightness"` // 0-255, matches the panel's range
}
```

Add the fields to `Config`:

```go
	WiFi    WiFiConfig    `json:"wifi"`
	Portal  PortalConfig  `json:"portal"`
	Display DisplayConfig `json:"display"`
```

And in `applyDefaults`:

```go
	// 200/255 is the shipped default and a reasonable indoor level. Zero would
	// be a black panel, which is indistinguishable from a crash.
	if c.Display.Brightness <= 0 {
		c.Display.Brightness = 200
	}
	if c.Display.Brightness > 255 {
		c.Display.Brightness = 255
	}
```

Removing struct fields is safe for existing configs: `encoding/json` ignores unknown keys by default.

- [ ] **Step 4: Remove LocationName from the view model**

In `internal/model/model.go`, delete the `LocationName string` field from `ViewModel` and the `LocationName: c.Location.Name,` line in `Build`. It is set but never rendered — confirmed absent from `internal/view/templates/dashboard.html`.

Update `internal/model/model_test.go`: remove any assertion referencing `LocationName`. If a test's only purpose was that field, delete the test.

- [ ] **Step 5: Update the sample config**

Replace `config.sample.json` with:

```json
{
  "location": {
    "latitude": 38.5693164,
    "longitude": -92.1629241,
    "timezone": "America/Chicago"
  },
  "units": {
    "temperature": "fahrenheit",
    "clock_24h": false
  },
  "refresh": {
    "weather_minutes": 15,
    "calendar_minutes": 10
  },
  "agenda": {
    "days_ahead": 7,
    "max_events": 40
  },
  "display": {
    "brightness": 200
  },
  "calendars": [
    { "name": "Personal", "color": "#4f9cff", "url": "PASTE_ICAL_URL" }
  ]
}
```

Note `wifi` and `portal` are absent: they are written by the portal, not hand-edited, and a sample containing an empty password invites someone to commit a real one.

- [ ] **Step 6: Run the full suite**

Run: `gofmt -w internal/ cmd/ && go test -count=1 ./...`
Expected: PASS across all packages. If `internal/view` fails, a template still references `LocationName` — remove it there too.

- [ ] **Step 7: Commit**

```bash
git add internal/config internal/model config.sample.json
git commit -m "feat: config sections for wifi, portal, and display

Removes units.wind_speed, units.precipitation, and location.name -- all three
had zero consumers, so a portal control for them would do nothing. Existing
configs carrying the keys still load; encoding/json ignores unknown fields."
```

---

### Task 3: Config — atomic save

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `config.Config` (Task 2)
- Produces: `func (c *Config) Save(path string) error`

`/root` is UBI on NAND. A torn write during a power cut would leave an unparseable config and a board that fails to start. Write to a temp file in the same directory, fsync, then rename — rename is atomic within a filesystem, so a reader sees either the old file or the new one, never a partial one.

- [ ] **Step 1: Write the failing test**

```go
func TestSaveRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")

	c := &Config{}
	c.Location.Timezone = "America/Chicago"
	c.Location.Latitude = 38.5
	c.WiFi.SSID = "HomeNet"
	c.Display.Brightness = 120
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
	if got.Display.Brightness != 120 {
		t.Errorf("Brightness = %d, want 120", got.Display.Brightness)
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
```

```go
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
```

Add `"path/filepath"` and `"strings"` to the test imports.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/config/ -run TestSave`
Expected: FAIL — `c.Save undefined`

- [ ] **Step 3: Implement Save**

```go
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
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	// Keep the previous version. Ignore a missing original (first save).
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, path+".bak"); err != nil {
			return fmt.Errorf("back up config: %w", err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}
```

Add `"path/filepath"` to the imports in `config.go`.

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 ./internal/config/ -v -run TestSave`
Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/config
git add internal/config
git commit -m "feat: atomic config save with backup

Temp file, fsync, rename -- /root is UBI on NAND and a torn write would leave
a board that cannot parse its config at boot."
```

---

### Task 4: Admin password

**Files:**
- Create: `internal/portal/auth.go`
- Create: `internal/portal/auth_test.go`

**Interfaces:**
- Consumes: `config.PortalConfig` (Task 2)
- Produces:
  - `func DefaultPassword(mac string) string` — last 6 hex of the MAC, lowercase
  - `func HashPassword(pw string) string`
  - `func CheckPassword(pw, hash, mac string) bool`

The spec's rule: default is the last 6 hex of the WiFi MAC (`54:01:4a:4c:1b:fd` → `4c1bfd`), shown on the panel while unconfigured. Once the user sets their own, the MAC default stops working.

`golang.org/x/crypto` is **not available** and no new modules may be added, so this uses `crypto/sha256` with `crypto/subtle` for comparison. That is weaker than bcrypt against offline attack — acceptable here because the threat model is a houseguest on the LAN, not a stolen hash file, and the spec states this tradeoff explicitly.

- [ ] **Step 1: Write the failing test**

```go
package portal

import "testing"

func TestDefaultPasswordFromMAC(t *testing.T) {
	got := DefaultPassword("54:01:4a:4c:1b:fd")
	if got != "4c1bfd" {
		t.Errorf("DefaultPassword = %q, want 4c1bfd", got)
	}
}

func TestDefaultPasswordNormalizes(t *testing.T) {
	// Uppercase, and a dash-separated form some tools emit.
	for _, mac := range []string{"54:01:4A:4C:1B:FD", "54-01-4a-4c-1b-fd"} {
		if got := DefaultPassword(mac); got != "4c1bfd" {
			t.Errorf("DefaultPassword(%q) = %q, want 4c1bfd", mac, got)
		}
	}
}

func TestDefaultPasswordEmptyMAC(t *testing.T) {
	// No MAC (driver not loaded) must not panic or return a guessable constant.
	if got := DefaultPassword(""); got != "" {
		t.Errorf("DefaultPassword(\"\") = %q, want empty", got)
	}
}

func TestCheckPasswordUsesMACWhenNoHashSet(t *testing.T) {
	mac := "54:01:4a:4c:1b:fd"
	if !CheckPassword("4c1bfd", "", mac) {
		t.Error("MAC default should be accepted when no hash is set")
	}
	if CheckPassword("wrong", "", mac) {
		t.Error("wrong password accepted against the MAC default")
	}
}

func TestCheckPasswordUsesHashWhenSet(t *testing.T) {
	mac := "54:01:4a:4c:1b:fd"
	h := HashPassword("chosen-by-user")

	if !CheckPassword("chosen-by-user", h, mac) {
		t.Error("the configured password should be accepted")
	}
	// Once a password is set, the MAC default must stop working.
	if CheckPassword("4c1bfd", h, mac) {
		t.Error("MAC default still accepted after a password was set")
	}
}

func TestHashIsNotPlaintext(t *testing.T) {
	h := HashPassword("hunter2")
	if h == "hunter2" || h == "" {
		t.Errorf("hash = %q, must be neither the plaintext nor empty", h)
	}
	if HashPassword("hunter2") != h {
		t.Error("hashing must be deterministic")
	}
}

func TestEmptyPasswordNeverAuthenticates(t *testing.T) {
	mac := "54:01:4a:4c:1b:fd"
	if CheckPassword("", "", mac) {
		t.Error("empty password accepted against the MAC default")
	}
	if CheckPassword("", HashPassword("x"), mac) {
		t.Error("empty password accepted against a set hash")
	}
	// And with no MAC available either -- nothing should authenticate.
	if CheckPassword("", "", "") {
		t.Error("empty password accepted with no MAC and no hash")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/portal/`
Expected: FAIL — `undefined: DefaultPassword`

- [ ] **Step 3: Implement auth.go**

```go
// Package portal serves the configuration UI, in AP mode and on the LAN.
package portal

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// DefaultPassword derives the shipped admin password from the WiFi MAC: the
// last six hex digits, lowercase, no separators. It is per-device rather than a
// shared constant, and it is shown on the panel while the board is unconfigured
// so first-run needs no documentation. Returns "" when the MAC is unavailable
// (driver not loaded), which never authenticates -- see CheckPassword.
func DefaultPassword(mac string) string {
	clean := strings.ToLower(mac)
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	if len(clean) < 6 {
		return ""
	}
	return clean[len(clean)-6:]
}

// HashPassword hashes an admin password for storage in config.json.
//
// This is a bare SHA-256, not bcrypt/scrypt/argon2: golang.org/x/crypto is not
// a dependency of this project and no new modules may be added. That is weaker
// against offline attack on a stolen config file, and acceptable here -- the
// threat this guards against is a houseguest on the LAN reconfiguring a wall
// display, not a credential-stuffing campaign.
func HashPassword(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// CheckPassword reports whether pw is correct. With no hash configured it
// accepts the MAC-derived default; once a hash is set the default stops
// working. An empty password never authenticates, whatever the state.
func CheckPassword(pw, hash, mac string) bool {
	if pw == "" {
		return false
	}
	if hash == "" {
		def := DefaultPassword(mac)
		if def == "" {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(pw), []byte(def)) == 1
	}
	return subtle.ConstantTimeCompare([]byte(HashPassword(pw)), []byte(hash)) == 1
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 ./internal/portal/ -v`
Expected: PASS for all seven.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/portal
git add internal/portal
git commit -m "feat: portal admin password

Defaults to the last 6 hex of the WiFi MAC (shown on the panel while
unconfigured); a user-set password replaces it. SHA-256 + constant-time
compare rather than bcrypt because x/crypto is not a dependency -- the
tradeoff is stated in the design."
```

---

### Task 5: WiFi command runner and status

**Files:**
- Create: `internal/wifi/runner.go`
- Create: `internal/wifi/wifi.go`
- Create: `internal/wifi/wifi_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type Runner interface { Run(name string, args ...string) ([]byte, error) }`
  - `type ExecRunner struct{}` — the real one
  - `type Client struct { R Runner }`
  - `func (c *Client) Status() (Status, error)`
  - `func (c *Client) Scan() ([]Network, error)`
  - `type Status struct { State, SSID, IP string; Connected bool }`
  - `type Network struct { SSID string; Signal int; Secured bool }`

Every wifi operation goes through `Runner` so tests never touch hardware. This matters more here than elsewhere: a wifi test that shells out for real could strand the board.

- [ ] **Step 1: Write the failing test**

```go
package wifi

import (
	"errors"
	"testing"
)

// fakeRunner returns canned output per command, and records what was called.
type fakeRunner struct {
	out  map[string][]byte
	err  map[string]error
	call []string
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	f.call = append(f.call, key)
	if e, ok := f.err[key]; ok {
		return nil, e
	}
	return f.out[key], nil
}

// Captured from the real board.
const statusConnected = `bssid=fc:ec:da:f1:e7:e0
freq=2462
ssid=Argosity
id=0
mode=station
wpa_state=COMPLETED
ip_address=192.168.1.80
key_mgmt=WPA2-PSK
`

const statusScanning = `wpa_state=SCANNING
`

const scanResults = `bssid / frequency / signal level / flags / ssid
86:25:19:98:5a:b4	2462	-69	[WPA2-PSK-CCMP][ESS]	DIRECT-eGM2020 Series
fc:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]	Argosity
fc:ec:da:f1:ea:a0	2437	-77	[WPA2-PSK-CCMP][ESS]	Argosity
02:ec:da:f1:e7:e0	2462	-72	[ESS]	OpenNet
`

func TestStatusConnected(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 status": []byte(statusConnected),
	}}}
	got, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Connected {
		t.Error("Connected = false, want true for wpa_state=COMPLETED")
	}
	if got.SSID != "Argosity" {
		t.Errorf("SSID = %q, want Argosity", got.SSID)
	}
	if got.IP != "192.168.1.80" {
		t.Errorf("IP = %q", got.IP)
	}
}

func TestStatusScanningIsNotConnected(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 status": []byte(statusScanning),
	}}}
	got, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.Connected {
		t.Error("Connected = true for wpa_state=SCANNING")
	}
	if got.SSID != "" {
		t.Errorf("SSID = %q, want empty while scanning", got.SSID)
	}
}

func TestStatusErrorPropagates(t *testing.T) {
	c := &Client{R: &fakeRunner{err: map[string]error{
		"wpa_cli -i wlan0 status": errors.New("no such device"),
	}}}
	if _, err := c.Status(); err == nil {
		t.Fatal("expected an error when wpa_cli fails")
	}
}

func TestScanParsesAndDedupes(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(scanResults),
	}}}
	nets, err := c.Scan()
	if err != nil {
		t.Fatal(err)
	}
	// Argosity appears twice (two APs); the picker should list it once.
	var argosity int
	for _, n := range nets {
		if n.SSID == "Argosity" {
			argosity++
		}
	}
	if argosity != 1 {
		t.Errorf("Argosity listed %d times, want 1 (deduped)", argosity)
	}
	if len(nets) != 3 {
		t.Errorf("got %d networks, want 3 unique SSIDs", len(nets))
	}
}

func TestScanKeepsStrongestOfDuplicates(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(scanResults),
	}}}
	nets, _ := c.Scan()
	for _, n := range nets {
		if n.SSID == "Argosity" && n.Signal != -72 {
			t.Errorf("Argosity signal = %d, want the stronger -72", n.Signal)
		}
	}
}

func TestScanMarksOpenNetworks(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(scanResults),
	}}}
	nets, _ := c.Scan()
	for _, n := range nets {
		switch n.SSID {
		case "OpenNet":
			if n.Secured {
				t.Error("OpenNet has no [WPA...] flag; Secured should be false")
			}
		case "Argosity":
			if !n.Secured {
				t.Error("Argosity is WPA2-PSK; Secured should be true")
			}
		}
	}
}

func TestScanSkipsHiddenSSIDs(t *testing.T) {
	const withHidden = `bssid / frequency / signal level / flags / ssid
02:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]	
fc:ec:da:f1:e7:e0	2462	-70	[WPA2-PSK-CCMP][ESS]	Argosity
`
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(withHidden),
	}}}
	nets, _ := c.Scan()
	if len(nets) != 1 || nets[0].SSID != "Argosity" {
		t.Errorf("hidden (blank) SSID should be skipped, got %+v", nets)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/wifi/`
Expected: FAIL — `undefined: Client`

- [ ] **Step 3: Implement runner.go**

```go
// Package wifi wraps the board's wpa_cli/hostapd/dnsmasq tooling.
//
// Every command goes through Runner so tests never touch the hardware. That is
// a hard requirement here rather than a nicety: a test that shelled out for
// real could tear down wlan0, and adb rides the same USB/WiFi stack -- taking
// out the interface can strand the board entirely.
package wifi

import "os/exec"

// Runner executes a command and returns its combined output.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands for real. Used on the board.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
```

- [ ] **Step 4: Implement wifi.go**

```go
package wifi

import (
	"fmt"
	"strconv"
	"strings"
)

// Iface is the wireless interface. The board has exactly one.
const Iface = "wlan0"

// Status is the current association state.
type Status struct {
	State     string // raw wpa_state, e.g. COMPLETED, SCANNING
	SSID      string
	IP        string
	Connected bool
}

// Network is one SSID seen in a scan.
type Network struct {
	SSID    string
	Signal  int  // dBm, closer to zero is stronger
	Secured bool
}

// Client talks to the board's wifi tooling.
type Client struct {
	R Runner
}

// Status reports the current association.
func (c *Client) Status() (Status, error) {
	out, err := c.R.Run("wpa_cli", "-i", Iface, "status")
	if err != nil {
		return Status{}, fmt.Errorf("wpa_cli status: %w", err)
	}
	var s Status
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "wpa_state":
			s.State = v
		case "ssid":
			s.SSID = v
		case "ip_address":
			s.IP = v
		}
	}
	s.Connected = s.State == "COMPLETED"
	// wpa_cli reports a stale ssid while re-scanning; only claim one when
	// actually associated, so the UI never shows a network we are not on.
	if !s.Connected {
		s.SSID = ""
	}
	return s, nil
}

// Scan returns the visible networks, strongest first, one entry per SSID.
//
// This parses `scan_results` rather than using `iw`, which is not on the image.
// It does not trigger a fresh scan -- wpa_supplicant scans on its own while
// unassociated, which is exactly when the picker is used.
func (c *Client) Scan() ([]Network, error) {
	out, err := c.R.Run("wpa_cli", "-i", Iface, "scan_results")
	if err != nil {
		return nil, fmt.Errorf("wpa_cli scan_results: %w", err)
	}

	best := map[string]Network{}
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // header or blank
		}
		// bssid \t freq \t signal \t flags \t ssid
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		ssid := strings.TrimSpace(f[4])
		if ssid == "" {
			continue // hidden network: nothing to show or type
		}
		sig, err := strconv.Atoi(strings.TrimSpace(f[2]))
		if err != nil {
			continue
		}
		n := Network{
			SSID:    ssid,
			Signal:  sig,
			Secured: strings.Contains(f[3], "WPA") || strings.Contains(f[3], "WEP"),
		}
		// Same SSID on several APs: keep the strongest.
		if prev, seen := best[ssid]; !seen || n.Signal > prev.Signal {
			best[ssid] = n
		}
	}

	nets := make([]Network, 0, len(best))
	for _, n := range best {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].Signal > nets[j].Signal })
	return nets, nil
}
```

Add `"sort"` to the imports.

- [ ] **Step 5: Run the tests**

Run: `go test -count=1 ./internal/wifi/ -v`
Expected: PASS for all seven.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/wifi
git add internal/wifi
git commit -m "feat: wifi status and scan behind a command runner

Runner is an interface so tests never shell out for real -- a wifi test that
touched the hardware could tear down wlan0, and adb rides the same stack."
```

---

### Task 6: Associate with a network

**Files:**
- Modify: `internal/wifi/wifi.go`
- Modify: `internal/wifi/wifi_test.go`

**Interfaces:**
- Consumes: `Client` (Task 5)
- Produces: `func (c *Client) Connect(ssid, password string) error`

Writes `/etc/wpa_supplicant.conf` and restarts the supplicant. The existing `wifi-connect.sh` templates into `/tmp` (tmpfs), which does not survive reboot — that bug cost a debugging cycle already. This writes the persistent file directly.

- [ ] **Step 1: Write the failing test**

```go
func TestConnectWritesConfigAndRestarts(t *testing.T) {
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, ConfPath: t.TempDir() + "/wpa_supplicant.conf"}

	if err := c.Connect("HomeNet", "s3cret"); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(c.ConfPath)
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	if !strings.Contains(conf, `ssid="HomeNet"`) {
		t.Errorf("config missing ssid:\n%s", conf)
	}
	if !strings.Contains(conf, `psk="s3cret"`) {
		t.Errorf("config missing psk:\n%s", conf)
	}
	if !strings.Contains(conf, "ctrl_interface=") {
		t.Errorf("config missing ctrl_interface, wpa_cli will not work:\n%s", conf)
	}

	// It must actually restart the supplicant, or nothing takes effect.
	var restarted bool
	for _, call := range f.call {
		if strings.Contains(call, "wpa_supplicant") || strings.Contains(call, "S99wlan0") {
			restarted = true
		}
	}
	if !restarted {
		t.Errorf("no restart issued; calls were %v", f.call)
	}
}

func TestConnectRejectsEmptySSID(t *testing.T) {
	c := &Client{R: &fakeRunner{}, ConfPath: t.TempDir() + "/w.conf"}
	if err := c.Connect("", "pw"); err == nil {
		t.Error("expected an error for an empty SSID")
	}
}

func TestConnectEscapesQuotes(t *testing.T) {
	// A quote in the SSID would otherwise break out of the quoted value and
	// corrupt the config file.
	c := &Client{R: &fakeRunner{}, ConfPath: t.TempDir() + "/w.conf"}
	if err := c.Connect(`My"Net`, `pa"ss`); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.ConfPath)
	conf := string(b)
	if strings.Contains(conf, `ssid="My"Net"`) {
		t.Errorf("unescaped quote corrupts the config:\n%s", conf)
	}
	if !strings.Contains(conf, `\"`) {
		t.Errorf("expected escaped quotes:\n%s", conf)
	}
}
```

Add `"os"` and `"strings"` to the test imports.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/wifi/ -run TestConnect`
Expected: FAIL — `unknown field ConfPath`

- [ ] **Step 3: Implement Connect**

Add `ConfPath` to `Client`:

```go
// Client talks to the board's wifi tooling.
type Client struct {
	R Runner
	// ConfPath is the persistent supplicant config. Defaults to
	// /etc/wpa_supplicant.conf when empty.
	ConfPath string
}

func (c *Client) confPath() string {
	if c.ConfPath == "" {
		return "/etc/wpa_supplicant.conf"
	}
	return c.ConfPath
}
```

Then:

```go
// wpaEscape quotes a value for wpa_supplicant.conf. Without this an SSID or
// password containing a quote would terminate the value early and corrupt the
// file -- leaving a board that cannot associate and cannot be reached to fix.
func wpaEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// Connect writes the supplicant config and restarts the supplicant.
//
// It writes the persistent /etc/wpa_supplicant.conf rather than the /tmp copy
// the vendor's wifi-connect.sh uses -- /tmp is tmpfs, so credentials written
// there are lost on reboot.
func (c *Client) Connect(ssid, password string) error {
	if strings.TrimSpace(ssid) == "" {
		return fmt.Errorf("ssid is required")
	}

	conf := fmt.Sprintf(`ctrl_interface=/var/run/wpa_supplicant
ap_scan=1
update_config=1

network={
	ssid="%s"
	psk="%s"
	key_mgmt=WPA-PSK
}
`, wpaEscape(ssid), wpaEscape(password))

	// Written via a temp file + rename for the same reason config.Save is:
	// a torn write here leaves a board that cannot get back on the network.
	path := c.confPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(conf), 0o600); err != nil {
		return fmt.Errorf("write supplicant config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install supplicant config: %w", err)
	}

	// S99wlan0 restart re-runs the whole bring-up: supplicant, DHCP, and the
	// clock sync. Simpler and more reliable than driving wpa_cli reconfigure
	// and udhcpc separately.
	if out, err := c.R.Run("/etc/init.d/S99wlan0", "restart"); err != nil {
		return fmt.Errorf("restart networking: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
```

Add `"os"` to the imports in `wifi.go`.

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 ./internal/wifi/ -v`
Expected: PASS for all ten (seven from Task 5, three new).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/wifi
git add internal/wifi
git commit -m "feat: associate with a network

Writes the persistent /etc/wpa_supplicant.conf, not the /tmp copy the vendor
script uses -- tmpfs loses credentials on reboot. Escapes quotes so an SSID
containing one cannot corrupt the file."
```

---

### Task 7: AP mode lifecycle

**Files:**
- Create: `internal/wifi/ap.go`
- Create: `internal/wifi/ap_test.go`

**Interfaces:**
- Consumes: `Runner` (Task 5)
- Produces:
  - `func APName(mac string) string` — `upnext-<4 hex>`
  - `func (c *Client) StartAP(mac string) error`
  - `func (c *Client) StopAP() error`

AP name comes from the MAC's last two octets (`54:01:4a:4c:1b:fd` → `upnext-1bfd`) so it is stable across reboots. `dnsmasq` answers every DNS query with the portal address — with no `iptables` on this image, wildcard DNS is the entire captive-portal redirect.

**Only start this task if Task 1 confirmed AP mode works.**

- [ ] **Step 1: Write the failing test**

```go
func TestAPNameFromMAC(t *testing.T) {
	if got := APName("54:01:4a:4c:1b:fd"); got != "upnext-1bfd" {
		t.Errorf("APName = %q, want upnext-1bfd", got)
	}
}

func TestAPNameStableAcrossFormats(t *testing.T) {
	for _, mac := range []string{"54:01:4A:4C:1B:FD", "54-01-4a-4c-1b-fd"} {
		if got := APName(mac); got != "upnext-1bfd" {
			t.Errorf("APName(%q) = %q, want upnext-1bfd", mac, got)
		}
	}
}

func TestAPNameFallsBackWithoutMAC(t *testing.T) {
	// Must still produce a joinable name rather than "upnext-".
	got := APName("")
	if got == "upnext-" || got == "" {
		t.Errorf("APName(\"\") = %q, want a usable fallback", got)
	}
}

func TestStartAPWritesConfigsAndStartsBoth(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, HostapdConf: dir + "/hostapd.conf", DnsmasqConf: dir + "/dnsmasq-ap.conf"}

	if err := c.StartAP("54:01:4a:4c:1b:fd"); err != nil {
		t.Fatal(err)
	}

	hb, err := os.ReadFile(c.HostapdConf)
	if err != nil {
		t.Fatal(err)
	}
	h := string(hb)
	if !strings.Contains(h, "ssid=upnext-1bfd") {
		t.Errorf("hostapd.conf missing ssid:\n%s", h)
	}
	if !strings.Contains(h, "interface=wlan0") {
		t.Errorf("hostapd.conf missing interface:\n%s", h)
	}

	db, err := os.ReadFile(c.DnsmasqConf)
	if err != nil {
		t.Fatal(err)
	}
	d := string(db)
	// The wildcard DNS entry IS the captive-portal redirect; there is no
	// iptables on this image to do it any other way.
	if !strings.Contains(d, "address=/#/192.168.4.1") {
		t.Errorf("dnsmasq config missing the wildcard redirect:\n%s", d)
	}
	if !strings.Contains(d, "dhcp-range=") {
		t.Errorf("dnsmasq config missing dhcp-range; clients get no address:\n%s", d)
	}

	var startedHostapd, startedDnsmasq, addressed bool
	for _, call := range f.call {
		switch {
		case strings.Contains(call, "hostapd"):
			startedHostapd = true
		case strings.Contains(call, "dnsmasq"):
			startedDnsmasq = true
		case strings.Contains(call, "192.168.4.1"):
			addressed = true
		}
	}
	if !startedHostapd || !startedDnsmasq {
		t.Errorf("both daemons must start; calls were %v", f.call)
	}
	if !addressed {
		t.Errorf("wlan0 must get 192.168.4.1; calls were %v", f.call)
	}
}

func TestStopAPKillsBoth(t *testing.T) {
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f}
	if err := c.StopAP(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.call, " | ")
	if !strings.Contains(joined, "hostapd") || !strings.Contains(joined, "dnsmasq") {
		t.Errorf("StopAP must stop both daemons; calls were %v", f.call)
	}
}

func TestStopAPIgnoresNotRunning(t *testing.T) {
	// killall exits non-zero when nothing matches. Stopping an AP that is not
	// running is not an error -- it is the normal path on every boot.
	f := &fakeRunner{err: map[string]error{
		"killall hostapd": errors.New("exit status 1"),
		"killall dnsmasq": errors.New("exit status 1"),
	}}
	c := &Client{R: f}
	if err := c.StopAP(); err != nil {
		t.Errorf("StopAP should tolerate daemons that are not running: %v", err)
	}
}
```

Add `"errors"`, `"os"`, and `"strings"` to the test imports.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/wifi/ -run 'TestAP|TestStartAP|TestStopAP'`
Expected: FAIL — `undefined: APName`

- [ ] **Step 3: Implement ap.go**

```go
package wifi

import (
	"fmt"
	"os"
	"strings"
)

// APAddr is the board's address in AP mode, and the portal's address there.
const APAddr = "192.168.4.1"

// APName derives the access point name from the WiFi MAC's last two octets, so
// a returning user sees the same network name every time.
func APName(mac string) string {
	clean := strings.ToLower(mac)
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	if len(clean) < 4 {
		// No MAC available. "setup" is still joinable and still unambiguous
		// on a network that has exactly one of these boards on it.
		return "upnext-setup"
	}
	return "upnext-" + clean[len(clean)-4:]
}

func (c *Client) hostapdConf() string {
	if c.HostapdConf == "" {
		return "/tmp/hostapd.conf"
	}
	return c.HostapdConf
}

func (c *Client) dnsmasqConf() string {
	if c.DnsmasqConf == "" {
		return "/tmp/dnsmasq-ap.conf"
	}
	return c.DnsmasqConf
}

// StartAP brings up the setup access point: an open network named upnext-<hex>
// serving DHCP and answering every DNS query with the portal's address.
//
// The AP is deliberately open. A password on the setup network would need to be
// communicated somehow, and the thing it would protect is a form that already
// requires the admin password. Joining it grants nothing but the portal login.
func (c *Client) StartAP(mac string) error {
	// Stop the supplicant first: it and hostapd cannot both own wlan0.
	_, _ = c.R.Run("killall", "wpa_supplicant")

	hostapd := fmt.Sprintf(`interface=%s
driver=nl80211
ssid=%s
hw_mode=g
channel=6
auth_algs=1
wmm_enabled=0
`, Iface, APName(mac))
	if err := os.WriteFile(c.hostapdConf(), []byte(hostapd), 0o644); err != nil {
		return fmt.Errorf("write hostapd config: %w", err)
	}

	// address=/#/ answers EVERY name with the portal address. That is what
	// makes a phone show its "sign in to network" sheet, and with no iptables
	// on this image it is the whole redirect mechanism.
	dnsmasq := fmt.Sprintf(`interface=%s
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,12h
address=/#/%s
no-resolv
`, Iface, APAddr)
	if err := os.WriteFile(c.dnsmasqConf(), []byte(dnsmasq), 0o644); err != nil {
		return fmt.Errorf("write dnsmasq config: %w", err)
	}

	if out, err := c.R.Run("ip", "addr", "add", APAddr+"/24", "dev", Iface); err != nil {
		// Already assigned is fine; anything else is not.
		if !strings.Contains(string(out), "File exists") {
			return fmt.Errorf("assign %s: %w (%s)", APAddr, err, strings.TrimSpace(string(out)))
		}
	}
	if out, err := c.R.Run("ip", "link", "set", Iface, "up"); err != nil {
		return fmt.Errorf("bring up %s: %w (%s)", Iface, err, strings.TrimSpace(string(out)))
	}
	if out, err := c.R.Run("hostapd", "-B", c.hostapdConf()); err != nil {
		return fmt.Errorf("start hostapd: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := c.R.Run("dnsmasq", "-C", c.dnsmasqConf()); err != nil {
		return fmt.Errorf("start dnsmasq: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopAP tears the access point down. Stopping one that is not running is not
// an error -- that is the normal path on a board that booted straight into STA.
func (c *Client) StopAP() error {
	_, _ = c.R.Run("killall", "hostapd")
	_, _ = c.R.Run("killall", "dnsmasq")
	_, _ = c.R.Run("ip", "addr", "del", APAddr+"/24", "dev", Iface)
	return nil
}
```

Add the two config-path fields to `Client` in `wifi.go`:

```go
	// HostapdConf and DnsmasqConf default to /tmp paths when empty; tests
	// point them at a temp dir.
	HostapdConf string
	DnsmasqConf string
```

- [ ] **Step 4: Run the tests**

Run: `go test -count=1 ./internal/wifi/ -v`
Expected: PASS for all sixteen.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/wifi
git add internal/wifi
git commit -m "feat: AP mode lifecycle

upnext-<4hex> from the MAC so the name is stable across reboots. dnsmasq
answers every DNS query with 192.168.4.1 -- with no iptables on this image
that wildcard IS the captive-portal redirect."
```

---

### Task 8: Config behind the Store mutex

**Files:**
- Modify: `cmd/dashboard/loop.go`
- Modify: `cmd/dashboard/loop_test.go`
- Modify: `cmd/dashboard/main.go`

**Interfaces:**
- Consumes: `config.Config` (Task 2)
- Produces:
  - `func (s *Store) Config() *config.Config`
  - `func (s *Store) SetConfig(c *config.Config)`

Today `cfg` is loaded once and captured by the render loop and both fetchers. The portal needs to replace it while they are running. `Store` already owns the mutex pattern for weather and events; config joins it.

The pointer is **replaced, never mutated**, so a reader holds a consistent snapshot for a whole tick.

- [ ] **Step 1: Write the failing test**

```go
func TestStoreConfigSwap(t *testing.T) {
	s := &Store{}
	first := &config.Config{}
	first.Location.Timezone = "UTC"
	s.SetConfig(first)

	if got := s.Config(); got.Location.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", got.Location.Timezone)
	}

	second := &config.Config{}
	second.Location.Timezone = "America/Chicago"
	s.SetConfig(second)

	if got := s.Config(); got.Location.Timezone != "America/Chicago" {
		t.Errorf("Timezone = %q, want the swapped value", got.Location.Timezone)
	}
}

func TestStoreConfigSnapshotIsStable(t *testing.T) {
	// A reader that grabbed the pointer must keep seeing its own snapshot even
	// if the portal swaps in a new config mid-tick.
	s := &Store{}
	first := &config.Config{}
	first.Location.Timezone = "UTC"
	s.SetConfig(first)

	held := s.Config()
	swapped := &config.Config{}
	swapped.Location.Timezone = "America/Chicago"
	s.SetConfig(swapped)

	if held.Location.Timezone != "UTC" {
		t.Error("a held snapshot changed underneath the reader")
	}
}

func TestStoreConfigRace(t *testing.T) {
	// Run with -race: concurrent readers and a writer must not race.
	s := &Store{}
	s.SetConfig(&config.Config{})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c := &config.Config{}
			c.Agenda.MaxEvents = i
			s.SetConfig(c)
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = s.Config().Agenda.MaxEvents
	}
	<-done
}
```

Add `"github.com/nathanstitt/luckfox-dashboard/internal/config"` to the test imports.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/dashboard/ -run TestStoreConfig`
Expected: FAIL — `s.SetConfig undefined`

- [ ] **Step 3: Add cfg to Store**

In `cmd/dashboard/loop.go`, add the field and accessors:

```go
type Store struct {
	mu      sync.RWMutex
	cfg     *config.Config
	events  []calendar.Event
	weather *weather.Weather
	evErrs  []string
	wxErrs  []string
}

// Config returns the current configuration. The pointer is replaced rather
// than mutated on save, so the returned value is a stable snapshot: a reader
// keeps seeing its own version even if the portal swaps in a new one mid-tick.
func (s *Store) Config() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetConfig installs a new configuration. Called by the portal after a
// successful save; the next tick renders with it.
func (s *Store) SetConfig(c *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
}
```

Add the config import to `loop.go`.

- [ ] **Step 4: Read config from the Store in main.go**

In `cmd/dashboard/main.go`, after loading the config, install it:

```go
	store := &Store{}
	store.SetConfig(cfg)
```

Then change the five call sites that take `cfg` to read from the store instead. In the render loop:

```go
	for {
		time.Sleep(nextTick(time.Now()))
		if err := renderSafely(store.Config(), store, *fbDev, fbW, fbH, *htmlOut); err != nil {
			log.Printf("render: %v", err)
		}
	}
```

In `fetchLoop`, read per iteration so an interval change takes effect:

```go
func fetchLoop(store *Store, interval time.Duration, fn fetchFunc) {
	for {
		cfg := store.Config()
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		ok := fn(ctx, cfg, store)
		cancel()

		wait := interval
		if !ok {
			wait = retryDelay
		}
		time.Sleep(wait)
	}
}
```

Update both `go fetchLoop(...)` calls to drop the `cfg` argument, and the `--once` path to use `store.Config()`.

- [ ] **Step 5: Run everything, including the race detector**

Run: `go test -count=1 ./... && go test -race -count=1 ./cmd/dashboard/`
Expected: PASS, no races reported.

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/dashboard
git add cmd/dashboard
git commit -m "feat: config lives behind the Store mutex

The portal runs in this process and needs to replace the config while the
render loop and fetchers are running. The pointer is swapped, never mutated,
so readers keep a consistent snapshot for a whole tick."
```

---

### Task 9: Portal server, auth, and the settings page

**Files:**
- Create: `internal/portal/server.go`
- Create: `internal/portal/handlers.go`
- Create: `internal/portal/render.go`
- Create: `internal/portal/templates/layout.html`
- Create: `internal/portal/templates/settings.html`
- Create: `internal/portal/assets/portal.css`
- Create: `internal/portal/server_test.go`

**Interfaces:**
- Consumes: `config` (Tasks 2–3), `auth` (Task 4), `wifi.Client` (Tasks 5–7), `Store` — passed as an interface so `portal` does not import `main`
- Produces:
  - `type ConfigStore interface { Config() *config.Config; SetConfig(*config.Config) }`
  - `type Server struct { Store ConfigStore; WiFi *wifi.Client; MAC, ConfigPath string }`
  - `func (s *Server) Handler() http.Handler`

`portal` must not import `cmd/dashboard`, so it declares the narrow `ConfigStore` interface it needs and `*Store` satisfies it structurally.

**Design constraints from the spec, applied here:**
- Palette exactly as listed in Global Constraints. `#ff7a59` appears **only** on errors.
- The signature: every settings row is a 380px label column, a `#263041` hairline, then the field — the panel's own 380:1540 proportion. On narrow phones the columns stack and the rule turns horizontal.
- **No numbered steps** on the settings page; these are independent settings, not a sequence.
- Copy names things the owner recognises: "Network", "Calendar feed", "Where you are" — never `wpa_supplicant.conf` or `location.latitude`.
- Forms work without JavaScript. A captive-portal sheet may be an old WebView.

- [ ] **Step 1: Write the failing test**

```go
package portal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

type fakeStore struct{ c *config.Config }

func (f *fakeStore) Config() *config.Config     { return f.c }
func (f *fakeStore) SetConfig(c *config.Config) { f.c = c }

func newTestServer(t *testing.T) *Server {
	t.Helper()
	c := &config.Config{}
	c.Location.Timezone = "America/Chicago"
	c.Display.Brightness = 200
	c.Calendars = []config.CalendarSource{{Name: "Work", Color: "#4f9cff", URL: "https://example.com/w.ics"}}
	return &Server{
		Store:      &fakeStore{c: c},
		MAC:        "54:01:4a:4c:1b:fd",
		ConfigPath: t.TempDir() + "/config.json",
	}
}

func TestUnauthenticatedIsChallenged(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge: %q", w.Header().Get("WWW-Authenticate"))
	}
}

func TestMACPasswordAuthenticates(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "4c1bfd") // last 6 hex of the MAC
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestWrongPasswordRejected(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "nope")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestSettingsPageShowsCurrentValues(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{"America/Chicago", "Work", "https://example.com/w.ics"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
}

func TestSettingsPageEscapesCalendarNames(t *testing.T) {
	s := newTestServer(t)
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{{Name: `<script>alert(1)</script>`}},
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "<script>alert(1)</script>") {
		t.Error("calendar name was not escaped")
	}
}

func TestSaveDisplayUpdatesConfigAndPersists(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/display",
		strings.NewReader("brightness=90&clock_24h=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 (post-redirect-get)", w.Code)
	}
	got := s.Store.Config()
	if got.Display.Brightness != 90 {
		t.Errorf("Brightness = %d, want 90", got.Display.Brightness)
	}
	if !got.Units.Clock24h {
		t.Error("Clock24h = false, want true")
	}
	// It must reach disk, not just memory -- a reboot would lose it otherwise.
	saved, err := config.Load(s.ConfigPath)
	if err != nil {
		t.Fatalf("config was not written: %v", err)
	}
	if saved.Display.Brightness != 90 {
		t.Errorf("saved Brightness = %d, want 90", saved.Display.Brightness)
	}
}

func TestSaveRejectsInvalidBrightness(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/display",
		strings.NewReader("brightness=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if s.Store.Config().Display.Brightness != 200 {
		t.Error("invalid input must not change the stored config")
	}
}

func TestCSSIsServed(t *testing.T) {
	s := newTestServer(t)
	// The stylesheet must not require auth, or an unauthenticated 401 page
	// renders unstyled.
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/portal.css", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "#0b0d12") {
		t.Error("stylesheet does not contain the ground colour")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/portal/ -run 'TestUnauth|TestMAC|TestSettings|TestSave|TestCSS'`
Expected: FAIL — `undefined: Server`

- [ ] **Step 3: Write the stylesheet**

Create `internal/portal/assets/portal.css`:

```css
/* The portal inherits the dashboard's palette so the two surfaces read as one
   product. Literal hex throughout -- this file is served to a browser, but the
   values are kept in sync with the panel's stylesheet by hand. */
:root {
  --bg: #0b0d12;
  --raised: #141924;
  --rule: #263041;
  --text: #e8ecf3;
  --dim: #8b97ab;
  --accent: #4f9cff;
  --alarm: #ff7a59;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--bg);
  color: var(--text);
  font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif;
  -webkit-text-size-adjust: 100%;
}

.wrap { max-width: 420px; margin: 0 auto; padding: 0 20px 64px; }

/* Status strip: answers "is it working?" before any form appears, because that
   is the question that brought someone here. */
.status {
  display: flex; align-items: baseline; gap: 10px;
  padding: 20px 0 18px;
  border-bottom: 1px solid var(--rule);
}
.status .dot { width: 9px; height: 9px; border-radius: 50%; background: var(--accent); flex: none; }
.status.down .dot { background: var(--alarm); }
.status .what { font-weight: 600; letter-spacing: -0.01em; }
.status .where { color: var(--dim); font-size: 14px; font-family: ui-monospace, "SF Mono", Menlo, monospace; }

h1 {
  font-family: "Helvetica Neue", Inter, system-ui, sans-serif;
  font-size: 15px; font-weight: 700;
  text-transform: uppercase; letter-spacing: 0.14em;
  color: var(--dim);
  margin: 32px 0 0;
}

/* THE SIGNATURE: the panel is 1920x480 with a fixed 380px block and a hairline
   divider. Every row repeats that 380:1540 proportion with the same rule
   colour, so scrolling the portal echoes the geometry of the thing being
   configured. */
.row {
  display: grid;
  grid-template-columns: 98px 1px 1fr;   /* 380:1540 of a 420px column */
  gap: 0 16px;
  align-items: start;
  padding: 16px 0;
  border-bottom: 1px solid var(--rule);
}
.row > .rule { background: var(--rule); align-self: stretch; }
.row > label:first-child {
  font-size: 12px; font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.1em;
  color: var(--dim);
  padding-top: 10px;
}
.row .field { min-width: 0; }

input[type="text"], input[type="url"], input[type="password"], input[type="number"], select {
  width: 100%;
  background: var(--raised);
  border: 1px solid var(--rule);
  border-radius: 6px;
  color: var(--text);
  font: inherit;
  padding: 10px 12px;
}
input:focus, select:focus, button:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
input[type="range"] { width: 100%; accent-color: var(--accent); }

.hint { color: var(--dim); font-size: 13px; margin-top: 6px; }
.mono { font-family: ui-monospace, "SF Mono", Menlo, monospace; }

button {
  background: var(--accent);
  color: #06121f;
  border: 0; border-radius: 6px;
  font: 600 15px/1 inherit;
  padding: 13px 18px;
  margin-top: 20px;
  width: 100%;
  cursor: pointer;
}

.error {
  color: var(--alarm);
  border-left: 2px solid var(--alarm);
  padding-left: 10px;
  margin: 12px 0;
  font-size: 14px;
}

.empty { color: var(--dim); font-size: 14px; padding: 4px 0 8px; }

/* On a narrow phone the columns stack and the rule turns horizontal -- the
   label-above-field rhythm survives even though the vertical seam cannot. */
@media (max-width: 380px) {
  .row { grid-template-columns: 1fr; gap: 8px; }
  .row > .rule { height: 1px; align-self: auto; }
  .row > label:first-child { padding-top: 0; }
}

@media (prefers-reduced-motion: reduce) {
  * { transition: none !important; animation: none !important; }
}
```

- [ ] **Step 4: Write the templates**

Create `internal/portal/templates/layout.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>UpNext setup</title>
<link rel="stylesheet" href="/portal.css">
</head>
<body>
<div class="wrap">
  <div class="status{{if not .Status.Connected}} down{{end}}">
    <span class="dot"></span>
    <span class="what">{{if .Status.Connected}}Connected{{else}}Setup mode{{end}}</span>
    <span class="where">{{if .Status.Connected}}{{.Status.SSID}} · {{.Status.IP}}{{else}}{{.APName}}{{end}}</span>
  </div>
  {{template "content" .}}
</div>
</body>
</html>
```

Create `internal/portal/templates/settings.html`:

```html
{{define "content"}}
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}

<h1>Network</h1>
<form method="post" action="/save/wifi">
  <div class="row">
    <label for="ssid">Network</label><span class="rule"></span>
    <div class="field">
      <input id="ssid" name="ssid" type="text" value="{{.Config.WiFi.SSID}}" autocapitalize="off" autocorrect="off">
    </div>
  </div>
  <div class="row">
    <label for="wifipw">Password</label><span class="rule"></span>
    <div class="field">
      <input id="wifipw" name="password" type="password" placeholder="Leave blank to keep current">
    </div>
  </div>
  <button type="submit">Save network</button>
</form>

<h1>Calendars</h1>
<form method="post" action="/save/calendars">
  {{if not .Config.Calendars}}
    <p class="empty">No calendars yet — add a feed to see your agenda.</p>
  {{end}}
  {{range $i, $c := .Config.Calendars}}
  <div class="row">
    <label for="calname{{$i}}">Feed {{$i}}</label><span class="rule"></span>
    <div class="field">
      <input id="calname{{$i}}" name="name" type="text" value="{{$c.Name}}" placeholder="Name">
      <input name="url" type="url" value="{{$c.URL}}" placeholder="https://…" class="mono" style="margin-top:8px">
    </div>
  </div>
  {{end}}
  <div class="row">
    <label for="newname">Add</label><span class="rule"></span>
    <div class="field">
      <input id="newname" name="name" type="text" placeholder="Name">
      <input name="url" type="url" placeholder="Calendar address (webcal or https)" class="mono" style="margin-top:8px">
      <p class="hint">Find this in your calendar's sharing settings, as a secret address in iCal format.</p>
    </div>
  </div>
  <button type="submit">Save calendars</button>
</form>

<h1>Place</h1>
<form method="post" action="/save/place">
  <div class="row">
    <label for="lat">Where you are</label><span class="rule"></span>
    <div class="field">
      <input id="lat" name="latitude" type="text" value="{{.Config.Location.Latitude}}" class="mono" placeholder="Latitude">
      <input name="longitude" type="text" value="{{.Config.Location.Longitude}}" class="mono" placeholder="Longitude" style="margin-top:8px">
      <p class="hint">Used for the weather forecast.</p>
    </div>
  </div>
  <div class="row">
    <label for="tz">Time zone</label><span class="rule"></span>
    <div class="field">
      <input id="tz" name="timezone" type="text" value="{{.Config.Location.Timezone}}" class="mono" placeholder="America/Chicago">
    </div>
  </div>
  <button type="submit">Save place</button>
</form>

<h1>Display</h1>
<form method="post" action="/save/display">
  <div class="row">
    <label for="bright">Brightness</label><span class="rule"></span>
    <div class="field">
      <input id="bright" name="brightness" type="range" min="10" max="255" value="{{.Config.Display.Brightness}}">
    </div>
  </div>
  <div class="row">
    <label for="clock">Clock</label><span class="rule"></span>
    <div class="field">
      <label><input id="clock" name="clock_24h" type="checkbox" {{if .Config.Units.Clock24h}}checked{{end}}> 24-hour</label>
    </div>
  </div>
  <div class="row">
    <label for="unit">Temperature</label><span class="rule"></span>
    <div class="field">
      <select id="unit" name="temperature">
        <option value="fahrenheit" {{if eq .Config.Units.Temperature "fahrenheit"}}selected{{end}}>Fahrenheit</option>
        <option value="celsius" {{if eq .Config.Units.Temperature "celsius"}}selected{{end}}>Celsius</option>
      </select>
    </div>
  </div>
  <button type="submit">Save display</button>
</form>

<h1>Admin password</h1>
<form method="post" action="/save/password">
  <div class="row">
    <label for="adminpw">New password</label><span class="rule"></span>
    <div class="field">
      <input id="adminpw" name="password" type="password" placeholder="Leave blank to keep current">
      <p class="hint">Currently the device default: <span class="mono">{{.DefaultPassword}}</span></p>
    </div>
  </div>
  <button type="submit">Save password</button>
</form>
{{end}}
```

- [ ] **Step 5: Implement render.go**

```go
package portal

import (
	"embed"
	"html/template"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
)

//go:embed templates/*.html assets/*.css
var assetFS embed.FS

var tmpl = template.Must(template.ParseFS(assetFS, "templates/*.html"))

// pageData is everything the templates read.
type pageData struct {
	Config          *config.Config
	Status          wifi.Status
	APName          string
	DefaultPassword string
	Error           string
}
```

- [ ] **Step 6: Implement server.go**

```go
package portal

import (
	"net/http"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
)

// ConfigStore is the slice of cmd/dashboard's Store that the portal needs.
// Declared here rather than imported so this package does not depend on main.
type ConfigStore interface {
	Config() *config.Config
	SetConfig(*config.Config)
}

// Server serves the configuration UI.
type Server struct {
	Store      ConfigStore
	WiFi       *wifi.Client
	MAC        string
	ConfigPath string
}

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// The stylesheet is deliberately unauthenticated: it is also used by the
	// 401 page, which by definition has no credentials yet.
	mux.HandleFunc("GET /portal.css", func(w http.ResponseWriter, r *http.Request) {
		b, err := assetFS.ReadFile("assets/portal.css")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(b)
	})

	mux.Handle("GET /", s.auth(http.HandlerFunc(s.handleSettings)))
	mux.Handle("POST /save/wifi", s.auth(http.HandlerFunc(s.handleSaveWiFi)))
	mux.Handle("POST /save/calendars", s.auth(http.HandlerFunc(s.handleSaveCalendars)))
	mux.Handle("POST /save/place", s.auth(http.HandlerFunc(s.handleSavePlace)))
	mux.Handle("POST /save/display", s.auth(http.HandlerFunc(s.handleSaveDisplay)))
	mux.Handle("POST /save/password", s.auth(http.HandlerFunc(s.handleSavePassword)))
	return mux
}

// auth gates a handler behind the admin password.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, ok := r.BasicAuth()
		if !ok || !CheckPassword(pw, s.Store.Config().Portal.PasswordHash, s.MAC) {
			w.Header().Set("WWW-Authenticate", `Basic realm="UpNext setup"`)
			http.Error(w, "Enter the device password to continue.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 7: Implement handlers.go**

```go
package portal

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

// page renders the settings page with an optional error message.
func (s *Server) page(w http.ResponseWriter, errMsg string, code int) {
	cfg := s.Store.Config()
	data := pageData{
		Config:          cfg,
		APName:          "upnext-setup",
		DefaultPassword: DefaultPassword(s.MAC),
		Error:           errMsg,
	}
	if s.WiFi != nil {
		if st, err := s.WiFi.Status(); err == nil {
			data.Status = st
		}
	}
	if s.MAC != "" {
		data.APName = apNameFor(s.MAC)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		// Headers are already sent; log-and-stop is all that is left.
		fmt.Fprintf(w, "\n<!-- render error: %v -->", err)
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.page(w, "", http.StatusOK)
}

// save persists a mutated copy and installs it. Never mutates the live config:
// readers hold that pointer for a whole tick.
func (s *Server) save(mutate func(*config.Config) error) error {
	cur := s.Store.Config()
	next := *cur // shallow copy is enough; slices are replaced wholesale below
	if err := mutate(&next); err != nil {
		return err
	}
	if err := next.Save(s.ConfigPath); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}
	s.Store.SetConfig(&next)
	return nil
}

func (s *Server) handleSaveDisplay(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	b, err := strconv.Atoi(r.FormValue("brightness"))
	if err != nil || b < 0 || b > 255 {
		s.page(w, "Brightness must be a number between 0 and 255.", http.StatusBadRequest)
		return
	}
	temp := r.FormValue("temperature")
	if temp != "fahrenheit" && temp != "celsius" {
		temp = "fahrenheit"
	}
	clock := r.FormValue("clock_24h") != ""

	if err := s.save(func(c *config.Config) error {
		c.Display.Brightness = b
		c.Units.Clock24h = clock
		c.Units.Temperature = temp
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	applyBrightness(b)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSavePlace(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("latitude")), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("longitude")), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		s.page(w, "Enter a latitude between -90 and 90, and a longitude between -180 and 180.", http.StatusBadRequest)
		return
	}
	tz := strings.TrimSpace(r.FormValue("timezone"))
	if _, err := time.LoadLocation(tz); err != nil {
		s.page(w, fmt.Sprintf("%q is not a time zone this device knows. Use a name like America/Chicago.", tz), http.StatusBadRequest)
		return
	}
	if err := s.save(func(c *config.Config) error {
		c.Location.Latitude = lat
		c.Location.Longitude = lon
		c.Location.Timezone = tz
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSaveCalendars(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	names := r.Form["name"]
	urls := r.Form["url"]

	var feeds []config.CalendarSource
	for i := range urls {
		u := strings.TrimSpace(urls[i])
		if u == "" {
			continue // an empty row is a deletion, or the unused "add" row
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "webcal://") {
			s.page(w, "A calendar address should start with https:// or webcal://.", http.StatusBadRequest)
			return
		}
		u = strings.Replace(u, "webcal://", "https://", 1)
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		if name == "" {
			name = "Calendar"
		}
		feeds = append(feeds, config.CalendarSource{
			Name:  name,
			Color: calendarColor(len(feeds)),
			URL:   u,
		})
	}
	if err := s.save(func(c *config.Config) error {
		c.Calendars = feeds
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSavePassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	if pw == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther) // blank means "keep current"
		return
	}
	if len(pw) < 6 {
		s.page(w, "Use at least 6 characters.", http.StatusBadRequest)
		return
	}
	if err := s.save(func(c *config.Config) error {
		c.Portal.PasswordHash = HashPassword(pw)
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSaveWiFi(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	ssid := strings.TrimSpace(r.FormValue("ssid"))
	pw := r.FormValue("password")
	if ssid == "" {
		s.page(w, "Enter the name of the network to join.", http.StatusBadRequest)
		return
	}
	cur := s.Store.Config()
	if pw == "" {
		pw = cur.WiFi.Password // blank means "keep current"
	}
	if err := s.save(func(c *config.Config) error {
		c.WiFi.SSID = ssid
		c.WiFi.Password = pw
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Associating drops the connection this request arrived on, so the
	// redirect is sent first and the reconnect happens after.
	if s.WiFi != nil {
		go func() {
			_ = s.WiFi.Connect(ssid, pw)
		}()
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// calendarColor assigns feed colours from the dashboard's palette in order, so
// a user never has to pick a hex value.
func calendarColor(i int) string {
	palette := []string{"#4f9cff", "#ff7a59", "#8b97ab", "#e8ecf3"}
	return palette[i%len(palette)]
}
```

Add `"time"` to the imports.

- [ ] **Step 8: Add the two small helpers**

Append to `auth.go`:

```go
// apNameFor mirrors wifi.APName without importing it, so the portal's page
// rendering does not depend on the wifi package.
func apNameFor(mac string) string {
	clean := strings.ToLower(mac)
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	if len(clean) < 4 {
		return "upnext-setup"
	}
	return "upnext-" + clean[len(clean)-4:]
}
```

Create the brightness helper in `handlers.go`:

```go
// applyBrightness writes the panel's sysfs control. A failure is not fatal --
// the value is saved either way and takes effect on the next boot.
func applyBrightness(v int) {
	_ = os.WriteFile("/sys/class/backlight/waveshare_bl/brightness",
		[]byte(strconv.Itoa(v)), 0o644)
}
```

Add `"os"` to the handlers imports.

- [ ] **Step 9: Run the tests**

Run: `gofmt -w internal/portal && go test -count=1 ./internal/portal/ -v`
Expected: PASS for all nine.

- [ ] **Step 10: Look at the page in a browser**

```bash
cat > /tmp/portalpreview.go <<'EOF'
//go:build ignore
EOF
rm /tmp/portalpreview.go
go test ./internal/portal/ -run TestSettingsPageShowsCurrentValues -v
```

Then render it for real: add a temporary `TestDumpPage` that writes the settings HTML to `/tmp/portal.html`, run it, and `open /tmp/portal.html`. Confirm the hairline rules line up, the label column reads as a column, and it looks right at 390px wide (a phone). Delete the temporary test before committing. Describe what you saw in your report.

- [ ] **Step 11: Commit**

```bash
gofmt -w internal/portal
git add internal/portal
git commit -m "feat: portal server, auth, and settings page

Basic auth against the MAC default or a set password. Forms work without
JavaScript -- a captive-portal sheet may be an old WebView. Every settings row
repeats the panel's 380:1540 proportion with the same hairline colour."
```

---

### Task 10: Wire the portal into the dashboard

**Files:**
- Modify: `cmd/dashboard/main.go`
- Modify: `internal/wifi/wifi.go` (add `MAC()`)
- Modify: `internal/wifi/wifi_test.go`

**Interfaces:**
- Consumes: `portal.Server` (Task 9), `wifi.Client` (Tasks 5–7), `Store` (Task 8)
- Produces: a portal goroutine on `:8080`; `func (c *Client) MAC() string`

- [ ] **Step 1: Write the failing test for MAC()**

```go
func TestMACReadsSysfs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/address", []byte("54:01:4a:4c:1b:fd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{R: &fakeRunner{}, SysfsNet: dir}
	if got := c.MAC(); got != "54:01:4a:4c:1b:fd" {
		t.Errorf("MAC = %q, want the trimmed address", got)
	}
}

func TestMACMissingIsEmpty(t *testing.T) {
	// Driver not loaded: must return empty rather than panic, so the portal
	// falls back to a usable AP name and refuses the MAC default password.
	c := &Client{R: &fakeRunner{}, SysfsNet: t.TempDir()}
	if got := c.MAC(); got != "" {
		t.Errorf("MAC = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/wifi/ -run TestMAC`
Expected: FAIL — `unknown field SysfsNet`

- [ ] **Step 3: Implement MAC()**

Add to `Client` in `wifi.go`:

```go
	// SysfsNet is the interface's sysfs directory. Defaults to
	// /sys/class/net/wlan0 when empty; tests point it at a temp dir.
	SysfsNet string
```

And:

```go
// MAC returns the interface's hardware address, or "" if it cannot be read
// (driver not loaded). Callers must treat "" as "no MAC available" rather than
// substituting a constant -- the admin password derives from this.
func (c *Client) MAC() string {
	dir := c.SysfsNet
	if dir == "" {
		dir = "/sys/class/net/" + Iface
	}
	b, err := os.ReadFile(dir + "/address")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
```

- [ ] **Step 4: Start the portal in main.go**

After `store.SetConfig(cfg)` and before the fetch goroutines:

```go
	wc := &wifi.Client{R: wifi.ExecRunner{}}
	ps := &portal.Server{
		Store:      store,
		WiFi:       wc,
		MAC:        wc.MAC(),
		ConfigPath: *cfgPath,
	}
	go func() {
		// The portal is a goroutine in this process, not a second binary, so a
		// save can swap the config pointer directly. A failure here must not
		// stop the dashboard: a panel that renders without a config UI is far
		// better than no panel.
		if err := http.ListenAndServe(*portalAddr, ps.Handler()); err != nil {
			log.Printf("portal: %v", err)
		}
	}()
```

Add the flag next to the others:

```go
	portalAddr := flag.String("portal", ":8080", "address for the configuration portal")
```

Add `"net/http"`, `"github.com/nathanstitt/luckfox-dashboard/internal/portal"`, and `"github.com/nathanstitt/luckfox-dashboard/internal/wifi"` to the imports.

Apply the saved brightness once at startup, right after the config loads:

```go
	// The panel keeps whatever brightness it had; apply the configured value so
	// a reboot honours it.
	if b := cfg.Display.Brightness; b > 0 {
		_ = os.WriteFile("/sys/class/backlight/waveshare_bl/brightness",
			[]byte(strconv.Itoa(b)), 0o644)
	}
```

Add `"strconv"` to the imports.

**`--once` must not start the portal.** Confirm the `if *once { ... return }` block still comes before the portal goroutine, exactly as it does for the fetch loops.

- [ ] **Step 5: Verify everything builds and passes**

Run: `gofmt -w ./... && go build ./... && go test -count=1 ./... && go test -race -count=1 ./cmd/dashboard/`
Expected: PASS, no races.

- [ ] **Step 6: Verify --once still starts nothing**

```bash
go run ./cmd/dashboard --once --config config.sample.json --fb /dev/null --html-out /tmp/f.html
lsof -i :8080 2>/dev/null | head -3 || echo "no listener — correct"
```
Expected: renders one frame, exits 0, and holds no port.

- [ ] **Step 7: Commit**

```bash
git add cmd/dashboard internal/wifi
git commit -m "feat: serve the portal from the dashboard process

One binary, one config pointer -- a save swaps it under the existing mutex and
the next tick renders the new settings. A portal failure logs and leaves the
panel running."
```

---

### Task 11: AP fallback at boot

**Files:**
- Modify: `board/etc/init.d/S99wlan0`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: `wifi.StartAP` semantics (Task 7)
- Produces: a board that raises its own AP when it cannot join a network

The current `S99wlan0` gives up after failing to associate. It should fall back to AP mode so the portal is reachable — that is the whole point of the feature.

- [ ] **Step 1: Add the fallback**

In `board/etc/init.d/S99wlan0`, replace the `udhcpc` block and everything after it in `start()` with:

```sh
	udhcpc -i wlan0 -n -q >/dev/null 2>&1
	if ip -o addr show wlan0 2>/dev/null | grep -q "inet "; then
		echo "OK"
	else
		echo "FAIL (no lease)"
		start_ap
		return 0
	fi

	# Clock: try each host, stop at the first that answers.
	for h in $RDATE_HOSTS; do
		if rdate -s "$h" >/dev/null 2>&1; then
			echo "Clock set from $h: $(date)"
			break
		fi
	done
}

# start_ap raises the setup access point so the portal stays reachable when
# there is no network to join. Named from the WiFi MAC's last two octets, so a
# returning user sees the same name every time.
start_ap() {
	printf "Starting setup AP: "

	mac=$(cat /sys/class/net/wlan0/address 2>/dev/null | tr -d ':' | tr 'A-Z' 'a-z')
	if [ -n "$mac" ]; then
		suffix=$(echo "$mac" | tail -c 5)
		ssid="upnext-$suffix"
	else
		ssid="upnext-setup"
	fi

	killall wpa_supplicant 2>/dev/null

	cat > /tmp/hostapd.conf <<EOF
interface=wlan0
driver=nl80211
ssid=$ssid
hw_mode=g
channel=6
auth_algs=1
wmm_enabled=0
EOF

	# address=/#/ answers every DNS name with the portal's address. There is no
	# iptables on this image, so that wildcard IS the captive-portal redirect.
	cat > /tmp/dnsmasq-ap.conf <<EOF
interface=wlan0
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,12h
address=/#/192.168.4.1
no-resolv
EOF

	ip addr add 192.168.4.1/24 dev wlan0 2>/dev/null
	ip link set wlan0 up
	hostapd -B /tmp/hostapd.conf >/dev/null 2>&1
	dnsmasq -C /tmp/dnsmasq-ap.conf >/dev/null 2>&1

	echo "$ssid at 192.168.4.1"
}
```

Also handle the no-credentials case — if `/etc/wpa_supplicant.conf` still holds the placeholder, do not waste 30 seconds waiting to associate. Insert before the `wpa_supplicant` launch:

```sh
	if grep -q 'ssid="SSID"' /etc/wpa_supplicant.conf 2>/dev/null; then
		echo "no network configured"
		start_ap
		return 0
	fi
```

- [ ] **Step 2: Check the script parses**

Run: `sh -n board/etc/init.d/S99wlan0 && echo "syntax OK"`
Expected: `syntax OK`

- [ ] **Step 3: Deploy and test the happy path first**

```bash
scripts/deploy.sh
adb shell '/etc/init.d/S99wlan0 restart'
adb shell 'ip -o addr show wlan0 | grep -o "inet [0-9.]*"'
```
Expected: the board reassociates and keeps its LAN address. **Verify this before testing the AP path** — it confirms the edit did not break normal operation.

- [ ] **Step 4: Test the AP fallback safely**

⚠️ This drops the board off the network. Run it detached with an unconditional restore, or the board can be stranded and need a physical power cycle:

```bash
adb shell 'nohup sh -c "
  cp /etc/wpa_supplicant.conf /tmp/wpa.good
  sed -i \"s/ssid=\\\".*\\\"/ssid=\\\"SSID\\\"/\" /etc/wpa_supplicant.conf
  /etc/init.d/S99wlan0 restart
  sleep 45
  cp /tmp/wpa.good /etc/wpa_supplicant.conf
  killall hostapd dnsmasq 2>/dev/null
  ip addr del 192.168.4.1/24 dev wlan0 2>/dev/null
  /etc/init.d/S99wlan0 restart
" > /tmp/aptest.log 2>&1 &
echo "AP test running; restores in ~45s"'
```

During that window, look for `upnext-1bfd` in your phone's WiFi list and confirm the portal loads at `http://192.168.4.1:8080`. Then wait for the restore and verify the board is back on the LAN.

- [ ] **Step 5: Document it**

Add to `CLAUDE.md` under the WiFi section:

```markdown
**No network means the board becomes one.** If `S99wlan0` cannot associate — no
credentials, wrong password, router down — it raises an open AP named
`upnext-<4 hex of the MAC>` at `192.168.4.1` and serves the portal there.
`dnsmasq` answers every DNS query with that address, which is what makes a phone
offer its "sign in to network" sheet; there is no `iptables` on this image, so
that wildcard is the entire redirect.

The portal is on `:8080` in both modes. The admin password defaults to the last
6 hex of the WiFi MAC and is shown on the panel while the board is unconfigured.
```

- [ ] **Step 6: Commit**

```bash
git add board/etc/init.d/S99wlan0 CLAUDE.md
git commit -m "feat: raise a setup AP when the board cannot join a network

Also skips the 30s association wait when the supplicant config still holds the
placeholder SSID -- a fresh board goes straight to AP mode."
```

---

### Task 12: Show the setup hint on the panel

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/model/model_test.go`
- Modify: `internal/view/templates/dashboard.html`
- Modify: `internal/view/assets/style.css`
- Modify: `internal/view/view_test.go`
- Modify: `cmd/dashboard/main.go`

**Interfaces:**
- Consumes: `ViewModel` (Task 2)
- Produces: `ViewModel.Setup *SetupHint`, `type SetupHint struct { APName, Password string }`

The spec's resolved decision: while unconfigured, the panel shows the AP name and admin password. First-run then needs no documentation, using a screen that is otherwise blank. Accepted trade: anyone who can see the panel learns the admin password.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/model/ -run TestBuildShowsSetup`
Expected: FAIL — too many arguments to `Build`

- [ ] **Step 3: Add the field and parameter**

In `internal/model/model.go`:

```go
// SetupHint is shown on the panel while the board has no network configured.
// It carries the setup AP's name and the admin password so first-run needs no
// documentation -- the screen is otherwise blank at that point. Anyone who can
// see the panel learns the password; that trade is accepted for a home display.
type SetupHint struct {
	APName   string
	Password string
}
```

Add `Setup *SetupHint` to `ViewModel`, and a `setup *SetupHint` parameter to `Build`, assigning `vm.Setup = setup`.

Update the existing `Build` call sites — `cmd/dashboard/main.go` and every model test — to pass `nil` where there is no hint.

- [ ] **Step 4: Render it**

In `internal/view/templates/dashboard.html`, inside `#now-block` after the clock:

```html
    {{with .VM.Setup}}
    <div id="setup-hint">
      <div class="sh-lead">Set me up</div>
      <div class="sh-step">Join Wi-Fi <span class="sh-mono">{{.APName}}</span></div>
      <div class="sh-step">Password <span class="sh-mono">{{.Password}}</span></div>
    </div>
    {{end}}
```

In `internal/view/assets/style.css`:

```css
/* Setup hint: only visible while the board has no network. Sized to be read
   from across a room, since that is the distance someone stands at when they
   notice the panel is not working. */
#setup-hint { margin-top: 26px; }
.sh-lead { font-size: 15px; color: var(--text-dim); text-transform: uppercase; letter-spacing: 1px; }
.sh-step { font-size: 22px; margin-top: 8px; }
.sh-mono { font-family: "DejaVu Sans Mono", monospace; color: var(--blue); }
```

- [ ] **Step 5: Pass the hint from main.go**

Where the render loop builds the view model, supply the hint only when unconfigured:

```go
// setupHint returns a panel hint while the board has no network configured,
// and nil once it does.
func setupHint(cfg *config.Config, wc *wifi.Client) *model.SetupHint {
	if cfg.WiFi.SSID != "" {
		return nil
	}
	mac := wc.MAC()
	return &model.SetupHint{
		APName:   wifi.APName(mac),
		Password: portal.DefaultPassword(mac),
	}
}
```

Thread it into the `model.Build` call inside `renderOnce`.

- [ ] **Step 6: Add a view test**

```go
func TestRenderShowsSetupHint(t *testing.T) {
	vm, w := fixtureVM(t)
	vm.Setup = &model.SetupHint{APName: "upnext-1bfd", Password: "4c1bfd"}
	got, err := Render(vm, w)
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
	vm, w := fixtureVM(t)
	vm.Setup = nil
	got, err := Render(vm, w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "setup-hint") {
		t.Error("setup hint rendered when the board is configured")
	}
}
```

- [ ] **Step 7: Run everything and regenerate the golden**

Run: `gofmt -w ./... && go test -count=1 ./...`
If the view golden fails, regenerate and inspect it: `UPDATE_GOLDEN=1 go test ./internal/view/ -run TestRenderMatchesGolden` then confirm the file still looks like a dashboard.

- [ ] **Step 8: Commit**

```bash
git add internal/model internal/view cmd/dashboard
git commit -m "feat: show the setup AP and password on the panel when unconfigured

First run then needs no documentation, using a screen that is otherwise blank."
```

---

### Task 13: Verify on hardware

**Files:**
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: everything
- Produces: a verified end-to-end first-run

**Ask before running any step in this task.** It reconfigures the physical appliance, and one step deliberately drops it off the network.

- [ ] **Step 1: Build and deploy**

```bash
scripts/build.sh dashboard && scripts/deploy.sh
adb shell '/etc/init.d/S99zdashboard restart'
```

- [ ] **Step 2: Reach the portal on the LAN**

```bash
open http://192.168.1.80:8080
```
Log in with the MAC default (`4c1bfd` on this board). Confirm: the status strip reads "Connected · Argosity · 192.168.1.80", the settings show current values, and at a phone width the rows read as label-above-field.

- [ ] **Step 3: Change something and watch the panel**

Move the brightness slider, save, and watch the panel dim within a second. Then change the clock to 24-hour, save, and confirm the panel switches at the next minute tick **without the process restarting** (`adb shell '/etc/init.d/S99zdashboard status'` should report the same PID).

- [ ] **Step 4: Add a real calendar feed**

Paste an actual iCal URL and save. Within the calendar refresh interval the agenda should populate — event blocks with floating labels on the timeline. This is the first time that code renders real events; look for overlapping labels, events in the wrong place relative to the NOW line, and anything clipped.

Capture it: `adb shell 'cat /dev/fb0' > /tmp/fb.raw && go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate && open /tmp/panel.png`

- [ ] **Step 5: Test first-run AP mode**

⚠️ Detached with an unconditional restore, or the board can be stranded:

```bash
adb shell 'nohup sh -c "
  cp /etc/wpa_supplicant.conf /tmp/wpa.good
  sed -i \"s/ssid=\\\".*\\\"/ssid=\\\"SSID\\\"/\" /etc/wpa_supplicant.conf
  /etc/init.d/S99wlan0 restart
  sleep 90
  cp /tmp/wpa.good /etc/wpa_supplicant.conf
  killall hostapd dnsmasq 2>/dev/null
  ip addr del 192.168.4.1/24 dev wlan0 2>/dev/null
  /etc/init.d/S99wlan0 restart
" > /tmp/aptest.log 2>&1 &'
```

In that 90-second window, on a phone: the panel should show "Join Wi-Fi upnext-1bfd / Password 4c1bfd"; join it; a captive-portal sheet should appear (or `http://192.168.4.1:8080` loads); enter the password and the SSID, save. Then let the restore run and confirm the board returns to the LAN.

- [ ] **Step 6: Reboot and confirm it holds**

```bash
adb reboot
until adb shell 'ip -o addr show wlan0 2>/dev/null | grep -q "inet "'; do sleep 3; done
adb shell '/etc/init.d/S99zdashboard status; ip -o addr show wlan0 | grep -o "inet [0-9.]*"'
curl -s -o /dev/null -w "portal: %{http_code}\n" -u admin:4c1bfd http://192.168.1.80:8080/
```

- [ ] **Step 7: Record what you found and commit**

Update `CLAUDE.md` with the measured portal behaviour and anything surprising. Commit.

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Settings inventory (9 live settings) | 2, 9 |
| Dead fields removed | 2 |
| WiFi / admin password / backlight added | 2, 4, 9 |
| AP mode with `upnext-<4hex>` | 7, 11 |
| dnsmasq wildcard DNS as the redirect | 7, 11 |
| Portal on `:8080` in both modes | 9, 10 |
| Admin password: MAC default, hashed, changeable | 4, 9 |
| Panel shows AP name + password when unconfigured | 12 |
| Palette, 380:1540 signature, phone-first | 9 |
| Copy rules (owner's vocabulary, actionable errors) | 9 |
| No numbered steps on settings | 9 |
| Atomic config writes with `.bak` | 3 |
| Config swap under the existing mutex | 8 |
| Progressive enhancement, no JS framework | 9 |
| Testing: config, portal, wifi with fake runner, hardware | 3, 4, 5, 6, 7, 9, 13 |

**Deviations, flagged:**
- **BOOT-button password recovery is NOT implemented.** The spec lists it; reading the button at boot needs GPIO work that is a separate concern from the portal. Recovery today is editing `config.json` over adb. Worth a follow-up task.
- **Task 1 is resolved.** The AIC8800DC reports `AP` from `wpa_cli get_capability modes`, and the driver implements the cfg80211 AP callbacks. Tasks 7, 11, and 13 are viable as designed. Implementation starts at Task 2. Noted for Task 7: the radio is 2.4 GHz only, channels 1-14, so channel 6 is valid.
- The spec's "one orchestrated moment" motion on save is not implemented — forms use plain post-redirect-get. Adding it needs JavaScript, which conflicts with the old-WebView constraint. The status strip still reflects the new state after the redirect.

**Type consistency:** `Runner`, `Client`, `Status`, `Network` are consistent across Tasks 5–7 and 10. `ConfigStore` (Task 9) matches the `Config()`/`SetConfig()` pair from Task 8. `DefaultPassword`/`HashPassword`/`CheckPassword` (Task 4) are used unchanged in Task 9. `SetupHint` (Task 12) matches its use in `main.go`.

**Known rough edge:** Task 9's `handleSaveCalendars` reconstructs the whole feed list from repeated form fields, so an empty URL row deletes that feed. That is intentional (it is how you remove one) but non-obvious; the UI should eventually have an explicit delete control.
