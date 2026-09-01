# UpNext Luckfox Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go service on the Luckfox Lyra Zero W that fetches calendar and weather data, generates HTML+SVG, rasterizes it with omnidoc, and writes the result to `/dev/fb0` as a 1920×480 landscape dashboard.

**Architecture:** Single static binary, display-only (no touch, no HTTP server). A tick loop rebuilds a view model each minute and re-renders only when the generated HTML changes. Layout logic lives in a pure `model` package that takes time as a parameter, so the timeline algorithm is unit-testable without network, rendering, or hardware.

**Tech Stack:** Go 1.25+ (stdlib only, plus `github.com/nathanstitt/omnidoc` for HTML layout/rasterization). Cross-compiled `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0`. Deployed over adb.

## Global Constraints

- **Module path:** `github.com/nathanstitt/luckfox-dashboard` — this repo has no `go.mod` yet; Task 1 creates it.
- **Go version:** `go 1.26.0` in `go.mod`, matching the host toolchain (1.26.3). omnidoc declares 1.25.0; a newer declaration builds it fine under the replace directive.
- **Dependencies:** stdlib only, plus omnidoc via a `replace` directive to a local path. No other third-party modules.
- **Cross-compile:** `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0`, `-ldflags="-s -w"`. Binaries must be 32-bit ARM ELF or they fail on the board with a confusing exec error.
- **Panel geometry:** framebuffer is 480×1920 portrait XR24 (stride 1920, 3686400 bytes). The UI is 1920×480 landscape, rotated 270° at blit time.
- **Page sizing:** always `omnidoc.WithPageSize(1920, 480)`. Without it the viewport defaults to 1280px and the render is pillarboxed.
- **No `time.Now()` inside logic.** Every function whose behavior depends on the clock takes a `now time.Time` parameter. The board has no RTC and boots at 1970; tests must be deterministic.
- **No emoji in rendered output.** The board has only DejaVu and Liberation; 6 of 9 weather emoji render as nothing at all. Weather icons must be SVG.
- **Never deploy to the board without being asked.** Building is fine; pushing to hardware is a separate, explicitly-requested step.

## File Structure

```
go.mod                          module + omnidoc replace
cmd/dashboard/main.go           flag parsing, tick loop wiring
internal/config/config.go       Config struct, load, defaults
internal/config/config_test.go
internal/calendar/ical.go       iCal parse + RRULE expansion (ported)
internal/calendar/ical_test.go
internal/calendar/testdata/*.ics committed fixtures
internal/weather/weather.go     Open-Meteo fetch + normalized types
internal/weather/weather_test.go
internal/model/model.go         Event/Weather -> ViewModel
internal/model/timeline.go      time->X mapping, window, label placement
internal/model/timeline_test.go
internal/model/model_test.go
internal/chart/chart.go         Weather -> SVG (template + computed values)
internal/chart/templates/*.svg
internal/chart/chart_test.go
internal/chart/testdata/*.svg   golden SVG
internal/view/view.go           ViewModel -> HTML
internal/view/templates/*.html
internal/view/assets/style.css
internal/view/assets/icons/*.svg
internal/view/view_test.go
internal/view/testdata/*.html   golden HTML
internal/fb/fb.go               rasterize, rotate, write framebuffer
internal/fb/fb_test.go
tools/fb2png/main.go            host-side framebuffer dump -> PNG
```

Rationale: `model` holds all the interesting logic and is pure. `view` and `chart` are output formatting with golden tests. `fb` is the only package that touches hardware. `calendar` and `weather` are the ported I/O layers.

---

### Task 1: Module scaffolding and build wiring

**Files:**
- Create: `go.mod`
- Create: `cmd/dashboard/main.go`
- Modify: `scripts/build.sh:20-30`

**Interfaces:**
- Consumes: nothing
- Produces: a buildable `./cmd/dashboard` package; `scripts/build.sh dashboard` cross-compiles it to `build/dashboard`

Note on `build.sh`: with no arguments it switches `MODULE_DIR` to the omnidoc repo and builds `html2fb`/`fbtouch`. Building a package from *this* repo currently requires passing the package path. Task 1 adds a `dashboard` shorthand so `scripts/build.sh dashboard` works.

- [ ] **Step 1: Create the module**

```bash
cd /Users/nas/code/upnext/luckfox
cat > go.mod <<'EOF'
module github.com/nathanstitt/luckfox-dashboard

go 1.26.0

require github.com/nathanstitt/omnidoc v0.0.0

replace github.com/nathanstitt/omnidoc => ../../omnidoc
EOF
```

The `replace` path is relative to this repo (`/Users/nas/code/upnext/luckfox` → `/Users/nas/code/omnidoc`). If `OMNIDOC_DIR` differs, adjust it.

- [ ] **Step 2: Write a minimal main that proves the toolchain**

Create `cmd/dashboard/main.go`:

```go
// Command dashboard renders the UpNext dashboard to the Luckfox framebuffer.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	once := flag.Bool("once", false, "render a single frame and exit")
	fbDev := flag.String("fb", "/dev/fb0", "framebuffer device")
	cfgPath := flag.String("config", "/root/config.json", "path to config.json")
	flag.Parse()

	fmt.Printf("dashboard: once=%v fb=%s config=%s\n", *once, *fbDev, *cfgPath)
	os.Exit(0)
}
```

- [ ] **Step 3: Verify it builds for the host**

Run: `go build ./cmd/dashboard && ./dashboard --once`
Expected: prints `dashboard: once=true fb=/dev/fb0 config=/root/config.json`

- [ ] **Step 4: Add the dashboard shorthand to build.sh**

In `scripts/build.sh`, replace the block that currently reads:

```bash
OMNIDOC="${OMNIDOC_DIR:-$HOME/code/omnidoc}"
if [ "$MODULE_DIR" = "$REPO_ROOT" ] && [ $# -eq 0 ]; then
	[ -d "$OMNIDOC" ] || die "omnidoc not found at $OMNIDOC
set OMNIDOC_DIR, or pass packages to build"
	MODULE_DIR="$OMNIDOC"
	set -- ./cmd/html2fb ./cmd/fbtouch
fi
```

with:

```bash
OMNIDOC="${OMNIDOC_DIR:-$HOME/code/omnidoc}"
# "dashboard" builds this repo's service; no args builds the omnidoc tools.
if [ "${1:-}" = "dashboard" ]; then
	shift
	set -- ./cmd/dashboard "$@"
elif [ "$MODULE_DIR" = "$REPO_ROOT" ] && [ $# -eq 0 ]; then
	[ -d "$OMNIDOC" ] || die "omnidoc not found at $OMNIDOC
set OMNIDOC_DIR, or pass packages to build"
	MODULE_DIR="$OMNIDOC"
	set -- ./cmd/html2fb ./cmd/fbtouch
fi
```

- [ ] **Step 5: Verify the cross-compile produces a 32-bit ARM binary**

Run: `scripts/build.sh dashboard`
Expected: `==> building dashboard for linux/arm (v7)` then a size line, and no "not a 32-bit ARM binary" error (build.sh checks the ELF header itself).

- [ ] **Step 6: Commit**

```bash
rm -f dashboard   # host build artifact from step 3
git add go.mod cmd/dashboard/main.go scripts/build.sh
git commit -m "feat: scaffold dashboard command and cross-compile wiring"
```

---

### Task 2: Config loading

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `config.sample.json`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type Config struct` with fields `Location{Name string, Latitude, Longitude float64, Timezone string}`, `Units{Temperature, WindSpeed, Precipitation string, Clock24h bool}`, `Refresh{WeatherMinutes, CalendarMinutes int}`, `Agenda{DaysAhead, MaxEvents int}`, `Calendars []CalendarSource`
  - `type CalendarSource struct { Name, Color, URL string }`
  - `func Load(path string) (*Config, error)` — applies defaults, returns error on unreadable/invalid JSON
  - `func (c *Config) Location() *time.Location` — resolves `Location.Timezone`, falling back to UTC

Dropped from the Pi config: `open_weather_api_key`, `calendar_source`, `appsscript` (the Apps Script path and OpenWeather are not ported; Open-Meteo needs no key and iCal is the calendar source).

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL — `undefined: Load`

- [ ] **Step 3: Write the implementation**

Create `internal/config/config.go`:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS for all five tests.

- [ ] **Step 5: Add a sample config**

Create `config.sample.json`:

```json
{
  "location": {
    "name": "Home",
    "latitude": 38.5693164,
    "longitude": -92.1629241,
    "timezone": "America/Chicago"
  },
  "units": {
    "temperature": "fahrenheit",
    "wind_speed": "mph",
    "precipitation": "inch",
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
  "calendars": [
    { "name": "Personal", "color": "#4f9cff", "url": "PASTE_ICAL_URL" }
  ]
}
```

- [ ] **Step 6: Commit**

```bash
git add internal/config config.sample.json
git commit -m "feat: config loading with defaults"
```

---

### Task 3: Port the iCal parser with real test coverage

**Files:**
- Create: `internal/calendar/ical.go`
- Create: `internal/calendar/ical_test.go`
- Create: `internal/calendar/testdata/basic.ics`
- Create: `internal/calendar/testdata/recurring.ics`

**Interfaces:**
- Consumes: `config.CalendarSource`
- Produces:
  - `type Event struct { Calendar, Color, Title, Location, Description string; Start, End time.Time; AllDay bool; Status string }`
  - `func Parse(body []byte, calName, color string, now time.Time, daysAhead int, ownerEmail string) ([]Event, error)`
  - `func Fetch(ctx context.Context, src config.CalendarSource, now time.Time, daysAhead int) ([]Event, error)`

**Two deliberate changes from the Pi version:**

1. **`Start`/`End` become `time.Time`, not `string`.** The Pi serialized to JSON for a browser; here the model does time math directly. This removes a parse/format round trip.
2. **Parsing is split from fetching.** The Pi's `fetchIcal` did both, which is why its tests need a live URL. `Parse` takes bytes so it can be tested against committed fixtures.

The Pi's tests (`ical_test.go`) both `t.Skip` unless `RICE_ICS` points at a private calendar export — so this logic currently ships with **no runnable coverage**. That is the gap this task closes.

Source to port: `/Users/nas/code/upnext/pi-dashboard/main.go:553-1133` (581 lines). Its only external dependency is `httpGet`; `icalUnescape` and `icalParseDateTime` are inside the block. Copy these functions and adapt signatures:

`icalProp`, `icalVEvent` (+ `get`/`val` methods), `fetchIcal` (split into `Fetch`/`Parse`), `icalOwnerFromURL`, `icalOwnerStatus`, `icalParseProp`, `icalOccKey`, `icalExpand`, `icalRecur`, `bydayWeekdays`, `weekdayCode`, `bydayOrdinal`, `startOfWeek`, `dateWithClock`, `monthlyOccurrences`, `nthWeekdayOfMonth`, `monthDay`, `icalParseDateTime`, `icalUnescape`.

Replace every internal `time.Now()` with the `now` parameter.

- [ ] **Step 1: Create the test fixtures**

Create `internal/calendar/testdata/basic.ics`:

```
BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:single-1
DTSTART:20260826T140000Z
DTEND:20260826T150000Z
SUMMARY:Design Review
LOCATION:Conference Room B
END:VEVENT
BEGIN:VEVENT
UID:allday-1
DTSTART;VALUE=DATE:20260827
DTEND;VALUE=DATE:20260828
SUMMARY:Company Holiday
END:VEVENT
BEGIN:VEVENT
UID:folded-1
DTSTART:20260826T160000Z
DTEND:20260826T163000Z
SUMMARY:A meeting with a very long title that RFC 5545 requires be fol
 ded across lines
END:VEVENT
END:VCALENDAR
```

Note: the third event's `SUMMARY` is folded — the continuation line starts with a single space. That space is significant; unfolding must join the lines and drop it.

Create `internal/calendar/testdata/recurring.ics`:

```
BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:weekly-1
DTSTART:20260824T130000Z
DTEND:20260824T133000Z
RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR
SUMMARY:Standup
END:VEVENT
BEGIN:VEVENT
UID:weekly-2
DTSTART:20260825T090000Z
DTEND:20260825T100000Z
RRULE:FREQ=DAILY;COUNT=5
EXDATE:20260827T090000Z
SUMMARY:Daily Sync
END:VEVENT
BEGIN:VEVENT
UID:weekly-2
RECURRENCE-ID:20260828T090000Z
DTSTART:20260828T110000Z
DTEND:20260828T120000Z
SUMMARY:Daily Sync (moved)
END:VEVENT
END:VCALENDAR
```

This covers: weekly BYDAY expansion, DAILY with COUNT, an EXDATE exclusion, and a RECURRENCE-ID override that both replaces an instance and must not produce a duplicate.

- [ ] **Step 2: Write the failing test**

Create `internal/calendar/ical_test.go`:

```go
package calendar

import (
	"os"
	"testing"
	"time"
)

// ref is a fixed "now" inside the fixture window: Mon 2026-08-24 08:00 UTC.
var ref = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func titles(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Title
	}
	return out
}

