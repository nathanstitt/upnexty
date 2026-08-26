# UpNext on the Luckfox Lyra Zero W — Design

**Status:** approved design, not yet implemented
**Date:** 2026-08-25

Port the `pi-dashboard` display to the Luckfox Lyra Zero W: a Go service that
generates HTML, rasterizes it with doctaculous, and writes the result to
`/dev/fb0`. No browser, no compositor, no X/Wayland.

## Why this is not a straight port

The Pi ran Chromium in kiosk mode. The dashboard is therefore a browser
application: it polls JSON, renders DOM, draws two `<canvas>` charts, measures
laid-out elements to place a "NOW" bar, and handles touch. The Luckfox has no
browser. doctaculous provides the CSS layout engine and rasterizer, but has **no
JavaScript engine and no canvas** — `<script>` is parsed and discarded.

Three classes of browser work move into Go:

| Browser did | Now |
|---|---|
| Poll `/data.json`, render DOM | Service builds a model and emits finished HTML |
| Draw charts to `<canvas>` | Charts generated as SVG |
| Measure DOM, scroll to align NOW | Positions computed directly in Go |

The fetch layer (`main.go`) is browser-independent and ports nearly as-is. The
**iCal parser with RRULE expansion** (~400 lines: `icalExpand`, `icalRecur`,
`monthlyOccurrences`, `nthWeekdayOfMonth`) is the single most valuable asset
here and carries over untouched.

## Hardware findings

All measured on the board, not estimated.

| | |
|---|---|
| Full refresh (parse + layout + raster) | **~1.5s** (~1.37s layout, ~170ms raster) |
| Re-raster of unchanged document | ~170ms |
| RSS | ~115–154MB of 466MB total |
| Framebuffer | 480×1920 XR24, 3686400 bytes, stride 1920 |

At one refresh per minute that is a **2.5% duty cycle** — comfortable. Layout,
not rasterization, dominates; it recurs on every data change because new data
means a new document.

### Two constraints found by measurement

**1. Default viewport is 1280px, not the panel width.** doctaculous's
fit-within sizing preserves aspect ratio, so an HTML document (which defaults to
a 1280pt-wide viewport) fitted into 1920×480 renders 1280×480 and is
pillarboxed with white. Fix: `WithPageSize(1920, 480)`, which sets the viewport
and page height to the exact panel aspect, making fit-within an exact 1:1 fill
at 72 DPI. Verified full-bleed on hardware.

**2. Weather emoji do not render.** The board ships 33 font files — DejaVu and
Liberation only, no emoji font. Of the 9 weather emoji the Pi dashboard uses,
**3 render** (`☀ ☁ ❄`, as monochrome DejaVu glyphs) and **6 render as nothing
at all** — silently dropped, not even tofu. A missing icon therefore looks like
a rendering gap rather than an error. This makes SVG icons a **correctness
requirement**, not a preference.

## Architecture

Single binary. No HTTP server initially (a configuration server is planned
later, and is the only reason to add one).

```
 config.json ──┐
               ├─> fetch (weather, calendars) ──> model ──> HTML+SVG ──> raster ──> /dev/fb0
 tick loop ────┘         (own intervals)                   doctaculous     blit 270°
```

### Packages

Split so each unit is understandable and testable alone. The Pi version is one
39K `main.go`; that is the thing to improve on.

| Package | Responsibility | Depends on |
|---|---|---|
| `config` | Load/validate `config.json`, defaults | — |
| `calendar` | iCal fetch + RRULE expansion → `[]Event` | `config` |
| `weather` | Open-Meteo / OpenWeather fetch → `Weather` | `config` |
| `model` | Merge sources; compute the view model (window, positions, labels) | `calendar`, `weather` |
| `chart` | `Weather` → SVG string | `model` |
| `view` | View model → HTML (templates + embedded CSS/SVG assets) | `model`, `chart` |
| `fb` | Rasterize + rotate + write framebuffer | `view` |

`model` is where the interesting logic lives and is pure: given a time and
fetched data, produce positioned output. That makes the timeline algorithm
directly unit-testable with no network, no rendering, and no board.

### Refresh

One tick loop aligned to the minute boundary. Data fetches run on their own
longer intervals (weather 15m, calendar 10m per config) and only update the
model. Each tick rebuilds the view model and re-renders **only if the generated
HTML differs from the last render** — the clock changes every minute, so in
practice most ticks do render, but this avoids pointless work when nothing has
moved and makes the no-change path cheap.

Last-good data is retained if a fetch fails (as on the Pi). A cold boot with no
network retries at 30s rather than waiting a full interval.

## The timeline

The signature feature: a horizontal time axis with a NOW line, agenda cards
above, weather below, both sharing one time→X mapping.

### Strictly linear axis, floating labels

Cards are positioned **and sized strictly by time**. A 15-minute meeting gets a
genuinely narrow block. Legibility does not come from widening the card —
widening breaks the axis, and once the axis is non-linear the agenda and the
weather strip below it disagree everywhere except at NOW.

Instead, **labels float free of their card** and may overflow it. The card is
the truthful time block; the label is a separate layer placed for readability.
This keeps one exact `timeToX()` function shared by every row.

