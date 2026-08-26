# Captive portal — Design

**Status:** proposed, not yet implemented
**Date:** 2026-08-26

Configure the dashboard from a phone. When WiFi is down the board raises its own
access point; when WiFi is up the same portal stays reachable on the LAN.

## The problem this solves

Today every setting is a JSON file you edit over `adb`. That is fine for a
developer at a desk and useless for the actual failure case: the panel is blank
or stale, the board is on a shelf, and there is no keyboard. Worse, the first-run
case is circular — the board needs WiFi credentials to be reachable, and it is
only reachable over WiFi.

The portal breaks the circle. No network means the board becomes the network.

## Who this is for

One person, on a phone, standing next to a panel that is not doing what they
expect. Not a fleet operator, not a developer at a terminal. They want to fix
one thing and leave.

That framing drives the design more than anything else:

- **Every screen fits a phone.** The panel is 1920×480; the portal is 390×844.
- **The task is usually one field.** Wrong WiFi password, expired calendar URL.
  Optimise for change-one-thing-and-go, not for a settings tour.
- **State first, form second.** The first thing on screen answers "is it
  working?", because that is the question that brought them here.

## Settings inventory

Traced from the code, not from `config.sample.json`. Three fields in the current
config are **dead** and must not appear in the UI.

### Live settings — these change what renders

| Setting | Path | Where it is read | Notes |
|---|---|---|---|
| Calendar feeds | `calendars[]` | `main.go` → `calendar.Fetch` | name, colour, URL. Repeatable. **The main event.** |
| Latitude / longitude | `location.latitude/longitude` | `weather.Fetch` | Weather query. Needs a friendlier input than two decimals. |
| Timezone | `location.timezone` | `model.Build`, `weather.Fetch`, `calendar.Parse` | Drives the clock, weather timestamps, and all-day event parsing. |
| Temperature unit | `units.temperature` | `weather.Fetch` | Passed to Open-Meteo. `fahrenheit`/`celsius`. |
| 12/24-hour clock | `units.clock_24h` | `model.Build` | |
| Weather refresh | `refresh.weather_minutes` | `fetchLoop` | Default 15. |
| Calendar refresh | `refresh.calendar_minutes` | `fetchLoop` | Default 10. |
| Days ahead | `agenda.days_ahead` | `calendar.Fetch` | Default 7. |
| Max events | `agenda.max_events` | `fetchCalendars` | Default 40. |

### Not currently in config.json but should be

| Setting | Today | Why expose it |
|---|---|---|
| WiFi SSID + password | `/etc/wpa_supplicant.conf`, set by hand | The reason the portal exists. |
| Admin password | — | Guards the portal. See below. |
| Backlight | `/sys/class/backlight/waveshare_bl/brightness` (0–255, currently 200) | A wall panel at full brightness at night is the most likely real complaint. |

### Dead — do not surface

- `units.wind_speed` — **zero consumers**. No wind is displayed.
- `units.precipitation` — **zero consumers**. Precip renders as a bare `%`.
- `location.name` — reaches `ViewModel.LocationName` and is never rendered.

These are **deleted** as part of this work rather than given a UI. Surfacing a
control that does nothing is worse than having no control, and leaving them in
place invites someone to wire up a control later.

### Deliberately not exposed

Layout internals (`NowBlockW`, `MaxOverflow`, `minBlockPx`, window clamps) are
tuning values, not preferences. `rotate` is fixed by how the panel is mounted and
would let someone brick their own display from a phone.

## Two modes, one portal

```
                    ┌─────────────────────────────┐
   WiFi down  ──▶   │  AP: upnext-a4f2            │  ← board is the network
                    │  portal at 192.168.4.1      │
                    └─────────────────────────────┘
                                 │  credentials saved
                                 ▼
                    ┌─────────────────────────────┐
   WiFi up    ──▶   │  STA: 192.168.1.80          │  ← portal stays up
                    │  portal at :8080            │
                    └─────────────────────────────┘
```