func TestParseSingleEvent(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	var got *Event
	for i := range evs {
		if evs[i].Title == "Design Review" {
			got = &evs[i]
		}
	}
	if got == nil {
		t.Fatalf("Design Review not found in %v", titles(evs))
	}
	want := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", got.Start, want)
	}
	if got.Location != "Conference Room B" {
		t.Errorf("Location = %q", got.Location)
	}
	if got.AllDay {
		t.Error("AllDay = true, want false")
	}
	if got.Calendar != "Work" || got.Color != "#fff" {
		t.Errorf("Calendar/Color = %q/%q", got.Calendar, got.Color)
	}
}

func TestParseAllDayEvent(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Title == "Company Holiday" {
			if !e.AllDay {
				t.Error("AllDay = false, want true")
			}
			return
		}
	}
	t.Fatalf("Company Holiday not found in %v", titles(evs))
}

func TestParseUnfoldsLongLines(t *testing.T) {
	evs, err := Parse(load(t, "basic.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "A meeting with a very long title that RFC 5545 requires be folded across lines"
	for _, e := range evs {
		if e.Title == want {
			return
		}
	}
	t.Fatalf("unfolded title not found; got %v", titles(evs))
}

func TestParseExpandsWeeklyByDay(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	var days []time.Weekday
	for _, e := range evs {
		if e.Title == "Standup" {
			days = append(days, e.Start.Weekday())
		}
	}
	if len(days) < 3 {
		t.Fatalf("expected >=3 Standup occurrences in 7 days, got %d", len(days))
	}
	for _, d := range days {
		if d != time.Monday && d != time.Wednesday && d != time.Friday {
			t.Errorf("Standup on %v, want only Mon/Wed/Fri", d)
		}
	}
}

func TestParseHonorsExdate(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	excluded := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	for _, e := range evs {
		if e.Title == "Daily Sync" && e.Start.Equal(excluded) {
			t.Fatal("EXDATE occurrence was not excluded")
		}
	}
}

func TestParseAppliesRecurrenceOverride(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	moved := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	var foundMoved bool
	for _, e := range evs {
		if e.Start.Equal(moved) && e.Title == "Daily Sync (moved)" {
			foundMoved = true
		}
		// The original 09:00 slot on the 28th must not also appear.
		if e.Title == "Daily Sync" &&
			e.Start.Equal(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)) {
			t.Error("overridden occurrence still present at its original time")
		}
	}
	if !foundMoved {
		t.Error("RECURRENCE-ID override not found at its new time")
	}
}

func TestParseNoDuplicateOccurrences(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		k := e.Title + "|" + e.Start.Format(time.RFC3339)
		if seen[k] {
			t.Errorf("duplicate occurrence: %s", k)
		}
		seen[k] = true
	}
}

