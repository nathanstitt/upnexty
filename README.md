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
- **Settings portal** — served on `:80` by the same process. Wi-Fi, calendars,
  brightness, clock format, hostname, and unhiding events.
- **Self-setup** — with no network the board raises its own access point and
  serves the portal there, answering captive-portal probes so a phone offers
  its sign-in sheet.

## Repository layout

```
cmd/dashboard/      the service: fetch, render, blit, portal, touch
internal/calendar/  iCal parsing and the Google Calendar API backend
internal/googleauth/per-account OAuth token storage and refresh
internal/model/     view model: agenda layout, now-block, hit testing
internal/view/      HTML template, CSS, fonts, SVG
internal/portal/    the settings web UI
internal/fb/        framebuffer format and blitting
internal/weather/   Open-Meteo client
internal/wifi/      association, AP mode, status
board/              files installed onto the device (init scripts, helpers)
scripts/            host-side build, deploy, flash, and board helpers
docs/plans/         design notes for work in progress
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
replace github.com/nathanstitt/omnidoc => ../../omnidoc
```

The renderer is a separate project and must be checked out as a sibling
directory. Override its location with `OMNIDOC_DIR`.

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
the browser that started the flow has already been answered.

Setting it up needs a Google Cloud project with an OAuth client of type "TV and
Limited Input device". See `docs/plans/google-calendar-api.md`, which also
records what was measured and what was tried and rejected.

## Hardware notes

The board-specific detail — panel timings, the DSI fix, Wi-Fi driver
installation, flashing, and the gotchas that cost real time — lives in
`CLAUDE.md`. Read it before touching the device; several of its entries exist
because something failed silently and took a while to find.