**AP mode** is entered when association fails or drops for longer than a grace
period. SSID is `upnext-<4 hex>` from the WiFi MAC's last two octets — stable
across reboots, so a returning user sees the same name. `hostapd` and `dnsmasq`
are already on the image; `dnsmasq` answers every DNS query with `192.168.4.1`,
which is what makes phones pop the "sign in to network" sheet. There is no
`iptables` on this image, so wildcard DNS is the whole redirect mechanism.

**STA mode** keeps the portal on `:8080`. The dashboard already has no HTTP
server — this is the config server the original spec deferred.

The board should **not** silently sit in AP mode forever. If credentials exist,
it retries STA periodically; AP is a fallback, not a destination.

## Admin password

The brief asks for a password derived from something the user can see, but
changeable.

**Default:** the last 6 hex of the WiFi MAC, no separators — e.g. `4c1bfd`.
Printed on the panel itself when in AP mode (the dashboard has 1920×480 of
space and nothing useful to show while unconfigured). That is the "known to the
user" property, and it is per-device rather than a shared default.

**Changeable:** stored as a hash in `/root/portal.json`. Once changed, the MAC
default stops working.

**Recovery:** hold the BOOT button during power-on to reset to the MAC default.
The button already exists for Maskrom.

Honest limits, worth stating rather than pretending otherwise: the portal is
HTTP, not HTTPS — a self-signed cert produces a browser warning that trains
users to click through warnings, which is worse than plaintext on a LAN. A MAC
suffix is weak against someone already on your WiFi. This is a wall display, not
a bank. What the password prevents is a houseguest reconfiguring the panel, and
it is sized for that.

## Visual direction

The portal and the panel are one product, so the portal inherits the dashboard's
palette rather than inventing a second identity.

**Colour** — `#0b0d12` ground, `#141924` raised surface, `#263041` hairlines,
`#e8ecf3` primary text, `#8b97ab` secondary, `#4f9cff` the single accent, and
`#ff7a59` reserved exclusively for "this is broken." Two accents, each with one
job, so blue never has to mean trouble.

**Type** — the panel renders in DejaVu because that is what the board has. The
portal has a browser and should not imitate that constraint. Display face is a
condensed grotesque for headings and the device name; body is a plain system
stack for inputs, where familiarity beats personality; a monospace face carries
MACs, SSIDs, and the admin password, because those are strings you read
character by character and type by hand.

**Layout** — single column, 420px max, generous vertical rhythm. Phone-shaped
because that is the only device this is used from.

### The signature

The panel is 1920×480 — a 4:1 letterbox with a fixed 380px block on the left,
divided by a hairline. That ratio is the most distinctive thing about this
device.

Every settings row in the portal repeats it: a fixed-width label column, a
hairline rule, then the field. The rule is the same `#263041` as the panel's
divider, at the same proportion. Scrolling the portal echoes the geometry of the
thing being configured.

```
┌────────────────────────────────────────┐
│  ● Connected · Argosity                │   status, first
│    192.168.1.80                        │
├────────────────────────────────────────┤
│  NETWORK        │  Argosity            │   ← 380:1540 proportion,
│                 │  ●●●●●●●●●●          │     hairline at the seam
├─────────────────┼──────────────────────┤
│  CALENDARS      │  Personal    ▸       │
│                 │  Work        ▸       │
│                 │  + Add feed          │
├─────────────────┼──────────────────────┤
│  PLACE          │  38.569, -92.163     │
│                 │  America/Chicago     │
├─────────────────┼──────────────────────┤
│  DISPLAY        │  Brightness ▓▓▓▓▓░░  │
│                 │  °F   12-hour        │
└─────────────────┴──────────────────────┘
```

On a narrow phone the two columns stack and the rule becomes horizontal — the
proportion survives as a label-above-field rhythm rather than breaking.

**No numbered steps.** These are independent settings, not a sequence, so
numbering them would encode order that is not real. The one place numbering is
honest is first-run AP setup, which genuinely is ordered: join, configure,
reconnect.