func TestParseRespectsWindow(t *testing.T) {
	evs, err := Parse(load(t, "recurring.ics"), "Work", "#fff", ref, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	cutoff := ref.AddDate(0, 0, 1)
	for _, e := range evs {
		if e.Start.After(cutoff) {
			t.Errorf("event %q at %v is beyond the 1-day window", e.Title, e.Start)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/calendar/`
Expected: FAIL — `undefined: Parse`, `undefined: Event`

- [ ] **Step 4: Port the parser**

Create `internal/calendar/ical.go`. Copy `main.go:553-1133` from pi-dashboard, then apply these changes:

1. Package declaration `package calendar`; export `Event` with `time.Time` for `Start`/`End`.
2. Split `fetchIcal` into:

```go
// Fetch retrieves one iCal feed and parses it. The owner email is derived from
// the feed URL so ATTENDEE PARTSTAT can be matched.
func Fetch(ctx context.Context, src config.CalendarSource, now time.Time, daysAhead int) ([]Event, error) {
	body, err := httpGet(ctx, src.URL)
	if err != nil {
		return nil, err
	}
	return Parse(body, src.Name, src.Color, now, daysAhead, icalOwnerFromURL(src.URL))
}

// Parse expands an iCal feed into concrete occurrences within the window
// [now, now+daysAhead]. Handles folded lines, VALUE=DATE all-day events, RRULE
// expansion with EXDATE exclusions and RECURRENCE-ID overrides.
func Parse(body []byte, calName, color string, now time.Time, daysAhead int, ownerEmail string) ([]Event, error) {
	// ... body of the Pi's fetchIcal after the httpGet call ...
}
```

3. Replace every `time.Now()` inside the block with the `now` parameter — it appears in the window/cutoff computation and inside `icalExpand`/`icalRecur`.
4. Where the Pi built `Event{Start: t.Format(time.RFC3339)}`, assign `Start: t` directly.
5. Keep `httpGet` as a small unexported helper in this package:

```go
func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/calendar/ -v`
Expected: PASS for all eight tests. If `TestParseExpandsWeeklyByDay` fails with zero occurrences, check that `now` replaced `time.Now()` in the recurrence window computation — that is the most likely porting mistake.

- [ ] **Step 6: Commit**

```bash
git add internal/calendar
git commit -m "feat: port iCal parser with committed fixtures

Splits parsing from fetching so recurrence expansion is testable without a
live calendar URL. The Pi version's tests skip unless RICE_ICS points at a
private export, so this logic shipped without runnable coverage."
```

---

### Task 4: Weather fetching

**Files:**
- Create: `internal/weather/weather.go`
- Create: `internal/weather/weather_test.go`
- Create: `internal/weather/testdata/openmeteo.json`

**Interfaces:**
- Consumes: `config.Config`
- Produces:
  - `type Conditions struct { Time time.Time; TempF float64; Code int; IsDay bool }`
  - `type HourPoint struct { Time time.Time; TempF float64; PrecipProb int; Code int }`
  - `type DayPoint struct { Date time.Time; HiF, LoF float64; PrecipProb int; Code int }`
  - `type Weather struct { Current Conditions; Hourly []HourPoint; Daily []DayPoint }`
  - `func ParseOpenMeteo(body []byte, loc *time.Location) (*Weather, error)`
  - `func Fetch(ctx context.Context, c *config.Config) (*Weather, error)`

Only Open-Meteo is ported (no API key). The Pi's OpenWeather path, `owCache`, and `past_temps.json` history are dropped for now — the temperature curve starts at "now" rather than extending into the past. Revisit if the curve looks wrong without it.

- [ ] **Step 1: Create the fixture**

Fetch a real response so the fixture matches the live schema:

```bash
mkdir -p internal/weather/testdata
curl -s 'https://api.open-meteo.com/v1/forecast?latitude=38.5693&longitude=-92.1629&current=temperature_2m,weather_code,is_day&hourly=temperature_2m,precipitation_probability,weather_code&daily=temperature_2m_max,temperature_2m_min,precipitation_probability_max,weather_code&temperature_unit=fahrenheit&timezone=America%2FChicago&forecast_days=7' \
  -o internal/weather/testdata/openmeteo.json
head -c 400 internal/weather/testdata/openmeteo.json
```

Expected: JSON containing `"current"`, `"hourly"`, and `"daily"` objects.

**If the network is unavailable,** hand-write `internal/weather/testdata/openmeteo.json` from the schema below instead — the field names must match exactly, since that is what the parser binds to. Include at least 24 hourly entries and exactly 7 daily entries so `TestParseOpenMeteo`'s length assertion holds:

```json
{
  "current": {"time": "2026-08-25T10:00", "temperature_2m": 72.4, "weather_code": 2, "is_day": 1},
  "hourly": {
    "time": ["2026-08-25T00:00", "2026-08-25T01:00"],
    "temperature_2m": [64.2, 63.8],
    "precipitation_probability": [0, 5],
    "weather_code": [0, 1]
  },
  "daily": {
    "time": ["2026-08-25", "2026-08-26"],
    "temperature_2m_max": [88.1, 90.3],
    "temperature_2m_min": [64.0, 66.2],
    "precipitation_probability_max": [20, 0],
    "weather_code": [2, 0]
  }
}
```

Note the shape: `hourly` and `daily` are objects of parallel arrays, not arrays of objects. Timestamps carry no zone suffix — they are local to the requested timezone, which is why `parseLocal` resolves them against a `*time.Location`.

- [ ] **Step 2: Write the failing test**

Create `internal/weather/weather_test.go`:

```go
package weather

import (
	"os"
	"testing"
	"time"
)

func TestParseOpenMeteo(t *testing.T) {
	b, err := os.ReadFile("testdata/openmeteo.json")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseOpenMeteo(b, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if w.Current.TempF == 0 {
		t.Error("Current.TempF is zero; expected a parsed temperature")
	}
	if len(w.Hourly) == 0 {
		t.Fatal("Hourly is empty")
	}
	if len(w.Daily) != 7 {
		t.Errorf("len(Daily) = %d, want 7", len(w.Daily))
	}
	// Hourly must be chronological — the chart depends on ordering.
	for i := 1; i < len(w.Hourly); i++ {
		if !w.Hourly[i].Time.After(w.Hourly[i-1].Time) {
			t.Fatalf("Hourly not ascending at %d: %v then %v",
				i, w.Hourly[i-1].Time, w.Hourly[i].Time)
		}
	}
	for i, d := range w.Daily {
		if d.HiF < d.LoF {
			t.Errorf("Daily[%d]: HiF %.1f < LoF %.1f", i, d.HiF, d.LoF)
		}
	}
}

func TestParseOpenMeteoRejectsGarbage(t *testing.T) {
	if _, err := ParseOpenMeteo([]byte(`{"nope":true}`), time.UTC); err == nil {
		t.Fatal("expected error when hourly data is absent, got nil")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/weather/`
Expected: FAIL — `undefined: ParseOpenMeteo`

- [ ] **Step 4: Write the implementation**

Create `internal/weather/weather.go`:

```go
// Package weather fetches and normalizes forecast data from Open-Meteo.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

const openMeteoURL = "https://api.open-meteo.com/v1/forecast"

// Conditions is the current observation.
type Conditions struct {
	Time   time.Time
	TempF  float64
	Code   int // WMO weather code
	IsDay  bool
}

// HourPoint is one hourly forecast sample.
type HourPoint struct {
	Time       time.Time
	TempF      float64
	PrecipProb int
	Code       int
}

// DayPoint is one daily forecast summary.
type DayPoint struct {
	Date       time.Time
	HiF, LoF   float64
	PrecipProb int
	Code       int
}

// Weather is the normalized forecast the rest of the app consumes.
type Weather struct {
	Current Conditions
	Hourly  []HourPoint
	Daily   []DayPoint
}

type omResponse struct {
	Current struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		WeatherCode int     `json:"weather_code"`
		IsDay       int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time          []string  `json:"time"`
		Temperature   []float64 `json:"temperature_2m"`
		PrecipProb    []int     `json:"precipitation_probability"`
		WeatherCode   []int     `json:"weather_code"`
	} `json:"hourly"`
	Daily struct {
		Time           []string  `json:"time"`
		TempMax        []float64 `json:"temperature_2m_max"`
		TempMin        []float64 `json:"temperature_2m_min"`
		PrecipProbMax  []int     `json:"precipitation_probability_max"`
		WeatherCode    []int     `json:"weather_code"`
	} `json:"daily"`
}

// ParseOpenMeteo normalizes an Open-Meteo response. Timestamps are local-naive
// (no zone suffix), so they are interpreted in loc.
func ParseOpenMeteo(body []byte, loc *time.Location) (*Weather, error) {
	var r omResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse open-meteo: %w", err)
	}
	if len(r.Hourly.Time) == 0 {
		return nil, fmt.Errorf("open-meteo response has no hourly data")
	}

	w := &Weather{}
	w.Current = Conditions{
		Time:  parseLocal(r.Current.Time, loc),
		TempF: r.Current.Temperature,
		Code:  r.Current.WeatherCode,
		IsDay: r.Current.IsDay == 1,
	}
	for i, ts := range r.Hourly.Time {
		p := HourPoint{Time: parseLocal(ts, loc)}
		if i < len(r.Hourly.Temperature) {
			p.TempF = r.Hourly.Temperature[i]
		}
		if i < len(r.Hourly.PrecipProb) {
			p.PrecipProb = r.Hourly.PrecipProb[i]
		}
		if i < len(r.Hourly.WeatherCode) {
			p.Code = r.Hourly.WeatherCode[i]
		}
		w.Hourly = append(w.Hourly, p)
	}
	for i, ts := range r.Daily.Time {
		d := DayPoint{Date: parseLocal(ts, loc)}
		if i < len(r.Daily.TempMax) {
			d.HiF = r.Daily.TempMax[i]
		}
		if i < len(r.Daily.TempMin) {
			d.LoF = r.Daily.TempMin[i]
		}
		if i < len(r.Daily.PrecipProbMax) {
			d.PrecipProb = r.Daily.PrecipProbMax[i]
		}
		if i < len(r.Daily.WeatherCode) {
			d.Code = r.Daily.WeatherCode[i]
		}
		w.Daily = append(w.Daily, d)
	}
	return w, nil
}

// parseLocal handles both "2006-01-02T15:04" and "2006-01-02" forms.
func parseLocal(s string, loc *time.Location) time.Time {
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Fetch retrieves the current forecast for the configured location.
func Fetch(ctx context.Context, c *config.Config) (*Weather, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%g", c.Location.Latitude))
	q.Set("longitude", fmt.Sprintf("%g", c.Location.Longitude))
	q.Set("current", "temperature_2m,weather_code,is_day")
	q.Set("hourly", "temperature_2m,precipitation_probability,weather_code")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_probability_max,weather_code")
	q.Set("temperature_unit", c.Units.Temperature)
	q.Set("timezone", c.Location.Timezone)
	q.Set("forecast_days", "7")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openMeteoURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ParseOpenMeteo(body, c.TimeLocation())
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/weather/ -v`
Expected: PASS for both tests.

- [ ] **Step 6: Commit**

```bash
git add internal/weather
git commit -m "feat: Open-Meteo fetch and normalization"
```

---

### Task 5: Timeline geometry

**Files:**
- Create: `internal/model/timeline.go`
- Create: `internal/model/timeline_test.go`

**Interfaces:**
- Consumes: `calendar.Event`
- Produces:
  - `type Window struct { Start, End time.Time; WidthPx float64 }`
  - `func (w Window) X(t time.Time) float64` — linear time→X
  - `func ComputeWindow(events []calendar.Event, now time.Time, widthPx float64, opts WindowOpts) Window`
  - `type WindowOpts struct { FitEvents int; PastContext, MinSpan, MaxSpan time.Duration }`
  - `type Block struct { Event calendar.Event; X, W float64 }`
  - `func Blocks(events []calendar.Event, w Window, minWidthPx float64) []Block`

This is the core of the design: a **strictly linear** axis. Cards are sized by duration; a `minWidthPx` visibility floor keeps a 5-minute event from rendering sub-pixel, but is small enough (default 4px) that axis distortion stays imperceptible. Legibility comes from floating labels (Task 6), not from widening cards.

- [ ] **Step 1: Write the failing test**

Create `internal/model/timeline_test.go`:

```go
package model

import (
	"math"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

var base = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func ev(title string, startMin, durMin int) calendar.Event {
	s := base.Add(time.Duration(startMin) * time.Minute)
	return calendar.Event{
		Title: title,
		Start: s,
		End:   s.Add(time.Duration(durMin) * time.Minute),
	}
}

func closeTo(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestWindowXIsLinear(t *testing.T) {
	w := Window{Start: base, End: base.Add(4 * time.Hour), WidthPx: 1000}
	if got := w.X(base); !closeTo(got, 0, 0.001) {
		t.Errorf("X(start) = %v, want 0", got)
	}
	if got := w.X(base.Add(4 * time.Hour)); !closeTo(got, 1000, 0.001) {
		t.Errorf("X(end) = %v, want 1000", got)
	}
	if got := w.X(base.Add(2 * time.Hour)); !closeTo(got, 500, 0.001) {
		t.Errorf("X(midpoint) = %v, want 500", got)
	}
	// Equal durations must map to equal widths anywhere on the axis.
	d1 := w.X(base.Add(30*time.Minute)) - w.X(base)
	d2 := w.X(base.Add(3*time.Hour+30*time.Minute)) - w.X(base.Add(3*time.Hour))
	if !closeTo(d1, d2, 0.001) {
		t.Errorf("axis not linear: %v vs %v", d1, d2)
	}
}

func TestComputeWindowFitsRequestedEvents(t *testing.T) {
	evs := []calendar.Event{
		ev("a", 30, 30), ev("b", 120, 60), ev("c", 300, 30), ev("d", 600, 30),
	}
	opts := WindowOpts{
		FitEvents:   3,
		PastContext: 30 * time.Minute,
		MinSpan:     2 * time.Hour,
		MaxSpan:     12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if !w.Start.Equal(base.Add(-30 * time.Minute)) {
		t.Errorf("Start = %v, want now-30m", w.Start)
	}
	// Must cover the 3rd event's end (300+30 min).
	third := base.Add(330 * time.Minute)
	if w.End.Before(third) {
		t.Errorf("End = %v, does not cover 3rd event ending %v", w.End, third)
	}
}

func TestComputeWindowClampsToMinSpan(t *testing.T) {
	evs := []calendar.Event{ev("soon", 10, 10)}
	opts := WindowOpts{
		FitEvents: 3, PastContext: 15 * time.Minute,
		MinSpan: 3 * time.Hour, MaxSpan: 12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if got := w.End.Sub(w.Start); got < 3*time.Hour {
		t.Errorf("span = %v, want >= MinSpan 3h", got)
	}
}

func TestComputeWindowClampsToMaxSpan(t *testing.T) {
	evs := []calendar.Event{ev("far", 60*40, 30)} // ~40h out
	opts := WindowOpts{
		FitEvents: 3, PastContext: 15 * time.Minute,
		MinSpan: 2 * time.Hour, MaxSpan: 12 * time.Hour,
	}
	w := ComputeWindow(evs, base, 1540, opts)
	if got := w.End.Sub(w.Start); got > 12*time.Hour {
		t.Errorf("span = %v, want <= MaxSpan 12h", got)
	}
}

func TestComputeWindowHandlesNoEvents(t *testing.T) {
	opts := WindowOpts{
		FitEvents: 3, PastContext: 15 * time.Minute,
		MinSpan: 4 * time.Hour, MaxSpan: 12 * time.Hour,
	}
	w := ComputeWindow(nil, base, 1540, opts)
	if got := w.End.Sub(w.Start); got < 4*time.Hour {
		t.Errorf("empty-calendar span = %v, want >= MinSpan", got)
	}
	if w.WidthPx != 1540 {
		t.Errorf("WidthPx = %v, want 1540", w.WidthPx)
	}
}

func TestBlocksAreDurationProportional(t *testing.T) {
	w := Window{Start: base, End: base.Add(4 * time.Hour), WidthPx: 1000}
	// 60m and 120m events: the second must be exactly twice as wide.
	evs := []calendar.Event{ev("hour", 0, 60), ev("two", 120, 120)}
	bs := Blocks(evs, w, 4)
	if len(bs) != 2 {
		t.Fatalf("len(Blocks) = %d, want 2", len(bs))
	}
	if !closeTo(bs[1].W, bs[0].W*2, 0.001) {
		t.Errorf("widths %v and %v are not 1:2", bs[0].W, bs[1].W)
	}
	if !closeTo(bs[0].X, 0, 0.001) {
		t.Errorf("first block X = %v, want 0", bs[0].X)
	}
}

func TestBlocksApplyVisibilityFloor(t *testing.T) {
	w := Window{Start: base, End: base.Add(12 * time.Hour), WidthPx: 1000}
	// 5 minutes of a 12h window is ~0.7px — below the floor.
	bs := Blocks([]calendar.Event{ev("tiny", 60, 5)}, w, 4)
	if len(bs) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1", len(bs))
	}
	if bs[0].W < 4 {
		t.Errorf("W = %v, want >= floor 4", bs[0].W)
	}
}

func TestBlocksExcludeEventsOutsideWindow(t *testing.T) {
	w := Window{Start: base, End: base.Add(2 * time.Hour), WidthPx: 1000}
	evs := []calendar.Event{
		ev("before", -120, 30),
		ev("inside", 30, 30),
		ev("after", 300, 30),
	}
	bs := Blocks(evs, w, 4)
	if len(bs) != 1 || bs[0].Event.Title != "inside" {
		var got []string
		for _, b := range bs {
			got = append(got, b.Event.Title)
		}
		t.Fatalf("Blocks = %v, want only [inside]", got)
	}
}

func TestBlocksIncludeStraddlingEvent(t *testing.T) {
	w := Window{Start: base, End: base.Add(2 * time.Hour), WidthPx: 1000}
	// Started before the window, still running inside it.
	bs := Blocks([]calendar.Event{ev("running", -30, 90)}, w, 4)
	if len(bs) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1 (in-progress event)", len(bs))
	}
	if bs[0].X < 0 {
		t.Errorf("X = %v, want clamped to >= 0", bs[0].X)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/model/`
Expected: FAIL — `undefined: Window`

- [ ] **Step 3: Write the implementation**

Create `internal/model/timeline.go`:

```go
// Package model turns fetched data into a positioned view model. Everything
// here is pure: time is always a parameter, never time.Now().
package model

import (
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// Window is the visible time span mapped onto a pixel width.
type Window struct {
	Start, End time.Time
	WidthPx    float64
}

// X maps a time to its horizontal pixel offset. The mapping is strictly linear,
// so equal durations always occupy equal widths and every row (agenda, weather)
// can share one axis.
func (w Window) X(t time.Time) float64 {
	span := w.End.Sub(w.Start).Seconds()
	if span <= 0 {
		return 0
	}
	return w.WidthPx * (t.Sub(w.Start).Seconds() / span)
}

// WindowOpts tunes how the visible span is chosen.
type WindowOpts struct {
	FitEvents   int           // aim to show this many upcoming events
	PastContext time.Duration // keep this much already-elapsed time visible
	MinSpan     time.Duration // never zoom in tighter than this
	MaxSpan     time.Duration // never zoom out wider than this
}

// ComputeWindow picks a visible span that fits the next FitEvents events,
// clamped to [MinSpan, MaxSpan] so neither an imminent meeting nor a distant
// one distorts the scale.
func ComputeWindow(events []calendar.Event, now time.Time, widthPx float64, opts WindowOpts) Window {
	start := now.Add(-opts.PastContext)
	end := start.Add(opts.MinSpan)

	var seen int
	for _, e := range events {
		if e.AllDay || !e.End.After(now) {
			continue
		}
		seen++
		if e.End.After(end) {
			end = e.End
		}
		if seen >= opts.FitEvents {
			break
		}
	}

	if span := end.Sub(start); span < opts.MinSpan {
		end = start.Add(opts.MinSpan)
	} else if opts.MaxSpan > 0 && span > opts.MaxSpan {
		end = start.Add(opts.MaxSpan)
	}
	return Window{Start: start, End: end, WidthPx: widthPx}
}

// Block is one event positioned on the axis.
type Block struct {
	Event calendar.Event
	X, W  float64
}

// Blocks positions timed events that overlap the window. Widths are strictly
// duration-proportional except for minWidthPx, a visibility floor that keeps a
// very short event from rendering sub-pixel.
func Blocks(events []calendar.Event, w Window, minWidthPx float64) []Block {
	var out []Block
	for _, e := range events {
		if e.AllDay {
			continue
		}
		// Overlap test: any part of the event inside the window.
		if !e.End.After(w.Start) || !e.Start.Before(w.End) {
			continue
		}
		x := w.X(e.Start)
		width := w.X(e.End) - x
		if x < 0 { // in progress: clamp the left edge, keep the visible remainder
			width += x
			x = 0
		}
		if width < minWidthPx {
			width = minWidthPx
		}
		out = append(out, Block{Event: e, X: x, W: width})
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/ -v`
Expected: PASS for all nine tests.

- [ ] **Step 5: Commit**

```bash
git add internal/model/timeline.go internal/model/timeline_test.go
git commit -m "feat: linear timeline axis with duration-proportional blocks"
```

---

### Task 6: Floating label placement

**Files:**
- Create: `internal/model/labels.go`
- Create: `internal/model/labels_test.go`

**Interfaces:**
- Consumes: `Block` (Task 5)
- Produces:
  - `type Label struct { Text string; X, W float64; Row int; Anchor float64 }`
  - `func PlaceLabels(blocks []Block, measure func(string) float64, opts LabelOpts) []Label`
  - `type LabelOpts struct { MaxRows int; Gap, MaxDrift, TrackWidth float64 }`

This is what makes a strictly linear axis readable. A label is placed at its block's left edge and may overflow the block. Walking left→right: if a label would overlap the previous one on its row, push it right; if pushing exceeds `MaxDrift`, demote it to the next row instead. `Anchor` records the block's own X so the renderer can draw a leader mark when the label has drifted.

`measure` is injected so this stays pure and testable — production passes a text-width estimator, tests pass a deterministic stub.

- [ ] **Step 1: Write the failing test**

Create `internal/model/labels_test.go`:

```go
package model

import (
	"testing"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// fixed returns a measure func where every character is w px wide.
func fixed(w float64) func(string) float64 {
	return func(s string) float64 { return float64(len(s)) * w }
}

func blk(title string, x, wpx float64) Block {
	return Block{Event: calendar.Event{Title: title}, X: x, W: wpx}
}

func defaultOpts() LabelOpts {
	return LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 60, TrackWidth: 1000}
}

func TestPlaceLabelsKeepsWellSpacedLabelsOnRowZero(t *testing.T) {
	bs := []Block{blk("aaa", 0, 100), blk("bbb", 400, 100), blk("ccc", 800, 100)}
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if len(ls) != 3 {
		t.Fatalf("len = %d, want 3", len(ls))
	}
	for i, l := range ls {
		if l.Row != 0 {
			t.Errorf("label %d Row = %d, want 0 (no crowding)", i, l.Row)
		}
		if l.X != bs[i].X {
			t.Errorf("label %d X = %v, want anchor %v", i, l.X, bs[i].X)
		}
	}
}

func TestPlaceLabelsPushesOverlappingLabelRight(t *testing.T) {
	// "aaaaa" is 50px wide at x=0; the next block starts at 30 — overlap.
	bs := []Block{blk("aaaaa", 0, 20), blk("bbbbb", 30, 20)}
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if ls[1].Row != 0 {
		t.Fatalf("second label Row = %d, want 0 (small push, no demotion)", ls[1].Row)
	}
	wantMin := ls[0].X + ls[0].W + 8 // Gap
	if ls[1].X < wantMin {
		t.Errorf("second label X = %v, want >= %v (pushed clear)", ls[1].X, wantMin)
	}
}

func TestPlaceLabelsDemotesWhenDriftExceedsMax(t *testing.T) {
	// A long label followed immediately by another forces a >MaxDrift push.
	bs := []Block{blk("aaaaaaaaaaaaaaaaaaaa", 0, 10), blk("bbb", 5, 10)}
	opts := defaultOpts()
	opts.MaxDrift = 20
	ls := PlaceLabels(bs, fixed(10), opts)
	if ls[1].Row != 1 {
		t.Errorf("second label Row = %d, want 1 (drift exceeded MaxDrift)", ls[1].Row)
	}
	if ls[1].X != 5 {
		t.Errorf("demoted label X = %v, want its anchor 5", ls[1].X)
	}
}

func TestPlaceLabelsNeverExceedsMaxRows(t *testing.T) {
	var bs []Block
	for i := 0; i < 8; i++ {
		bs = append(bs, blk("aaaaaaaaaa", float64(i)*3, 5))
	}
	opts := defaultOpts()
	opts.MaxDrift = 5
	ls := PlaceLabels(bs, fixed(10), opts)
	for i, l := range ls {
		if l.Row >= opts.MaxRows {
			t.Errorf("label %d Row = %d, want < MaxRows %d", i, l.Row, opts.MaxRows)
		}
	}
}

func TestPlaceLabelsRecordsAnchor(t *testing.T) {
	bs := []Block{blk("aaaaa", 0, 20), blk("bbbbb", 30, 20)}
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if ls[1].Anchor != 30 {
		t.Errorf("Anchor = %v, want the block's own X 30", ls[1].Anchor)
	}
}

func TestPlaceLabelsClampsToTrackWidth(t *testing.T) {
	bs := []Block{blk("aaaaaaaaaa", 960, 20)} // 100px label starting at 960 in a 1000 track
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if got := ls[0].X + ls[0].W; got > 1000 {
		t.Errorf("label right edge = %v, want <= TrackWidth 1000", got)
	}
}

func TestPlaceLabelsHandlesEmptyInput(t *testing.T) {
	if ls := PlaceLabels(nil, fixed(10), defaultOpts()); len(ls) != 0 {
		t.Errorf("len = %d, want 0", len(ls))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/model/ -run TestPlaceLabels`
Expected: FAIL — `undefined: PlaceLabels`

- [ ] **Step 3: Write the implementation**

Create `internal/model/labels.go`:

```go
package model

// Label is an event title positioned independently of its block. Because the
// time axis is strictly linear, a short meeting's block can be far narrower
// than its title; the label floats free and may overflow the block.
type Label struct {
	Text   string
	X, W   float64
	Row    int
	Anchor float64 // the block's own X, for drawing a leader when drifted
}

// LabelOpts tunes label placement.
type LabelOpts struct {
	MaxRows    int     // hard cap on stacked label rows
	Gap        float64 // minimum horizontal space between labels on a row
	MaxDrift   float64 // how far a label may be pushed before it is demoted
	TrackWidth float64 // labels are clamped to this width
}

// PlaceLabels positions one label per block, avoiding overlap. Labels are laid
// out left to right; a label that would collide is pushed right, and demoted to
// the next row if pushing would drift it more than MaxDrift from its anchor.
func PlaceLabels(blocks []Block, measure func(string) float64, opts LabelOpts) []Label {
	if len(blocks) == 0 {
		return nil
	}
	if opts.MaxRows < 1 {
		opts.MaxRows = 1
	}
	rowEnd := make([]float64, opts.MaxRows) // right edge occupied per row
	for i := range rowEnd {
		rowEnd[i] = -1e9
	}

	out := make([]Label, 0, len(blocks))
	for _, b := range blocks {
		text := b.Event.Title
		w := measure(text)
		if w > opts.TrackWidth {
			w = opts.TrackWidth
		}

		placed := false
		var lab Label
		for row := 0; row < opts.MaxRows; row++ {
			x := b.X
			if min := rowEnd[row] + opts.Gap; x < min {
				x = min
			}
			// Only the drift caused by pushing counts; demote if it is too far.
			if x-b.X > opts.MaxDrift && row < opts.MaxRows-1 {
				continue
			}
			if x+w > opts.TrackWidth {
				x = opts.TrackWidth - w
				if x < 0 {
					x = 0
				}
			}
			lab = Label{Text: text, X: x, W: w, Row: row, Anchor: b.X}
			rowEnd[row] = x + w
			placed = true
			break
		}
		if !placed { // every row drifted too far: accept the last row anyway
			row := opts.MaxRows - 1
			x := rowEnd[row] + opts.Gap
			if x+w > opts.TrackWidth {
				x = opts.TrackWidth - w
				if x < 0 {
					x = 0
				}
			}
			lab = Label{Text: text, X: x, W: w, Row: row, Anchor: b.X}
			rowEnd[row] = x + w
		}
		out = append(out, lab)
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/ -v -run TestPlaceLabels`
Expected: PASS for all seven tests.

- [ ] **Step 5: Run the whole model package**

Run: `go test ./internal/model/`
Expected: PASS (Task 5's tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/model/labels.go internal/model/labels_test.go
git commit -m "feat: floating label placement with row demotion"
```

---

### Task 7: View model assembly

**Files:**
- Create: `internal/model/model.go`
- Create: `internal/model/model_test.go`

**Interfaces:**
- Consumes: `config.Config`, `calendar.Event`, `weather.Weather`, `Window`/`Blocks`/`PlaceLabels`
- Produces:
  - `type ViewModel struct { Now time.Time; ClockTime, ClockDate string; LocationName string; Current *weather.Conditions; NextEvent *calendar.Event; UntilNext string; Window Window; Blocks []Block; Labels []Label; AllDay []calendar.Event; Forecast []weather.DayPoint; NowX float64; Stale bool; Errors []string }`
  - `func Build(now time.Time, c *config.Config, evs []calendar.Event, w *weather.Weather, errs []string) ViewModel`

`Build` is the single place that turns raw data into everything the template needs. It must tolerate nil weather and empty events — a fetch failure at boot must still render a clock.

- [ ] **Step 1: Write the failing test**

Create `internal/model/model_test.go`:

```go
package model

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func testConfig() *config.Config {
	c := &config.Config{}
	c.Location.Name = "Home"
	c.Location.Timezone = "UTC"
	c.Units.Clock24h = false
	return c
}

func TestBuildFormatsClock(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, nil)
	if vm.ClockTime != "2:05" {
		t.Errorf("ClockTime = %q, want 2:05", vm.ClockTime)
	}
	if vm.ClockDate != "Tuesday, August 25" {
		t.Errorf("ClockDate = %q", vm.ClockDate)
	}
}

func TestBuildFormatsClock24h(t *testing.T) {
	c := testConfig()
	c.Units.Clock24h = true
	now := time.Date(2026, 8, 25, 14, 5, 0, 0, time.UTC)
	vm := Build(now, c, nil, nil, nil)
	if vm.ClockTime != "14:05" {
		t.Errorf("ClockTime = %q, want 14:05", vm.ClockTime)
	}
}

func TestBuildSurvivesNilWeatherAndEvents(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	vm := Build(now, testConfig(), nil, nil, []string{"weather: timeout"})
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
	vm := Build(now, testConfig(), evs, nil, nil)
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
	vm := Build(now, testConfig(), evs, nil, nil)
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
	vm := Build(now, testConfig(), evs, nil, nil)
	if len(vm.AllDay) != 1 || vm.AllDay[0].Title != "holiday" {
		t.Errorf("AllDay = %v, want [holiday]", vm.AllDay)
	}
	for _, b := range vm.Blocks {
		if b.Event.AllDay {
			t.Error("all-day event leaked into Blocks")
		}
	}
}

func TestBuildComputesNowX(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	evs := []calendar.Event{{Title: "e", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}
	vm := Build(now, testConfig(), evs, nil, nil)
	if vm.NowX <= 0 {
		t.Errorf("NowX = %v, want > 0 (past context puts now inside the window)", vm.NowX)
	}
	if vm.NowX != vm.Window.X(now) {
		t.Errorf("NowX = %v, inconsistent with Window.X(now) = %v", vm.NowX, vm.Window.X(now))
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
	vm := Build(now, testConfig(), nil, w, nil)
	if vm.Current == nil || vm.Current.TempF != 72 {
		t.Errorf("Current = %v, want 72F", vm.Current)
	}
	if len(vm.Forecast) != 2 {
		t.Errorf("len(Forecast) = %d, want 2", len(vm.Forecast))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/model/ -run TestBuild`
Expected: FAIL — `undefined: Build`

- [ ] **Step 3: Write the implementation**

Create `internal/model/model.go`:

```go
package model

import (
	"fmt"
	"sort"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// Layout constants for the 1920x480 panel. The left "now block" is fixed width;
// the timeline occupies the rest.
const (
	PanelWidth  = 1920
	PanelHeight = 480
	NowBlockW   = 380
	TrackWidth  = PanelWidth - NowBlockW // 1540
)

// Timeline tuning. Starting values carried over from the Pi dashboard
// (GAP_MIN/IMMINENT_MIN); adjust against real renders.
var (
	defaultWindow = WindowOpts{
		FitEvents:   4,
		PastContext: 20 * time.Minute,
		MinSpan:     4 * time.Hour,
		MaxSpan:     12 * time.Hour,
	}
	defaultLabels = LabelOpts{
		MaxRows: 2, Gap: 10, MaxDrift: 80, TrackWidth: TrackWidth,
	}
	minBlockPx = 4.0 // visibility floor, not a layout min-width
)

// ViewModel is everything the template needs. No method on it may consult the
// clock; every time-dependent value is resolved in Build.
type ViewModel struct {
	Now       time.Time
	ClockTime string
	ClockDate string

	LocationName string
	Current      *weather.Conditions
	Forecast     []weather.DayPoint

	NextEvent *calendar.Event
	UntilNext string

	Window Window
	Blocks []Block
	Labels []Label
	AllDay []calendar.Event
	NowX   float64

	Stale  bool
	Errors []string
}

// Build assembles the view model. It tolerates nil weather and no events so a
// boot with no network still renders a clock.
func Build(now time.Time, c *config.Config, evs []calendar.Event, w *weather.Weather, errs []string) ViewModel {
	loc := c.TimeLocation()
	local := now.In(loc)

	clockLayout := "3:04"
	if c.Units.Clock24h {
		clockLayout = "15:04"
	}

	vm := ViewModel{
		Now:          now,
		ClockTime:    local.Format(clockLayout),
		ClockDate:    local.Format("Monday, January 2"),
		LocationName: c.Location.Name,
		Errors:       errs,
		Stale:        w == nil && len(evs) == 0,
	}

	if w != nil {
		cur := w.Current
		vm.Current = &cur
		vm.Forecast = w.Daily
	}

	sorted := append([]calendar.Event(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })

	var timed []calendar.Event
	for _, e := range sorted {
		if e.AllDay {
			vm.AllDay = append(vm.AllDay, e)
		} else {
			timed = append(timed, e)
		}
	}

	// Next up: an in-progress event wins over the next one to start.
	for i := range timed {
		if timed[i].End.After(now) {
			vm.NextEvent = &timed[i]
			vm.UntilNext = untilText(now, timed[i].Start)
			break
		}
	}

	vm.Window = ComputeWindow(timed, now, TrackWidth, defaultWindow)
	vm.Blocks = Blocks(timed, vm.Window, minBlockPx)
	vm.Labels = PlaceLabels(vm.Blocks, estimateTextWidth, defaultLabels)
	vm.NowX = vm.Window.X(now)
	return vm
}

// untilText renders the wait until an event starts; an already-started event
// reads as "now".
func untilText(now, start time.Time) string {
	d := start.Sub(now)
	if d <= 0 {
		return "now"
	}
	mins := int(d.Minutes())
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}

// estimateTextWidth approximates rendered label width. Labels are 19px and the
// UI font averages ~0.52em per character; exact metrics are not needed because
// placement only has to avoid visible collisions.
func estimateTextWidth(s string) float64 {
	const avgCharPx = 19 * 0.52
	return float64(len([]rune(s))) * avgCharPx
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/ -v -run TestBuild`
Expected: PASS for all eight tests.

- [ ] **Step 5: Run the whole package**

Run: `go test ./internal/model/`
Expected: PASS (Tasks 5 and 6 still green).

- [ ] **Step 6: Commit**

```bash
git add internal/model/model.go internal/model/model_test.go
git commit -m "feat: view model assembly"
```

---

### Task 8: Weather icons and SVG chart

**Files:**
- Create: `internal/chart/chart.go`
- Create: `internal/chart/chart_test.go`
- Create: `internal/chart/icons.go`
- Create: `internal/chart/icons/*.svg` (11 files, listed below)
- Create: `internal/chart/testdata/hourly.svg`

**Interfaces:**
- Consumes: `weather.Weather`, `model.Window`
- Produces:
  - `func Icon(code int) string` — WMO code → inline SVG markup
  - `func Hourly(w *weather.Weather, win model.Window, heightPx float64) string` — SVG for the hourly temp/precip strip

**This is a correctness fix, not decoration.** The board has only DejaVu and Liberation. Of the 9 weather emoji the Pi uses, 3 render as monochrome glyphs and **6 render as nothing at all** — silently dropped. Icons must therefore be SVG.

Icon files, keyed by WMO code, each a 24×24 `<svg>` with `fill="currentColor"` and no `<style>`:

| File | WMO codes | Depicts |
|---|---|---|
| `clear.svg` | 0 | sun |
| `mainly-clear.svg` | 1 | sun with small cloud |
| `partly-cloudy.svg` | 2 | sun behind cloud |
| `overcast.svg` | 3 | full cloud |
| `fog.svg` | 45, 48 | cloud with horizontal lines |
| `drizzle.svg` | 51, 53, 55 | cloud with short dashes |
| `rain.svg` | 61, 63, 65, 80, 81 | cloud with drops |
| `snow.svg` | 71, 73 | cloud with flakes |
| `heavy-snow.svg` | 75 | dense flakes |
| `showers.svg` | 82 | cloud with heavy drops |
| `thunderstorm.svg` | 95, 96, 99 | cloud with bolt |

- [ ] **Step 1: Write the failing test**

Create `internal/chart/chart_test.go`:

```go
package chart

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestIconReturnsSVGForKnownCodes(t *testing.T) {
	for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 75, 82, 95} {
		got := Icon(code)
		if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
			t.Errorf("Icon(%d) = %.40q, want inline <svg>", code, got)
		}
	}
}

func TestIconFallsBackForUnknownCode(t *testing.T) {
	got := Icon(9999)
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Errorf("Icon(unknown) = %.40q, want a fallback <svg>", got)
	}
}

func TestIconNeverReturnsEmoji(t *testing.T) {
	// The board cannot render these; a regression here is invisible on screen.
	for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 75, 82, 95} {
		for _, r := range Icon(code) {
			if r > 0x2000 {
				t.Errorf("Icon(%d) contains non-ASCII rune %q", code, r)
			}
		}
	}
}

func hourlyFixture() *weather.Weather {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w := &weather.Weather{Current: weather.Conditions{TempF: 70}}
	temps := []float64{68, 70, 73, 77, 80, 82, 81, 78}
	probs := []int{0, 10, 20, 40, 60, 30, 10, 0}
	for i := range temps {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: base.Add(time.Duration(i) * time.Hour), TempF: temps[i], PrecipProb: probs[i],
		})
	}
	return w
}

func TestHourlyProducesSVG(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(hourlyFixture(), win, 104)
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Fatalf("Hourly = %.60q, want <svg>", got)
	}
	if !strings.Contains(got, "<path") {
		t.Error("expected a <path> for the temperature curve")
	}
	if !strings.Contains(got, "<rect") {
		t.Error("expected <rect> bars for precipitation")
	}
}

func TestHourlyHandlesNoData(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(&weather.Weather{}, win, 104)
	if strings.Contains(got, "NaN") {
		t.Error("empty weather produced NaN coordinates")
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "<svg") {
		t.Error("expected a valid empty <svg>, not a panic or blank string")
	}
}

func TestHourlyMatchesGolden(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	win := model.Window{Start: base, End: base.Add(8 * time.Hour), WidthPx: 1540}
	got := Hourly(hourlyFixture(), win, 104)

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/hourly.svg", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile("testdata/hourly.svg")
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if got != string(want) {
		t.Errorf("SVG differs from golden.\n got: %.200q\nwant: %.200q", got, string(want))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/chart/`
Expected: FAIL — `undefined: Icon`

- [ ] **Step 3: Author the icon set**

Create the 11 files listed above in `internal/chart/icons/`. Each is a 24×24 SVG using `currentColor` so CSS controls the color. Example — `internal/chart/icons/clear.svg`:

```xml
<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4.5" fill="currentColor" stroke="none"/><path d="M12 2v2.5M12 19.5V22M2 12h2.5M19.5 12H22M4.9 4.9l1.8 1.8M17.3 17.3l1.8 1.8M19.1 4.9l-1.8 1.8M6.7 17.3l-1.8 1.8"/></svg>
```

`internal/chart/icons/overcast.svg`:

```xml
<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6.5 18a4 4 0 0 1 .4-8 5.5 5.5 0 0 1 10.5 1.4A3.5 3.5 0 0 1 17 18z" fill="currentColor" stroke="none"/></svg>
```

`internal/chart/icons/rain.svg`:

```xml
<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6.5 14a4 4 0 0 1 .4-8 5.5 5.5 0 0 1 10.5 1.4A3.5 3.5 0 0 1 17 14z" fill="currentColor" stroke="none"/><path d="M8 17.5l-1 3M12 17.5l-1 3M16 17.5l-1 3"/></svg>
```

Author the remaining eight in the same style: `mainly-clear.svg`, `partly-cloudy.svg`, `fog.svg`, `drizzle.svg`, `snow.svg`, `heavy-snow.svg`, `showers.svg`, `thunderstorm.svg`. Keep every one a single line with no `<style>` element and no emoji.

- [ ] **Step 4: Write the icon lookup**

Create `internal/chart/icons.go`:

```go
package chart

import (
	"embed"
	"sync"
)

//go:embed icons/*.svg
var iconFS embed.FS

var (
	iconOnce  sync.Once
	iconCache map[string]string
)

// wmoIcon maps a WMO weather code to an icon file stem.
func wmoIcon(code int) string {
	switch code {
	case 0:
		return "clear"
	case 1:
		return "mainly-clear"
	case 2:
		return "partly-cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 61, 63, 65, 80, 81:
		return "rain"
	case 71, 73:
		return "snow"
	case 75:
		return "heavy-snow"
	case 82:
		return "showers"
	case 95, 96, 99:
		return "thunderstorm"
	default:
		return "overcast"
	}
}

// Icon returns inline SVG markup for a WMO weather code. Never returns emoji:
// the board has no emoji font and missing glyphs render as nothing at all.
func Icon(code int) string {
	iconOnce.Do(func() {
		iconCache = map[string]string{}
		entries, _ := iconFS.ReadDir("icons")
		for _, e := range entries {
			b, err := iconFS.ReadFile("icons/" + e.Name())
			if err != nil {
				continue
			}
			stem := e.Name()[:len(e.Name())-len(".svg")]
			iconCache[stem] = string(b)
		}
	})
	if s, ok := iconCache[wmoIcon(code)]; ok {
		return s
	}
	return `<svg viewBox="0 0 24 24" width="24" height="24"></svg>`
}
```

- [ ] **Step 5: Write the chart generator**

Create `internal/chart/chart.go`:

```go
// Package chart renders weather data as SVG. SVG rather than <canvas> because
// omnidoc has no JavaScript engine, and rather than emoji/PNG because the
// board has no emoji font and vector art scales cleanly.
package chart

import (
	"fmt"
	"strings"

	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

const labelBandPx = 24 // reserved at the bottom for hour labels

// Hourly renders the temperature curve and precipitation bars across the same
// time window the agenda uses, so the two rows align on one axis.
func Hourly(w *weather.Weather, win model.Window, heightPx float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" class="wx-chart">`,
		win.WidthPx, heightPx, win.WidthPx, heightPx)

	// Every return closes the element explicitly. A deferred WriteString would
	// not work: b.String() is evaluated before deferred calls run.
	chartH := heightPx - labelBandPx
	if w == nil || len(w.Hourly) == 0 || chartH <= 0 {
		return b.String() + "</svg>"
	}

	// Only points inside the window matter.
	type pt struct{ x, temp float64; prob int }
	var pts []pt
	minT, maxT := w.Hourly[0].TempF, w.Hourly[0].TempF
	for _, h := range w.Hourly {
		if h.Time.Before(win.Start) || h.Time.After(win.End) {
			continue
		}
		pts = append(pts, pt{x: win.X(h.Time), temp: h.TempF, prob: h.PrecipProb})
		if h.TempF < minT {
			minT = h.TempF
		}
		if h.TempF > maxT {
			maxT = h.TempF
		}
	}
	if len(pts) == 0 {
		return b.String() + "</svg>"
	}
	if maxT-minT < 1 { // avoid divide-by-zero on a flat forecast
		maxT = minT + 1
	}
	tempY := func(t float64) float64 {
		return chartH - ((t-minT)/(maxT-minT))*(chartH*0.7) - chartH*0.15
	}

	// Precipitation bars first so the curve draws over them.
	barW := win.WidthPx / float64(max(len(pts), 1))
	for _, p := range pts {
		if p.prob <= 0 {
			continue
		}
		h := (float64(p.prob) / 100) * chartH * 0.5
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="wx-precip"/>`,
			p.x, chartH-h, barW*0.6, h)
	}

	// Temperature curve.
	b.WriteString(`<path class="wx-temp" d="`)
	for i, p := range pts {
		verb := "L"
		if i == 0 {
			verb = "M"
		}
		fmt.Fprintf(&b, "%s%.1f %.1f ", verb, p.x, tempY(p.temp))
	}
	b.WriteString(`"/>`)

	return b.String() + "</svg>"
}
```

Go 1.21+ has a builtin `max` for ordered types, so do not define one.

- [ ] **Step 6: Create the golden and run the tests**

Run: `mkdir -p internal/chart/testdata && UPDATE_GOLDEN=1 go test ./internal/chart/ -run TestHourlyMatchesGolden`
Then: `go test ./internal/chart/ -v`
Expected: PASS for all six tests. Inspect `internal/chart/testdata/hourly.svg` and confirm it opens in a browser as a recognizable curve before committing it.

- [ ] **Step 7: Commit**

```bash
git add internal/chart
git commit -m "feat: SVG weather icons and hourly chart

Icons are SVG because 6 of 9 weather emoji render as nothing on the board's
DejaVu/Liberation-only font set."
```

---

### Task 9: HTML rendering

**Files:**
- Create: `internal/view/view.go`
- Create: `internal/view/templates/dashboard.html`
- Create: `internal/view/assets/style.css`
- Create: `internal/view/view_test.go`
- Create: `internal/view/testdata/dashboard.html`

**Interfaces:**
- Consumes: `model.ViewModel`, `chart.Icon`, `chart.Hourly`
- Produces: `func Render(vm model.ViewModel) (string, error)`

CSS is adapted from `/Users/nas/code/upnext/pi-dashboard/web/style.css` (22K). Port the design tokens (colors, type scale, card treatment) but not the scrolling-rail rules — layout here is absolute positioning computed in Go.

- [ ] **Step 1: Write the failing test**

Create `internal/view/view_test.go`:

```go
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

// fixtureVM returns a populated view model and the weather it was built from;
// Render needs both (the chart reads the raw hourly series).
func fixtureVM(t *testing.T) (model.ViewModel, *weather.Weather) {
	t.Helper()
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Name = "Home"
	c.Location.Timezone = "UTC"

	evs := []calendar.Event{
		{Title: "Standup", Color: "#4f9cff", Start: now.Add(18 * time.Minute), End: now.Add(33 * time.Minute)},
		{Title: "Design Review", Color: "#ff7a59", Location: "Room B",
			Start: now.Add(90 * time.Minute), End: now.Add(150 * time.Minute)},
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
	return model.Build(now, c, evs, w, nil), w
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
		t.Error("output has a <script> tag; omnidoc discards scripts")
	}
}

func TestRenderIncludesEventTitlesAndClock(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Standup", "Design Review", "Company Holiday", "10:42"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderEscapesTitles(t *testing.T) {
	vm, w := fixtureVM(t)
	vm.Labels[0].Text = `Tom & Jerry <script>`
	got, err := Render(vm, w)
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
	got, err := Render(model.Build(now, c, nil, nil, []string{"weather: timeout"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "10:42") {
		t.Error("clock must render even with no data")
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/view/`
Expected: FAIL — `undefined: Render`

- [ ] **Step 3: Write the stylesheet**

Create `internal/view/assets/style.css`. Port the design tokens from the Pi's `web/style.css`:

```css
:root {
  --bg: #0b0d12;
  --panel: #141924;
  --border: #263041;
  --text: #e8ecf3;
  --text-mid: #8b97ab;
  --text-dim: #5b6678;
  --blue: #4f9cff;
}
* { box-sizing: border-box; }
html, body {
  margin: 0; padding: 0;
  width: 1920px; height: 480px;
  background: var(--bg); color: var(--text);
  font-family: "DejaVu Sans", sans-serif;
  overflow: hidden;
}
#app { display: flex; width: 1920px; height: 480px; }

/* Left: fixed-width now block */
#now-block {
  width: 380px; flex-shrink: 0; height: 480px;
  border-right: 1px solid var(--border);
  padding: 20px 22px; display: flex; flex-direction: column;
}
#clock-time { font-size: 78px; font-weight: 700; line-height: 1; letter-spacing: -2px; }
#clock-date { font-size: 19px; color: var(--text-mid); text-transform: uppercase;
  letter-spacing: 1px; margin-top: 10px; }
#now-wx { display: flex; align-items: center; gap: 10px; margin-top: 26px; font-size: 30px; }
#now-wx svg { width: 34px; height: 34px; color: var(--text-mid); }
#next-label { font-size: 15px; color: var(--text-dim); text-transform: uppercase;
  letter-spacing: 1px; margin-top: 30px; }
#next-title { font-size: 25px; font-weight: 600; margin-top: 6px; }
#next-when { font-size: 19px; color: var(--blue); margin-top: 4px; }

/* Right: timeline track */
#track { position: relative; width: 1540px; height: 480px; }
.ad-ribbon { position: absolute; top: 0; left: 0; height: 30px;
  display: flex; gap: 8px; padding: 5px 8px; }
.ad-pill { font-size: 15px; padding: 2px 10px; border-radius: 9px; }

.block { position: absolute; top: 118px; height: 54px; border-radius: 5px;
  background: var(--panel); border-left: 3px solid var(--blue); }
.label { position: absolute; font-size: 19px; white-space: nowrap; }
.label .lt { font-weight: 600; }
.label .lm { color: var(--text-mid); font-size: 15px; margin-left: 7px; }
.leader { position: absolute; top: 108px; width: 1px; height: 10px;
  background: var(--text-dim); }

#now-line { position: absolute; top: 34px; width: 2px; height: 300px;
  background: var(--blue); }
#now-line::after { content: "NOW"; position: absolute; top: -18px; left: -13px;
  font-size: 12px; color: var(--blue); letter-spacing: 1px; }

#wx { position: absolute; bottom: 96px; left: 0; }
.wx-chart { display: block; }
.wx-temp { fill: none; stroke: #ffb454; stroke-width: 2; }
.wx-precip { fill: rgba(79,156,255,0.35); }

#forecast { position: absolute; bottom: 0; left: 0; width: 1540px; height: 92px;
  display: flex; border-top: 1px solid var(--border); }
.fc-col { flex: 1; display: flex; flex-direction: column; align-items: center;
  justify-content: center; gap: 3px; border-right: 1px solid #1b2230; }
.fc-dow { font-size: 14px; color: var(--text-dim); text-transform: uppercase; }
.fc-col svg { width: 24px; height: 24px; color: var(--text-mid); }
.fc-hi { font-size: 21px; font-weight: 700; }
.fc-lo { font-size: 17px; color: var(--text-mid); margin-left: 5px; }
.fc-pp { font-size: 14px; color: var(--blue); }

#errors { position: absolute; top: 4px; right: 8px; font-size: 13px; color: #ff7a59; }
```

- [ ] **Step 4: Write the template**

Create `internal/view/templates/dashboard.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<title>UpNext</title>
<style>{{.CSS}}</style>
</head>
<body>
<div id="app">
  <aside id="now-block">
    <div id="clock-time">{{.VM.ClockTime}}</div>
    <div id="clock-date">{{.VM.ClockDate}}</div>
    {{with .VM.Current}}
    <div id="now-wx">{{icon .Code}}<span>{{printf "%.0f" .TempF}}&deg;</span></div>
    {{end}}
    {{with .VM.NextEvent}}
    <div id="next-label">Next</div>
    <div id="next-title">{{.Title}}</div>
    <div id="next-when">{{$.VM.UntilNext}}{{if .Location}} &middot; {{.Location}}{{end}}</div>
    {{end}}
  </aside>

  <div id="track">
    {{if .VM.AllDay}}
    <div class="ad-ribbon">
      {{range .VM.AllDay}}<div class="ad-pill" style="background:{{.Color}}22;color:{{.Color}}">{{.Title}}</div>{{end}}
    </div>
    {{end}}

    {{range .VM.Blocks}}
    <div class="block" style="left:{{px .X}};width:{{px .W}};border-left-color:{{.Event.Color}}"></div>
    {{end}}

    {{range .Labels}}
    {{if .Drifted}}<div class="leader" style="left:{{px .Anchor}}"></div>{{end}}
    <div class="label" style="left:{{px .X}};top:{{px .Top}}"><span class="lt">{{.Text}}</span></div>
    {{end}}

    <div id="now-line" style="left:{{px .VM.NowX}}"></div>

    <div id="wx" style="width:{{px .VM.Window.WidthPx}}">{{.Chart}}</div>

    <div id="forecast">
      {{range .Forecast}}
      <div class="fc-col">
        <div class="fc-dow">{{.Day}}</div>
        {{icon .Code}}
        <div><span class="fc-hi">{{.Hi}}&deg;</span><span class="fc-lo">{{.Lo}}&deg;</span></div>
        <div class="fc-pp">{{.Pop}}</div>
      </div>
      {{end}}
    </div>

    {{if .VM.Errors}}<div id="errors">{{range .VM.Errors}}{{.}} {{end}}</div>{{end}}
  </div>
</div>
</body>
</html>
```

- [ ] **Step 5: Write the renderer**

Create `internal/view/view.go`:

```go
// Package view renders the view model to a complete HTML document. CSS is
// inlined and SVG embedded: the board loads no external resources, and
// omnidoc discards <script>, so all layout must be static.
package view

import (
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/nathanstitt/luckfox-dashboard/internal/chart"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

//go:embed templates/*.html assets/*.css
var assetFS embed.FS

// labelRowTop is the y offset of each label row within the track.
var labelRowTop = []float64{78, 52}

type labelView struct {
	Text    string
	X       float64
	Top     float64
	Anchor  float64
	Drifted bool
}

type forecastView struct {
	Day  string
	Code int
	Hi   string
	Lo   string
	Pop  string
}

type pageData struct {
	VM       model.ViewModel
	CSS      template.CSS
	Chart    template.HTML
	Labels   []labelView
	Forecast []forecastView
}

var funcs = template.FuncMap{
	"px":   func(v float64) template.CSS { return template.CSS(fmt.Sprintf("%.1fpx", v)) },
	"icon": func(code int) template.HTML { return template.HTML(chart.Icon(code)) },
}

var tmpl = template.Must(
	template.New("dashboard.html").Funcs(funcs).ParseFS(assetFS, "templates/dashboard.html"),
)

// Render produces the full HTML document for a view model. The weather is
// passed separately because the chart needs the raw hourly series, which the
// view model does not carry.
func Render(vm model.ViewModel, w *weather.Weather) (string, error) {
	css, err := assetFS.ReadFile("assets/style.css")
	if err != nil {
		return "", err
	}

	labels := make([]labelView, 0, len(vm.Labels))
	for _, l := range vm.Labels {
		row := l.Row
		if row >= len(labelRowTop) {
			row = len(labelRowTop) - 1
		}
		labels = append(labels, labelView{
			Text: l.Text, X: l.X, Top: labelRowTop[row],
			Anchor: l.Anchor, Drifted: l.X-l.Anchor > 2,
		})
	}

	forecast := make([]forecastView, 0, len(vm.Forecast))
	for i, d := range vm.Forecast {
		day := d.Date.Format("Mon")
		if i == 0 {
			day = "Today"
		}
		pop := "—"
		if d.PrecipProb > 0 {
			pop = fmt.Sprintf("%d%%", d.PrecipProb)
		}
		forecast = append(forecast, forecastView{
			Day: day, Code: d.Code,
			Hi:  fmt.Sprintf("%.0f", d.HiF),
			Lo:  fmt.Sprintf("%.0f", d.LoF),
			Pop: pop,
		})
	}

	data := pageData{
		VM:       vm,
		CSS:      template.CSS(css),
		Chart:    template.HTML(chart.Hourly(w, vm.Window, 104)),
		Labels:   labels,
		Forecast: forecast,
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}
```

Two things to note, both already reflected in the code above:

- The template iterates `{{range .Labels}}`, not `.VM.Labels` — `Render` prepares
  `labelView` structs that carry the computed `Top` for each row.
- `chart.Hourly` output is wrapped in `template.HTML` so the SVG is emitted
  literally. Event titles stay as plain strings so `html/template` escapes them;
  never wrap user-supplied text in `template.HTML`.

- [ ] **Step 6: Create the golden and run the tests**

Run: `mkdir -p internal/view/testdata && UPDATE_GOLDEN=1 go test ./internal/view/ -run TestRenderMatchesGolden`
Then: `go test ./internal/view/ -v`
Expected: PASS for all five tests.

- [ ] **Step 7: Preview the golden in a browser**

Run: `open internal/view/testdata/dashboard.html`
Confirm: a 1920×480 dark dashboard with a clock on the left, event blocks with labels, a temperature curve, and a 7-column forecast. Fix layout problems now — it is far cheaper than debugging on the panel.

- [ ] **Step 8: Commit**

```bash
git add internal/view
git commit -m "feat: HTML rendering with inlined CSS and embedded SVG"
```

---

### Task 10: Framebuffer output

**Files:**
- Create: `internal/fb/fb.go`
- Create: `internal/fb/fb_test.go`

**Interfaces:**
- Consumes: HTML from `view.Render`
- Produces:
  - `func Geometry(dev string) (w, h int, err error)`
  - `func RenderHTML(ctx context.Context, html []byte, pageW, pageH int) (image.Image, error)`
  - `func Pack(img image.Image, fbW, fbH, rotate int) []byte`
  - `func Write(dev string, buf []byte) error`

`Pack` is separated from `Write` so the rotation math is testable without hardware. Verified constants: the framebuffer is 480×1920 XR24 (4 bytes/px, B,G,R,X little-endian), and `-rotate 270` maps `fb(x,y) ← canvas(pageW-1-y, x)`.

- [ ] **Step 1: Write the failing test**

Create `internal/fb/fb_test.go`:

```go
package fb

import (
	"image"
	"image/color"
	"testing"
)

func TestPackProducesCorrectSize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 480))
	buf := Pack(img, 480, 1920, 270)
	if len(buf) != 480*1920*4 {
		t.Errorf("len = %d, want %d", len(buf), 480*1920*4)
	}
}

func TestPackWritesBGRX(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	// Fill with pure red so byte order is unambiguous.
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	buf := Pack(img, 2, 4, 270)
	if buf[0] != 0 || buf[1] != 0 || buf[2] != 255 {
		t.Errorf("pixel bytes = %d,%d,%d; want B=0,G=0,R=255", buf[0], buf[1], buf[2])
	}
}

// Rotating 270 must map the canvas's top-left corner to a known framebuffer
// position. With fb(x,y) = canvas(pageW-1-y, x), canvas(0,0) lands at
// fb(x=0, y=pageW-1).
func TestPackRotates270(t *testing.T) {
	const pageW, pageH = 4, 2
	img := image.NewRGBA(image.Rect(0, 0, pageW, pageH))
	for y := 0; y < pageH; y++ {
		for x := 0; x < pageW; x++ {
			img.Set(x, y, color.RGBA{A: 255}) // black
		}
	}
	img.Set(0, 0, color.RGBA{G: 255, A: 255}) // mark the corner

	fbW, fbH := pageH, pageW // 2x4
	buf := Pack(img, fbW, fbH, 270)
	off := (pageW-1)*fbW*4 + 0*4
	if buf[off+1] != 255 {
		t.Errorf("green marker not at fb(0,%d); got B=%d G=%d R=%d",
			pageW-1, buf[off], buf[off+1], buf[off+2])
	}
}

func TestPackRotate0IsIdentity(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	buf := Pack(img, 2, 2, 0)
	if buf[0] != 255 {
		t.Errorf("blue pixel not at origin; got B=%d", buf[0])
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/fb/`
Expected: FAIL — `undefined: Pack`

- [ ] **Step 3: Write the implementation**

Create `internal/fb/fb.go`:

```go
// Package fb rasterizes HTML and writes it to the Linux framebuffer.
package fb

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strconv"
	"strings"

	"github.com/nathanstitt/omnidoc/pkg/omnidoc"
)

// Geometry reads the visible resolution from sysfs.
func Geometry(dev string) (int, int, error) {
	name := dev[strings.LastIndex(dev, "/")+1:]
	raw, err := os.ReadFile("/sys/class/graphics/" + name + "/virtual_size")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected virtual_size %q", raw)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return w, h, nil
}

// RenderHTML lays out and rasterizes an HTML document at exactly pageW x pageH.
//
// WithPageSize is required: without it the layout viewport defaults to 1280px,
// and omnidoc's fit-within sizing preserves aspect ratio, so the render
// comes back 1280x480 and is pillarboxed with white on a 1920x480 panel.
func RenderHTML(ctx context.Context, html []byte, pageW, pageH int) (image.Image, error) {
	doc, err := omnidoc.OpenHTMLBytes(html, omnidoc.WithPageSize(float64(pageW), float64(pageH)))
	if err != nil {
		return nil, fmt.Errorf("layout: %w", err)
	}
	img, err := doc.RasterizePage(ctx, 0, omnidoc.RasterOptions{
		MaxWidthPx: pageW, MaxHeightPx: pageH, Background: color.White,
	})
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	return img, nil
}

// Pack converts an image to XR24 bytes for a fbW x fbH framebuffer, rotating
// clockwise by the given angle. Pixel format is B,G,R,X little-endian.
func Pack(img image.Image, fbW, fbH, rotate int) []byte {
	b := img.Bounds()
	pageW, pageH := b.Dx(), b.Dy()

	canvas := image.NewRGBA(image.Rect(0, 0, pageW, pageH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, b.Min, draw.Src)

	buf := make([]byte, fbW*fbH*4)
	for y := 0; y < fbH; y++ {
		row := y * fbW * 4
		for x := 0; x < fbW; x++ {
			var sx, sy int
			switch rotate {
			case 90:
				sx, sy = y, pageH-1-x
			case 180:
				sx, sy = pageW-1-x, pageH-1-y
			case 270:
				sx, sy = pageW-1-y, x
			default:
				sx, sy = x, y
			}
			if sx < 0 || sy < 0 || sx >= pageW || sy >= pageH {
				continue
			}
			s := sy*canvas.Stride + sx*4
			d := row + x*4
			buf[d+0] = canvas.Pix[s+2] // B
			buf[d+1] = canvas.Pix[s+1] // G
			buf[d+2] = canvas.Pix[s+0] // R
			buf[d+3] = 0               // X
		}
	}
	return buf
}

// Write writes a packed buffer to the framebuffer device.
func Write(dev string, buf []byte) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteAt(buf, 0)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/fb/ -v`
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/fb
git commit -m "feat: framebuffer rasterization and XR24 packing"
```

---

### Task 11: Host-side fb2png verification tool

**Files:**
- Create: `tools/fb2png/main.go`

**Interfaces:**
- Consumes: a raw framebuffer dump
- Produces: a PNG; `unrotate` recovers the upright 1920×480 landscape image

This is the verification loop used throughout the design work. Built for the **host**, not the board.

The inverse mapping matters and is easy to get wrong: `Pack` writes `fb(x,y) ← canvas(pageW-1-y, x)`, so recovering the canvas is `canvas(cx,cy) ← fb(x=cy, y=pageW-1-cx)`.

- [ ] **Step 1: Write the tool**

Create `tools/fb2png/main.go`:

```go
// Command fb2png converts a raw XR24 framebuffer dump to PNG.
//
//	adb shell cat /dev/fb0 > fb.raw
//	go run ./tools/fb2png fb.raw out.png 480 1920 unrotate
//
// The panel is 480x1920 portrait; the UI is a 1920x480 landscape canvas rotated
// 270 at blit time. "unrotate" inverts that so the PNG reads upright.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: fb2png <in.raw> <out.png> <fbW> <fbH> [unrotate]")
		os.Exit(2)
	}
	fbW, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fatal("bad fbW: %v", err)
	}
	fbH, err := strconv.Atoi(os.Args[4])
	if err != nil {
		fatal("bad fbH: %v", err)
	}
	unrot := len(os.Args) > 5 && os.Args[5] == "unrotate"

	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal("%v", err)
	}
	if len(raw) < fbW*fbH*4 {
		fatal("short dump: %d bytes, need %d", len(raw), fbW*fbH*4)
	}
	at := func(x, y int) color.RGBA {
		s := y*fbW*4 + x*4
		return color.RGBA{R: raw[s+2], G: raw[s+1], B: raw[s+0], A: 255}
	}

	var img *image.RGBA
	if unrot {
		cw, ch := fbH, fbW // 1920x480
		img = image.NewRGBA(image.Rect(0, 0, cw, ch))
		for cy := 0; cy < ch; cy++ {
			for cx := 0; cx < cw; cx++ {
				img.Set(cx, cy, at(cy, cw-1-cx))
			}
		}
	} else {
		img = image.NewRGBA(image.Rect(0, 0, fbW, fbH))
		for y := 0; y < fbH; y++ {
			for x := 0; x < fbW; x++ {
				img.Set(x, y, at(x, y))
			}
		}
	}

	f, err := os.Create(os.Args[2])
	if err != nil {
		fatal("%v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fatal("%v", err)
	}
	b := img.Bounds()
	fmt.Printf("wrote %s (%dx%d)\n", os.Args[2], b.Dx(), b.Dy())
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
```

- [ ] **Step 2: Verify it builds and rejects a short dump**

Run: `go build ./tools/fb2png && go run ./tools/fb2png /dev/null /tmp/x.png 480 1920 unrotate; echo "exit=$?"`
Expected: `short dump: 0 bytes, need 3686400` and `exit=1`.

- [ ] **Step 3: Commit**

```bash
rm -f fb2png
git add tools/fb2png
git commit -m "feat: fb2png host tool for framebuffer verification"
```

---

### Task 12: Service loop and wiring

**Files:**
- Modify: `cmd/dashboard/main.go` (replace the Task 1 stub entirely)
- Create: `cmd/dashboard/loop.go`
- Create: `cmd/dashboard/loop_test.go`

**Interfaces:**
- Consumes: everything above
- Produces:
  - `type Store struct` — holds last-good events/weather with a mutex
  - `func (s *Store) Snapshot() ([]calendar.Event, *weather.Weather, []string)`
  - `func nextTick(now time.Time) time.Duration` — aligns to the next minute boundary

Behavior: data fetches run on their own intervals; the render loop wakes each minute. Re-render only when the generated HTML differs from the last render. Last-good data is kept when a fetch fails, and a failed fetch retries in 30s rather than waiting a full interval.

- [ ] **Step 1: Write the failing test**

Create `cmd/dashboard/loop_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestNextTickAlignsToMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 17, 0, time.UTC)
	if got, want := nextTick(now), 43*time.Second; got != want {
		t.Errorf("nextTick = %v, want %v", got, want)
	}
}

func TestNextTickAtExactBoundaryWaitsFullMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	if got, want := nextTick(now), time.Minute; got != want {
		t.Errorf("nextTick = %v, want %v", got, want)
	}
}

func TestStoreKeepsLastGoodOnFailure(t *testing.T) {
	s := &Store{}
	evs := []calendar.Event{{Title: "kept"}}
	w := &weather.Weather{Current: weather.Conditions{TempF: 70}}
	s.SetEvents(evs, nil)
	s.SetWeather(w, nil)

	// A later failure must not clear what we already have.
	s.SetEvents(nil, []string{"ical: timeout"})
	s.SetWeather(nil, []string{"weather: timeout"})

	gotEvs, gotW, errs := s.Snapshot()
	if len(gotEvs) != 1 || gotEvs[0].Title != "kept" {
		t.Errorf("events = %v, want the last-good set", gotEvs)
	}
	if gotW == nil || gotW.Current.TempF != 70 {
		t.Error("weather = nil, want the last-good value")
	}
	if len(errs) != 2 {
		t.Errorf("errs = %v, want both failures reported", errs)
	}
}

func TestStoreReplacesOnSuccess(t *testing.T) {
	s := &Store{}
	s.SetEvents([]calendar.Event{{Title: "old"}}, nil)
	s.SetEvents([]calendar.Event{{Title: "new"}}, nil)
	evs, _, errs := s.Snapshot()
	if len(evs) != 1 || evs[0].Title != "new" {
		t.Errorf("events = %v, want the fresh set", evs)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none after success", errs)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/dashboard/`
Expected: FAIL — `undefined: nextTick`, `undefined: Store`

- [ ] **Step 3: Write the store and tick helper**

Create `cmd/dashboard/loop.go`:

```go
package main

import (
	"sync"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// Store holds the most recent successful fetch of each source. A failed fetch
// records an error but never discards last-good data — a transient outage must
// not blank the panel.
type Store struct {
	mu       sync.RWMutex
	events   []calendar.Event
	weather  *weather.Weather
	evErrs   []string
	wxErrs   []string
}

func (s *Store) SetEvents(evs []calendar.Event, errs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evErrs = errs
	if len(errs) == 0 || evs != nil {
		s.events = evs
	}
}

func (s *Store) SetWeather(w *weather.Weather, errs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wxErrs = errs
	if w != nil {
		s.weather = w
	}
}

// Snapshot returns the current data plus any errors from the latest attempts.
func (s *Store) Snapshot() ([]calendar.Event, *weather.Weather, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	errs := append(append([]string(nil), s.evErrs...), s.wxErrs...)
	return s.events, s.weather, errs
}

// nextTick returns the delay until the next minute boundary, so the clock
// changes on the minute rather than drifting.
func nextTick(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/dashboard/ -v`
Expected: PASS for all four tests.

- [ ] **Step 5: Write the main wiring**

Replace `cmd/dashboard/main.go` entirely:

```go
// Command dashboard renders the UpNext dashboard to the Luckfox framebuffer.
//
// Display-only: there is no HTTP server and no touch handling. A tick loop
// wakes each minute, rebuilds the view model, and re-renders only when the
// generated HTML differs from the last frame.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/fb"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/view"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

const (
	pageW, pageH = 1920, 480
	rotate       = 270
	retryDelay   = 30 * time.Second
	fetchTimeout = 60 * time.Second
)

func main() {
	once := flag.Bool("once", false, "render a single frame and exit")
	fbDev := flag.String("fb", "/dev/fb0", "framebuffer device")
	cfgPath := flag.String("config", "/root/config.json", "path to config.json")
	htmlOut := flag.String("html-out", "", "also write the generated HTML here (debugging)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	fbW, fbH, err := fb.Geometry(*fbDev)
	if err != nil {
		log.Printf("framebuffer geometry: %v (falling back to %dx%d)", err, pageH, pageW)
		fbW, fbH = pageH, pageW
	}

	store := &Store{}
	if *once {
		fetchAll(context.Background(), cfg, store)
		if err := renderOnce(cfg, store, *fbDev, fbW, fbH, *htmlOut, nil); err != nil {
			log.Fatalf("render: %v", err)
		}
		return
	}

	go fetchLoop(cfg, store, time.Duration(cfg.Refresh.CalendarMinutes)*time.Minute, fetchCalendars)
	go fetchLoop(cfg, store, time.Duration(cfg.Refresh.WeatherMinutes)*time.Minute, fetchWeather)

	var last string
	for {
		time.Sleep(nextTick(time.Now()))
		if err := renderOnce(cfg, store, *fbDev, fbW, fbH, *htmlOut, &last); err != nil {
			log.Printf("render: %v", err)
		}
	}
}

// renderOnce builds the model, renders HTML, and blits it. When last is
// non-nil it is used to skip the raster when the HTML has not changed.
func renderOnce(cfg *config.Config, store *Store, dev string, fbW, fbH int, htmlOut string, last *string) error {
	evs, wx, errs := store.Snapshot()
	vm := model.Build(time.Now(), cfg, evs, wx, errs)

	html, err := view.Render(vm, wx)
	if err != nil {
		return err
	}
	if htmlOut != "" {
		if err := os.WriteFile(htmlOut, []byte(html), 0o644); err != nil {
			log.Printf("html-out: %v", err)
		}
	}
	if last != nil && html == *last {
		return nil // nothing changed; skip the ~1.5s render
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	img, err := fb.RenderHTML(ctx, []byte(html), pageW, pageH)
	if err != nil {
		return err
	}
	if err := fb.Write(dev, fb.Pack(img, fbW, fbH, rotate)); err != nil {
		return err
	}
	if last != nil {
		*last = html
	}
	return nil
}

type fetchFunc func(context.Context, *config.Config, *Store)

// fetchLoop runs one fetcher forever, retrying sooner after a failure so a boot
// with no DNS recovers in seconds rather than a full interval.
func fetchLoop(cfg *config.Config, store *Store, interval time.Duration, fn fetchFunc) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		before := len(errsOf(store))
		fn(ctx, cfg, store)
		cancel()

		wait := interval
		if len(errsOf(store)) > before {
			wait = retryDelay
		}
		time.Sleep(wait)
	}
}

func errsOf(s *Store) []string {
	_, _, errs := s.Snapshot()
	return errs
}

func fetchAll(ctx context.Context, cfg *config.Config, store *Store) {
	fetchCalendars(ctx, cfg, store)
	fetchWeather(ctx, cfg, store)
}

func fetchCalendars(ctx context.Context, cfg *config.Config, store *Store) {
	var all []calendar.Event
	var errs []string
	now := time.Now()
	for _, src := range cfg.Calendars {
		if src.URL == "" || len(src.URL) > 6 && src.URL[:6] == "PASTE_" {
			continue
		}
		evs, err := calendar.Fetch(ctx, src, now, cfg.Agenda.DaysAhead)
		if err != nil {
			errs = append(errs, "ical("+src.Name+"): "+err.Error())
			continue
		}
		all = append(all, evs...)
	}
	if cfg.Agenda.MaxEvents > 0 && len(all) > cfg.Agenda.MaxEvents {
		all = all[:cfg.Agenda.MaxEvents]
	}
	store.SetEvents(all, errs)
}

func fetchWeather(ctx context.Context, cfg *config.Config, store *Store) {
	w, err := weather.Fetch(ctx, cfg)
	if err != nil {
		store.SetWeather(nil, []string{"weather: " + err.Error()})
		return
	}
	store.SetWeather(w, nil)
}
```

- [ ] **Step 6: Verify the whole tree builds and tests pass**

Run: `go build ./... && go test ./...`
Expected: all packages build; all tests PASS.

- [ ] **Step 7: Render a frame on the host to check the HTML**

Run:
```bash
cp config.sample.json /tmp/dash-config.json
go run ./cmd/dashboard --once --config /tmp/dash-config.json --fb /dev/null --html-out /tmp/dash.html
open /tmp/dash.html
```
Expected: the dashboard renders in a browser with real weather (the sample config has a valid location; calendars are `PASTE_` and are skipped, so the agenda is empty). Writing to `/dev/null` exercises the blit path harmlessly.

- [ ] **Step 8: Commit**

```bash
git add cmd/dashboard
git commit -m "feat: service loop with per-source refresh and change detection"
```

---

### Task 13: Deploy and verify on hardware

**Files:**
- Modify: `CLAUDE.md` (add a "Dashboard" section)

**Interfaces:**
- Consumes: everything
- Produces: a verified frame on the panel

**Ask before running any step in this task.** Deploying touches the physical appliance. Building is fine unprompted; pushing is not.

- [ ] **Step 1: Cross-compile**

Run: `scripts/build.sh dashboard`
Expected: `build/dashboard` exists and build.sh's ELF check passes (32-bit ARM).

- [ ] **Step 2: Confirm the board is reachable**

Run: `adb devices`
Expected: one device listed as `device`. If it shows `Maskrom` or nothing, the board is not running Linux — power-cycle without holding BOOT.

- [ ] **Step 3: Deploy the binary and a config**

Run:
```bash
scripts/deploy.sh build/dashboard
adb push config.sample.json /root/config.json
```
Then edit `/root/config.json` on the board to add real iCal URLs, or leave the `PASTE_` placeholders to test with weather only.

- [ ] **Step 4: Render one frame and time it**

Run: `adb shell '/usr/bin/time -v /root/dashboard --once 2>&1 | grep -iE "Elapsed|Maximum res"'`
Expected: elapsed ~2–3s (one-time font setup plus ~1.5s render), max RSS under ~200MB. If RSS approaches 400MB, stop and investigate — the board has 466MB.

- [ ] **Step 5: Capture the framebuffer and inspect it**

Run:
```bash
adb shell cat /dev/fb0 > /tmp/fb.raw
go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate
open /tmp/panel.png
```
Expected: a 1920×480 dashboard filling the whole frame. **If there is a white band on the right, `WithPageSize` is not being applied** — that is the pillarboxing failure, and it means the render fell back to the 1280px default viewport.

- [ ] **Step 6: Confirm the icons rendered**

Inspect `/tmp/panel.png`: every forecast column must show a weather icon. A blank space where an icon belongs means an emoji leaked into the output — the board silently drops those glyphs. Cross-check with `go test ./internal/chart/ -run TestIconNeverReturnsEmoji`.

- [ ] **Step 7: Run the service and confirm it re-renders**

Run:
```bash
adb shell 'nohup /root/dashboard >/root/dashboard.log 2>&1 &'
```
Wait ~2 minutes, then capture again (Step 5) and confirm the clock advanced.

- [ ] **Step 8: Document it**

Add to `CLAUDE.md` after the "Display and touch" section:

```markdown
## Dashboard

The `dashboard` service renders the UpNext display: it fetches calendars and
weather, generates HTML+SVG, rasterizes with omnidoc, and writes `/dev/fb0`.
Display-only — no touch, no HTTP server.

```bash
scripts/build.sh dashboard
scripts/deploy.sh build/dashboard
adb push config.sample.json /root/config.json   # then edit in the iCal URLs
adb shell /root/dashboard --once                # single frame
adb shell 'nohup /root/dashboard >/root/dashboard.log 2>&1 &'
```

Cost per refresh is ~1.5s (~1.37s layout, ~170ms raster) at ~115-154MB RSS.
The loop wakes each minute and skips the raster when the HTML is unchanged.

**`WithPageSize(1920, 480)` is required.** omnidoc defaults to a 1280px
layout viewport, and its fit-within sizing preserves aspect ratio — so without
it the page renders 1280x480 and is pillarboxed with white.

**Weather icons must be SVG, never emoji.** The board ships only DejaVu and
Liberation. Of the 9 weather emoji the Pi dashboard used, 3 render as
monochrome glyphs and 6 render as *nothing at all* — silently dropped, so a
missing icon looks like a layout gap rather than an error.

Verify what the panel actually shows:

```bash
adb shell cat /dev/fb0 > /tmp/fb.raw
go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate
```
```

- [ ] **Step 9: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: dashboard build, deploy, and verification"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Packages (config/calendar/weather/model/chart/view/fb) | 2, 3, 4, 5–7, 8, 9, 10 |
| Refresh loop, change detection, last-good data | 12 |
| `WithPageSize(1920,480)` exact fit | 10, 13 |
| Linear axis, visibility floor | 5 |
| Floating labels, row demotion, leader marks | 6, 9 |
| Adaptive window with clamps | 5 |
| NOW line as exact `timeToX` | 5, 7, 9 |
| SVG icons (emoji correctness fix) | 8 |
| SVG charts from template + computed values | 8 |
| Rotate 270 blit, XR24 packing | 10 |
| `fb2png` verification loop | 11, 13 |
| `--once` flag | 1, 12, 13 |
| No `time.Now()` in logic | 3, 5, 6, 7 |
| Dropped: touch, HTTP, Apps Script, OpenWeather | n/a — explicitly out of scope |

**Deviations from the spec, flagged:**
- `past_temps.json` (temperature history extending the curve left of NOW) is **not** implemented. The spec called it "worth keeping". It is dropped from Task 4 to keep the weather package simple; the curve starts at "now". Revisit if the chart looks truncated.
- The spec's `model` package is split into three files (`timeline.go`, `labels.go`, `model.go`) so each stays focused.

**Type consistency:** `calendar.Event` uses `time.Time` for `Start`/`End` throughout (Tasks 3, 5, 6, 7, 9). `model.Window`/`Block`/`Label` names are consistent across Tasks 5–7 and 9. `view.Render(vm, w)` takes two arguments everywhere it appears (Tasks 9 and 12), and the Task 9 fixture returns `(model.ViewModel, *weather.Weather)` to match.

**Escaping:** only `chart.Hourly` output and `chart.Icon` output are wrapped in `template.HTML`. Event titles and locations flow through `html/template` as plain strings, so they are escaped — `TestRenderEscapesTitles` pins this.
