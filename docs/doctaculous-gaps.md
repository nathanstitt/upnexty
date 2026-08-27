# doctaculous gaps found while building the Luckfox dashboard

> **Re-tested 2026-08-27 against doctaculous `957c9e1`**, by rasterizing each
> case and counting painted pixels rather than eyeballing output.
>
> | Gap | Status | Evidence |
> |---|---|---|
> | §0 inline `<svg>` | **FIXED** | empty svg paints 0, `<circle r=35>` paints 3924, `<rect>` fills 6400 — subtree renders with correct geometry |
> | §1 `var()` | broken | `background:var(--c)` paints **0** where `background:black` paints 6400 |
> | §2 alpha | broken | same: `rgba(…,0.9)` and `#000000e6` each paint 0 |
> | §3 `linear-gradient` | broken | paints 0 |
> | §4 `border-radius` | broken | 80×80 box, `border-radius:40px` paints 6400 — identical to the square box, so corners are not rounded (a circle would be ~5024) |
> | §5 `box-shadow` | broken | paints 0 |
> | §6 `letter-spacing` | broken in CSS | glyph run paints 321px with and without it — byte-identical |
> | §8 `overflow-wrap` | broken | identical output with and without; no mid-word break |
>
> **§0 is the only one fixed since this file was written.** Weather icons now
> render on the panel.
>
> Two failure modes are worth distinguishing, because they point at different
> parts of the pipeline:
>
> - **Element absent** (§1, §2, §3, §5) — nothing is painted at all. Not a
>   wrong color: zero pixels where a literal paints the full box.
> - **Declaration ignored** (§4, §6, §8) — the element paints correctly, but
>   the property has no effect on geometry.
>
> Reproduce with the probe harness described under *Testing method* below.

Six CSS features, one API issue (context cancellation, §7), and a font-fallback
failure mode (§9). §9 was found on real hardware; the rest by rasterizing on the
host.

Found while rendering `internal/view/assets/style.css` (a 1920×480 dark-theme
dashboard) through `doctaculous.OpenHTMLBytes` + `RasterizePage`. Every item
below was isolated in a minimal page and confirmed against the engine source.

**These are not worked around in this repo.** The dashboard's stylesheet is
written as correct CSS; it will render properly once these land.

Testing method — a browser preview cannot find these, because browsers
implement all of them. Only rasterizing through doctaculous shows the gap.

Counting painted pixels beats looking at the image: it distinguishes "element
absent" from "declaration ignored," and it caught one wrong conclusion in an
earlier revision of this file (a `border-radius` box *painted*, which looked
like success until the count showed the corners were still square).

```go
import "github.com/nathanstitt/doctaculous/pkg/doctaculous"

// nonWhite counts pixels that are not the white background.
func nonWhite(img image.Image) int {
    b, n := img.Bounds(), 0
    for y := b.Min.Y; y < b.Max.Y; y++ {
        for x := b.Min.X; x < b.Max.X; x++ {
            if r, g, bl, _ := img.At(x, y).RGBA(); r>>8 < 250 || g>>8 < 250 || bl>>8 < 250 {
                n++
            }
        }
    }
    return n
}

func render(html string) (int, error) {
    doc, err := doctaculous.OpenHTMLBytes([]byte(html), doctaculous.WithPageSize(400, 200))
    if err != nil {
        return 0, err
    }
    img, err := doc.RasterizePage(context.Background(), 0, doctaculous.RasterOptions{
        MaxWidthPx: 400, MaxHeightPx: 200, Background: color.White})
    if err != nil {
        return 0, err
    }
    return nonWhite(img), nil
}
```

Compare each case against a reference: `<div style="width:80px;height:80px;
background:black">` paints 6400. Anything that should paint and returns 0 is
absent; anything that returns exactly 6400 when it should differ is ignored.

## 0. Inline `<svg>` — FIXED, no action needed

**Resolved upstream as of `957c9e1`.** Recorded here because it was this
project's highest-impact gap and the note may still be circulating.

Inline `<svg>` now produces a box and paints its subtree with correct geometry.
Verified: an empty `<svg width=80 height=80>` paints 0 pixels; the same element
containing `<circle cx=40 cy=40 r=35>` paints 3924 (right for that radius); with
`<rect width=80 height=80>` it fills 6400. Weather icons render on the panel.

The original diagnosis was that `pkg/svg` already existed and only the HTML
frontend wiring was missing — that appears to be what landed.

## 1. CSS custom properties — `var()`

`var(--x)` silently resolves to nothing and the declaration is dropped. No
`var()` or custom-property handling exists in `pkg/css`, and the feature is
absent from `FEATURES.md`.

```css
:root { --bg: #0b0d12; --fg: #4f9cff; }
.a { background: var(--bg); color: var(--fg); }   /* paints nothing at all */
.b { background: #0b0d12;   color: #4f9cff;   }   /* renders correctly */
```

The declaration is **dropped, not defaulted** — measured 2026-08-27: an 80×80
box with `background: var(--c)` paints **0** non-white pixels, where the same
box with `background: black` paints 6400. Nothing is drawn where the element
should be.

Impact here: the dashboard's entire dark theme disappeared — black text on a
white background — because the palette is defined once in `:root` and
referenced 18 times in `internal/view/assets/style.css` (and 20 more in
`internal/portal/assets/portal.css`). The layout was correct; only color was
lost.

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