### Motion

One orchestrated moment, not scattered effects: on save, the status strip at the
top transitions through its actual states — saving → applying → connected — so
the feedback lands where the question was asked. Everything else is instant.
`prefers-reduced-motion` collapses it to a straight state swap.

## Copy

The interface names things the way the owner thinks about them, not the way the
system stores them.

| Not this | This |
|---|---|
| `wpa_supplicant.conf` | Network |
| iCal URL | Calendar feed |
| `location.latitude` | Where you are |
| STA/AP mode | Connected / Setup mode |
| "Submit" | "Save network" → toast "Network saved" |

Errors say what happened and what to do, in the interface's voice:

- "Wrong password for Argosity. Try again."
- "That calendar link didn't load. Check the address, or the calendar may be
  private."
- Not: "Error: connection failed (-1)."

The empty calendar list is an invitation, not a void: **"No calendars yet — add
a feed to see your agenda."** That is also the panel's current real state.

## Architecture

New `internal/portal` package. The dashboard process gains an HTTP server; it
does not gain a second binary.

```
internal/portal/
  server.go     routes, auth middleware, mode detection
  handlers.go   GET/POST per settings group
  wifi.go       scan, associate, hostapd/dnsmasq lifecycle
  assets/       one HTML template, one CSS file, no JS framework
internal/config/
  config.go     + Save(), + WiFi and Portal sections, - dead fields
board/etc/init.d/
  S99wlan0      + AP fallback when association fails
```

**Config writes must be atomic.** Write to a temp file, `fsync`, rename. `/root`
is UBI on NAND and a torn write during a power cut would leave an unbootable
config. Keep the previous version as `config.json.bak` for the same reason.

**Applying changes.** The portal is a goroutine in the existing `dashboard`
binary, not a second process, so there is no IPC and nothing to signal. Today
`config.Load` runs once and the resulting `*Config` is captured by the loop and
both fetchers; the change is to put it behind the same mutex-guarded holder that
`Store` already uses for weather and calendar data:

```go
type Store struct {
    mu      sync.RWMutex
    cfg     *config.Config   // added
    events  []calendar.Event
    weather *weather.Weather
    ...
}
```

A portal handler validates, writes the file, and swaps the pointer. The next
tick reads the new value — no restart, no dropped frame. Because the pointer is
replaced rather than mutated, readers keep a consistent snapshot for the whole
tick.

The one case that genuinely needs more than a swap is **WiFi credentials**,
where the interface has to come down and reassociate. That is a `wifi` package
call, not a config reload.

**Progressive enhancement, not a SPA.** Plain HTML forms that work without
JavaScript, enhanced with fetch for inline validation. A captive-portal browser
sheet is a hostile environment — some are old WebViews with no JS at all.

## Testing

- `config` — round-trip Save/Load, atomic-write behaviour under a simulated
  interrupted write, dead-field removal does not break existing files.
- `portal` — auth (correct, wrong, absent), each handler's validation, golden
  HTML for the settings page.
- `wifi` — scan parsing against captured `wpa_cli` output; AP-mode transitions
  driven by a fake command runner, not the real board.
- On hardware — the actual first-run: no credentials, board raises AP, phone
  joins, portal loads, credentials save, board reconnects. That is the flow this
  whole feature exists for and it can only be verified on the device.

## Open questions

1. ~~Should the panel show the AP name and password while unconfigured?~~
   **Resolved: yes, both.** First-run then needs no documentation, using a
   screen that is otherwise blank. Accepted trade: anyone who can see the panel
   learns the admin password.
2. **Does WiFi scanning need `iw`?** It is missing from this image. `wpa_cli
   scan_results` works and is already proven, so probably not — worth confirming
   before committing to a network picker UI.
3. ~~Restart or reload on save?~~ **Resolved:** neither. The portal runs inside
   the dashboard process, so a handler swaps the config pointer under the
   existing mutex and the next tick picks it up.
