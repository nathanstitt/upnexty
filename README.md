# upnexty

A wall-mounted calendar and weather panel. Go, cross-compiled on a Mac and run
on a Luckfox Lyra Zero W driving a Waveshare 8.8" DSI display.

![1920×480 landscape panel: clock and weather on the left, a scrolling row of
event cards with a NOW cursor, an hourly temperature curve, and a 7-day
forecast strip](docs/panel.png)

The board fetches calendars and weather, renders an HTML document, rasterises
it in-process, and writes the result straight to `/dev/fb0`. There is no X, no
browser, and no compositor on the device.

## What it does

- **Agenda row** — the day as a departure board: the last thing that finished,
  what is happening now, what is next, and the free gaps between. Overlapping
  events stack rather than laying out as a false sequence.
- **Weather** — current conditions, an hourly temperature curve across the same
  window as the agenda, and a 7-day strip.
- **Touch** — tapping a card opens a detail sheet with "hide from panel".
- **Meeting chime** — a short bell through a USB speaker when a timed event
  starts. Silent on a board with no sound card; `"sound": {"chime": false}`
  turns it off.
- **Settings portal** — served on `:80` by the same process. Wi-Fi, calendars,
  brightness, clock format, hostname, and unhiding events.
- **Self-setup** — with no network the board raises its own access point and
  serves the portal there, answering captive-portal probes so a phone offers
  its sign-in sheet.
- **Recovery** — a supervisor restarts the dashboard when it exits, and a
  hardware watchdog resets the board if it locks up. The watchdog needs a
  one-time device-tree change; see `CLAUDE.md`, "Watchdog".

## Repository layout

```
cmd/dashboard/       the service: fetch, render, blit, portal, touch, chime
internal/calendar/   iCal parsing and the Google Calendar API backend
internal/googleauth/ per-account OAuth token storage and refresh
internal/config/     config.json schema and storage
internal/model/      view model: agenda layout, now-block, hit testing
internal/view/       HTML template, CSS, fonts
internal/chart/      weather charts as SVG
internal/portal/     the settings web UI
internal/fb/         framebuffer format and blitting
internal/touch/      Goodix touch input, rotated to panel space
internal/chime/      meeting chime synthesis and playback
internal/quote/      end-of-day quote
internal/weather/    Open-Meteo client
internal/wifi/       association, AP mode, status
board/               files installed onto the device (init scripts, helpers)
scripts/             host-side build, deploy, flash, and board helpers
tools/fb2png/        convert a /dev/fb0 capture to PNG
docker/usb-audio/    kernel build image for the USB audio modules
docs/                lockup investigation, TODO, engine gaps, plans
```

## Building

Requires Go 1.26 and, for deploying, `adb`.

```bash
scripts/build.sh dashboard    # cross-compile to build/
scripts/deploy.sh             # adb push to the board, install init scripts
```

`scripts/build.sh` targets `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0` and
verifies the result really is 32-bit ARM. The static binary has no libc
dependency, so it does not care what the board's Buildroot ships.

**This repo does not build standalone.** `go.mod` has

```
replace github.com/nathanstitt/omnidoc => ../omnidoc
```

The renderer is a separate project and must be checked out as a sibling
directory. To use a different location, change that `replace` line.

`scripts/deploy.sh` with no arguments also installs `board/etc/init.d/`, which
includes `S99wlan0`. To change only the dashboard, name the file:
`scripts/deploy.sh build/dashboard`. Read `CLAUDE.md` before a bare deploy.

## Board services

Installed from `board/etc/init.d/`:

| script | job |
|---|---|
| `S20watchdog` | feed the hardware watchdog; exits cleanly if there is none |
| `S30usbaudio` | set the USB speaker volume when the card appears |
| `S97lockupprobe` | temporary lockup-investigation settings (`docs/lockups.md`) |
| `S98health` | sample health to `/root/health.log`; mark unclean shutdowns |
| `S99wlan0` | associate, DHCP, set the clock; fall back to AP mode |
| `S99zdashboard` | run the dashboard under a restart supervisor |
| `S99zsoakexpire` | clean up a soak test that outlived a lockup |

USB audio needs four kernel modules that the stock image does not ship.
`scripts/build-usb-audio.sh` builds them in Docker and
`scripts/setup-usb-audio.sh` installs them; see `CLAUDE.md`, "USB audio".

## Calendars

Two backends, chosen per source by `kind` in `config.json`:

| kind | how | notes |
|---|---|---|
| `ical` (default) | a feed URL | works with any provider; the secret-address Google export included |
| `google` | Calendar API | needs OAuth; far cheaper on this hardware |

An existing config with no `kind` keeps working unchanged.

The Google backend exists because of the numbers. For these calendars the iCal
export is **15.9MB across 5,091 events** to display **43**, and parsing it
allocates 241MB on a board with ~430MB usable. The API returns the same 43
occurrences in **96KB**, with recurrences already expanded server-side.

Connecting an account uses the OAuth device flow: press the button in the
portal, and the code appears **on the panel** — by the time Google issues it,
the browser that started the flow has already been answered. Once the token
lands the account's primary calendar is added to the panel on its own, and
the settings page lists every calendar the account can read with a checkbox
each, so secondary and shared calendars are one tick away.

Setting it up needs a Google Cloud project with an OAuth client of type "TV and
Limited Input device". See `docs/plans/google-calendar-api.md`, which also
records what was measured and what was tried and rejected.

## Hardware notes

The board-specific detail — panel timings, the DSI fix, Wi-Fi driver
installation, flashing, the watchdog, USB audio, and the gotchas that cost real time — lives in
`CLAUDE.md`. Read it before touching the device; several of its entries exist
because something failed silently and took a while to find.