**A complete parser already exists in the SVG path.** `pkg/svg/color.go` parses
the full CSS Color 4 named table, `#rgb`/`#rgba`/`#rrggbb`/`#rrggbbaa`, and
`rgb()`/`rgba()`/`hsl()`/`hsla()` — including alpha. The CSS path does not reach
it. That makes this look like a wiring job rather than new parsing work.

Impact here: all-day event pills and the precipitation bars under the
temperature curve are tinted with alpha and vanish entirely.

## 3. `linear-gradient()`

Renders nothing — paints 0 pixels.

It is **explicitly rejected, not silently dropped**: `pkg/css/background.go:143`
treats `linear-gradient(...)` as an unsupported `<image>` and returns `ok=false`.
So the engine knows it cannot handle this; there is simply no gradient painter
on the CSS background path. (`pkg/render/raster/shading.go` has an axial shader
for SVG gradients, which may be reusable.)

## 4. `border-radius`

Ignored — corners render square. Not found anywhere in `pkg/css` or
`pkg/layout` (0 non-test hits).

Verified by pixel count rather than by eye: an 80×80 black box paints 6400
pixels with and without `border-radius:40px`. A 40px radius on that box should
produce a circle of roughly 5024 pixels, so the identical count shows the
corners are untouched. Cosmetic, but it is what makes event blocks read as
cards.

## 5. `box-shadow` (including `inset`)

Ignored. Not found in `pkg/css` or `pkg/layout`. The dashboard uses
`inset 3px 0 0 <color>` as a calendar-color spine on all-day pills; the
`border-left` fallback works, so this one is lowest priority.

## 6. `letter-spacing` — implemented in SVG, absent in CSS

Ignored on the CSS path: `III` at 30px paints 321 pixels with and without
`letter-spacing:20px` — byte-identical, so glyph advance is unchanged.

**But it already works in SVG.** `pkg/svg/style.go:156` resolves
`letterSpacingPt`/`wordSpacingPt`, and a comment there notes that after that
change "letter-spacing works in SVG and silently does nothing" elsewhere. So
the property is understood by the engine; the CSS/layout text path just does
not consult it.

Used on small uppercase labels (`NEXT`, `NOW`, weekday headers, and the panel's
`SET ME UP`), where tracking matters for legibility at a distance.

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

`max-width` IS honoured (verified — `resolveContentWidth` and `clampMaxMin`
in `pkg/layout/css/block.go:1148,1219` apply it, including to
absolutely-positioned boxes), so the box is constrained; the
text simply overflows it.

Impact here: the error line renders a fetch failure like
`ical(Personal): GET https://…very-long-url…: 401 Unauthorized`. That is one
unbroken token, so on a long URL it runs past its 460px box and off the right
edge of the panel. `overflow: visible` is the default and is honoured, so
nothing clips it. Cosmetic — the surrounding layout is unaffected because the
box is out-of-flow — but the diagnostic becomes unreadable exactly when it
matters.

## 9. Missing glyphs render as nothing, with no fallback or warning

Not strictly an engine defect — the board genuinely has no emoji font (DejaVu
and Liberation only) — but the *failure mode* is worth fixing. A character with
no glyph in any available font renders as empty space: no tofu box, no
`.notdef`, no warning.

Measured on the board with the 9 weather emoji the previous Pi dashboard used:
3 rendered (☀ ☁ ❄, as monochrome DejaVu glyphs) and 6 rendered as nothing.

Because some rendered and some didn't, the result reads as a layout gap rather
than a font problem — the hardest kind of bug to spot. Drawing `.notdef` (or
logging once per missing glyph) would turn a silent hole into an obvious one.

## Confirmed working

No action needed on these; recording them so the gaps above are unambiguous.

`display:flex` with `justify-content` · `display:grid` with
`grid-template-columns` · `position:absolute` inside `position:relative` ·
`border-left` and solid borders · `background`/`color` with literal hex and
`rgb()` · `text-transform:uppercase` · `opacity` on an element ·
`font-weight` · `white-space:nowrap` with `overflow:hidden` ·
`max-width` including on absolutely-positioned boxes ·
`@font-face`-free system font selection · **inline `<svg>`** (§0 — fixed
upstream; the dashboard embeds `<svg>` directly and it rasterizes correctly).

## Suggested priority

1. **`var()`** (§1) — blocks any stylesheet using a palette, which is most modern CSS. On this project it is the difference between the intended dark theme and black-on-white.
2. **alpha colors** (§2) — silently drops UI elements; the failure looks like a bug in the page. `pkg/svg/color.go` already parses alpha, so this may be wiring rather than new work.
3. **missing-glyph fallback** (§9) — a `.notdef` box would make font gaps visible instead of silent.
4. **context cancellation** (§7) — correctness/robustness rather than appearance; matters for any long-running renderer.
5. **`linear-gradient`** (§3) — explicitly rejected today, and `pkg/render/raster/shading.go` has an axial shader that may be reusable.
6. **`border-radius`**, **`letter-spacing`**, **`overflow-wrap`** — visual polish. `letter-spacing` already works in SVG (§6), so the CSS path is the gap; `overflow-wrap` matters most when rendering diagnostics (§8).
7. **`box-shadow`** — lowest; `border-left` covers the common inset-spine case.

§0 (inline `<svg>`) was previously first on this list and is now resolved.
