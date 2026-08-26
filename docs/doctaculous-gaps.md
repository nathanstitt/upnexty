# doctaculous gaps found while building the Luckfox dashboard

Seven CSS features below, plus one API issue (context cancellation, §7).

Found while rendering `internal/view/assets/style.css` (a 1920×480 dark-theme
dashboard) through `doctaculous.OpenHTMLBytes` + `RasterizePage`. Every item
below was isolated in a minimal page and confirmed against the engine source.

**These are not worked around in this repo.** The dashboard's stylesheet is
written as correct CSS; it will render properly once these land.

Testing method — a browser preview cannot find these, because browsers
implement all of them. Only rasterizing through doctaculous shows the gap:

```go
doc, _ := doctaculous.OpenHTMLBytes(html, doctaculous.WithPageSize(1920, 480))
img, _ := doc.RasterizePage(ctx, 0, doctaculous.RasterOptions{
    MaxWidthPx: 1920, MaxHeightPx: 480, Background: color.White})
```

## 1. CSS custom properties — `var()` (highest impact)

`var(--x)` silently resolves to nothing and the property falls back to its
default. No `var()` or custom-property handling exists in `pkg/css`, and the
feature is absent from `FEATURES.md`.

```css
:root { --bg: #0b0d12; --fg: #4f9cff; }
.a { background: var(--bg); color: var(--fg); }   /* renders white-on-white */
.b { background: #0b0d12;   color: #4f9cff;   }   /* renders correctly */
```

Impact here: the dashboard's entire dark theme disappeared — black text on a
white background — because the palette is defined once in `:root` and
referenced 16 times. The layout was correct; only color was lost.

Silent failure is the worst part: no warning, and the page still renders, so
it looks like a styling mistake rather than an unimplemented feature.

## 2. Alpha in color values — `rgba()` and `#RRGGBBAA`

Alpha-bearing color values render as fully transparent (nothing painted).
Notably this is specifically about *alpha*, not the color functions:

| Value | Result |
|---|---|
| `rgb(79,156,255)` | renders solid blue |
| `rgba(79,156,255,0.35)` | renders nothing |
| `#4f9cff59` | renders nothing |

`rgba` appears in `pkg/css/shorthand.go`, so it parses; the alpha does not
reach paint.

Impact here: all-day event pills and the precipitation bars under the
temperature curve are tinted with alpha and vanish entirely.

## 3. `linear-gradient()`

Renders nothing. Present in `pkg/css/background.go`, so it parses, but no
gradient is painted for the background shorthand path used here.

## 4. `border-radius`

Ignored — corners render square. Not found anywhere in `pkg/css` or
`pkg/layout`. Cosmetic, but it is what makes event blocks read as cards.

## 5. `box-shadow` (including `inset`)

Ignored. Not found in `pkg/css` or `pkg/layout`. The dashboard uses
`inset 3px 0 0 <color>` as a calendar-color spine on all-day pills; the
`border-left` fallback works, so this one is lowest priority.

## 6. `letter-spacing`

Ignored — glyph advance is unchanged. Not found in `pkg/css` or
`pkg/layout`. Used on small uppercase labels (`NEXT`, `NOW`, weekday
headers), where the tracking matters for legibility at a distance.

## 7. Context cancellation is a no-op on the HTML path (API, not CSS)

A hung or slow HTML render cannot be cancelled. Both halves of the pipeline
drop the context:

```go
// pkg/doctaculous/html_backend.go:248 — no ctx parameter at all
func OpenHTMLBytes(data []byte, opts ...HTMLOption) (*Document, error)

// pkg/doctaculous/reflow_backend.go:155 — ctx accepted then discarded
func (r *reflowRenderer) renderPage(_ context.Context, index int, opts RasterOptions) (image.Image, error)
```

`Document.RasterizePage` does thread its `ctx` down to `renderPage`, so the
call *looks* cancellable, but the underscore parameter means it is never
consulted. Parse and layout run under `context.Background()` regardless.

Impact here: the dashboard renders on a timer, forever, on a board with three
slow cores. A pathological document that sends layout into a very long loop
would wedge the render goroutine with no way to time it out — the caller can
only abandon it, not stop it. The work keeps consuming a core.

`OpenReader(ctx, ...)` accepts a context for the open phase, so that half has
a path forward; `renderPage` honouring its ctx (checking it between pages, or
between layout passes) would close the rest.

## 8. `overflow-wrap` / `word-break` — no mid-word breaking

Neither property exists in `pkg/css` or `pkg/layout`. Line breaking is
whitespace-only (`pkg/layout/inline/break.go`): a token with no break
opportunity keeps filling past its box rather than breaking mid-word.

`max-width` IS honoured (verified — `resolveContentWidth`/`clampMaxMin` apply
it, including to absolutely-positioned boxes), so the box is constrained; the
text simply overflows it.

Impact here: the error line renders a fetch failure like
`ical(Personal): GET https://…very-long-url…: 401 Unauthorized`. That is one
unbroken token, so on a long URL it runs past its 460px box and off the right
edge of the panel. `overflow: visible` is the default and is honoured, so
nothing clips it. Cosmetic — the surrounding layout is unaffected because the
box is out-of-flow — but the diagnostic becomes unreadable exactly when it
matters.

## Confirmed working

No action needed on these; recording them so the gaps above are unambiguous.

`display:flex` with `justify-content` · `display:grid` with
`grid-template-columns` · `position:absolute` inside `position:relative` ·
`border-left` and solid borders · `background`/`color` with literal hex and
`rgb()` · `text-transform:uppercase` · `opacity` on an element ·
`font-weight` · `white-space:nowrap` with `overflow:hidden` ·
`@font-face`-free system font selection · SVG icons inline via `<img>`-free
markup (the dashboard embeds `<svg>` directly and it rasterizes correctly).

## Suggested priority

1. **`var()`** — blocks any stylesheet using a palette, which is most modern CSS.
2. **alpha colors** — silently drops UI elements; the failure looks like a bug in the page.
3. **context cancellation** (§7) — correctness/robustness rather than appearance; matters for any long-running renderer.
4. **`linear-gradient`** — parses today, so the gap is surprising.
5. **`border-radius`**, **`letter-spacing`**, **`overflow-wrap`** — visual polish; `overflow-wrap` matters most when rendering diagnostics (§8).
6. **`box-shadow`** — lowest; `border-left` covers the common inset-spine case.