Cards still need a small **visibility floor** (a few px) so a 5-minute event
does not render sub-pixel. This is a rendering minimum, not the layout
min-width rejected above: it is small enough that axis distortion stays
imperceptible, and unlike a label-sized min-width it does not accumulate across
a dense morning.

```
  cards:   [██]  [████████]  [█]      [██████]
  labels:  Standup   Design review    1:1  Retro
                 ^ label wider than its card, overflows right
```

### Label placement

Labels collide when meetings are close together. Placement pass, in order:

1. Place each label at its card's left edge.
2. Walking left→right, if a label would overlap the previous one, push it right.
3. If pushing exceeds a threshold (the label would drift too far from its
   anchor), demote it to a **second label row** instead.
4. At most two label rows; beyond that, truncate with an ellipsis.

Two rows is the recommended cap — a third makes the association between label
and card ambiguous. Where a label is pushed off its anchor, a short leader mark
ties it back to its card.

### Adaptive window

The window fits the **next N events** rather than a fixed span, so an empty
afternoon does not waste the panel. Bounds:

- Always include a little past context (a just-ended meeting stays visible).
- Clamp to a minimum span so a single imminent event does not zoom the axis to
  a few minutes across.
- Clamp to a maximum so a far-future event does not compress today into
  nothing.

The exact N and clamps are tuning values; start from the Pi's constants
(`GAP_MIN` 10m, `IMMINENT_MIN` 10m) and adjust against real renders.

### NOW line

An exact `timeToX(now)` computation, emitted as an absolutely-positioned
element. No measuring, no scrolling. Because the axis is linear and shared, the
agenda and weather strip align by construction.

## SVG

Assumes complete SVG support in doctaculous.

**Icons** — the ~11 weather conditions (clear, mainly clear, partly cloudy,
overcast, fog, drizzle, rain, snow, heavy snow, showers, thunderstorm) plus a
warning and stale-data mark. Authored once as a small icon set, embedded in the
binary. Replaces the emoji that cannot render.

**Charts** — an SVG template per chart with Go-computed values substituted:
path `d` data for the temperature curve, `<rect>` bars for precipitation
probability, `<text>` hour labels. The template keeps the visual structure
hand-tunable; Go supplies only geometry and content.

Charts and icons enter the page as ordinary embedded/inline SVG, so they
participate in the same layout and coordinate system as everything else — no
separate compositing pass.

## Rendering and verification

```
model → HTML (+inline SVG) → OpenHTMLBytes(WithPageSize(1920,480))
      → RasterizePage(MaxWidthPx:1920, MaxHeightPx:480)
      → rotate 270 → XR24 → WriteAt(/dev/fb0)
```

`OpenHTMLBytes` avoids writing generated HTML to NAND on every refresh.

The blit maps `fb(x,y) ← canvas(pageW-1-y, x)`, matching `html2fb -rotate 270`.

**Pixel verification loop** (working, used throughout this design):

```sh
adb shell /root/dashboard --once      # render one frame
adb shell cat /dev/fb0 > fb.raw       # capture
fb2png fb.raw out.png 480 1920 unrotate
```

`fb2png` inverts the rotation to recover the upright 1920×480 landscape image.
This gives visual inspection and programmatic diffing of what the panel actually
shows, without eyeballing hardware. A `--once` flag (render a single frame and
exit) makes the service scriptable for this.

## What is dropped

Display-only kiosk: no touch. From the Pi version this removes the detail
overlay, swipe-to-dismiss, tap-to-hide events, the event-start chime, and the
mute toggle. `fbtouch` exists and the renderer should not make touch
*impossible* later, but no hit-testing or input state machine is built now.

Also dropped: the HTTP server, static file serving, and the `data.json` schema —
all browser-era plumbing.

**Deferred to a follow-up:** the Pi's `past_temps.json` history, which extends
the temperature curve left of NOW. It is worth keeping — it survives restarts,
and the board has no RTC — but it did not ship in the first implementation, so
the curve currently starts at the first forecast sample and runs forward only.
Implementing it means: record each observed current temp beside the config file,
load it at boot, and feed the recorded points into `chart.Hourly` so it can plot
left of the NOW line. Note the chart's window is half-open `[Start, End)`, and
`ComputeWindow`'s `PastContext` already reserves space to the left of NOW for
exactly this.

## Testing

- `model` — table-driven tests over the timeline algorithm: label collision,
  window clamping, `timeToX` correctness, empty/single/dense event sets.
- `calendar` — the existing `ical_test.go` ports over; RRULE expansion is the
  highest-risk logic in the project.
- `chart` — golden SVG string comparison.
- `view` — golden HTML comparison.
- End-to-end — render a fixture model on the board, capture the framebuffer,
  diff against a golden PNG.

The board clock resets to 1970 each boot (no RTC battery), so every test must
take time as a parameter rather than calling `time.Now()` internally. This also
makes the timeline tests deterministic.

## Open questions

None blocking. Tuning values (window size and clamps, label row cap, card
visibility floor, chart proportions) are best settled against real renders
rather than decided up front.
