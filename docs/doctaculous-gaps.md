# doctaculous gaps found while building the Luckfox dashboard

> **Re-verified 2026-08-29 against doctaculous `15ea0c4`.** Every finding here
> is a measurement: rasterize the case and sample pixels of a *specific colour*.
> That method matters — an earlier probe counted "any non-background pixel" and
> reported success on a case that was painting black.
>
> **Corrected 2026-08-29 after re-measuring upstream.** Finding 12 does not
> reproduce, finding 8's symptom was misdiagnosed (the container was short; no
> content was lost), and finding 3's "silently substitutes" claim was wrong —
> the fallback logs. Colour-specific sampling was still not enough: it answers
> "does the page look right", not "is this box the size CSS says". See
> "Testing method".

Most of what this document used to list has been **fixed upstream**. One live
engine issue remains, plus one enhancement that is working as designed; the rest
is kept as a short history at the bottom so a returning reader knows which
workarounds were removed and why.

| # | Finding | Effect | State |
|---|---|---|---|
| 14 | `writing-mode` ignored | vertical text lays out horizontally | **open** |
| 3 | `sysfont` matches a registry, not the disk | installed font never found | open (by design) |
| 8 | flex container height omits child margins | following content clipped | **fixed** ([#143]) |
| 12 | absolutely positioned box never shrink-wraps | — | **not reproducible** |

[#143]: https://github.com/nathanstitt/doctaculous/pull/143

Runnable cases for each are in `engine-probes/`.

---

## 8. A flex container's height omits its children's margins — FIXED

**Fixed upstream in [#143]**, which lands the one-line omission behind this: the
branch of `layoutFlex` that content-sizes a line when the main size is
*indefinite* summed each item's border box instead of its margin box. Every
other site — line packing, free-space distribution, cross sizing, stretch —
already used outer sizes; this one was missed by `fb5d58c`.

Only auto-height containers were affected. A container with an explicit `height`
takes a different branch and was always correct, which is why the bug survived
upstream's own showcase (its column demo states `height: 110px`).

The analysis below was correct and is kept for the record.

`fb5d58c "css: honor margins on flex children"` made the
margin affect *layout* — children are now placed correctly — but the container
does not count the margin in its own measured height. It reports the unmargined
total, so whatever follows is laid out overlapping the column's real content and
is clipped away.

Three 20px boxes in a `flex-direction: column`, the middle carrying
`margin-top: 40px`, followed by a marker element:

| | Correct | Actual |
|---|---|---|
| box 1 | 0–19 | 0–19 ✓ |
| box 2 (margined) | 60–79 | 60–79 ✓ |
| box 3 | 80–99 | 80–99 ✓ |
| column height | 100px | **60px** |
| marker after the column | 100–119 | **never painted** |
| document height | 120px | **80px** |

So the children land where they should — the earlier reading of this as "boxes
disappear" was an artifact of the page being cropped to the under-reported
height. The visible damage is to anything *after* the container.

A plain block parent is the control and measures 100px correctly, so this was
specific to flex.

One correction to the note above: cross-axis margins are **not** ignored.
`margin-left` on a child of a column applies, and the line grows to hold the
margin box — upstream has had a test pinning this since `fb5d58c`
(`TestFlexChildMarginsApply`). Only the container's *height* was wrong, and only
on the main axis, which is why a cross margin looked unaffected: it never
contributed to the height in the first place.

**Workaround (no longer needed):** `padding` was used for spacing inside a flex
container, since it is counted in both contexts. Note it changes what a
background paints over, so reverting it is not a blind substitution on elements
that have one.

`.nb-lead`, `.nb-free`, `.nb-next*` and `#agenda-row` all use padding for this
reason and can move back to margins once the engine bump lands. Negative margins
and `transform` on a flex child work correctly — the agenda's pre-scroll uses
`transform: translateX()`.

Repro: `engine-probes/08b-margin-flex-child.html`.

---

## 12. An absolutely positioned box never shrink-wraps — NOT REPRODUCIBLE

**This does not reproduce against `15ea0c4`.** Re-measured, every case is
correct — the badge hugs its text, and `width` is honoured exactly:

| Badge style | Reported | Re-measured |
|---|---|---|
| `left: 110px` | 110 → 201 (stretched) | 110 → 168 (**width 59, hugs text**) |
| `right: 0` | 104 → 201 (stretched) | 137 → 195 (**width 59, right-anchored**) |
| `left: 110px; width: 76px` | width ignored | 110 → 185 (**width 76, exact**) |
| `display: inline-block` | 0 → 91 | 0 → 58 (agrees with the positioned cases) |

`ccd6dd2` fixed this — the same commit already credited below for findings 9,
10, 10b and 11. `absShrinkToFitWidth` (`pkg/layout/css/block.go`) implements
CSS 10.3.7. This entry was simply not re-checked when the others were.

**The flex-child half does not reproduce either.** A "Company Holiday" pill in a
flex row measures 94px of painted background, and its glyph coverage is
byte-identical to the same pill as an `inline-block` — so no glyph is cut. The
reported 85px content box and the "Company Holida" truncation are not present.

**Consequence for this repo:** the `inline-block` workaround can be reverted.
It was the more costly of the two, because an `inline-block` badge is not
positioned and had to be moved into normal flow — it can go back to being
pinned to the card corner with `position: absolute`.

---

## 14. `writing-mode` is ignored

`writing-mode: vertical-rl` lays out identically to horizontal text — a probe
measured the same 225x32 box either way. The reference's vertical NOW label is
built instead from one `<span>` per letter, each a block of fixed height.

---

## 3. `sysfont`'s registry lacks most families

Installing a font on the board does **not** make it available, in any directory.

doctaculous resolves OS fonts through `adrg/sysfont`, which matches against a
hardcoded registry (`fonts.go`) rather than scanning what is on disk. That
registry has 32 DejaVu entries and **zero** for Roboto, Barlow Condensed, or IBM
Plex Mono. An unregistered family is never found, whatever directory it is in.

Confirmed: the fonts were installed to `/usr/share/fonts/upnext/` — the real
`xdg.FontDirs` path, alongside the DejaVu that does resolve — and still rendered
as DejaVu. Even `font-family: 'DejaVu Sans Condensed'`, a face the board
genuinely ships, did not resolve distinctly.

**Correction: it does not "silently substitute".** This entry previously said an
unregistered family silently resolves to DejaVu. Reading upstream
(`pkg/layout/font/osfont.go`), `LoadStyled` decodes whatever sysfont returns,
compares the font's *declared* family against the request, and rejects a
mismatch — logging `osfont: %q resolved to %q (%s); rejecting mismatch, falling
back` before falling through to the bundled face. The logger is wired on both
the HTML and PDF paths, so the fallback is reported, not silent.

That check is there for exactly the reason this entry assumed was unhandled:
`sysfont.Match` never reports a miss, and returns "a suitable default" for an
unknown family. Upstream's own comment calls the check load-bearing and records
the measurements behind it (a request for `"ZZZZ Totally Fake 12345"` returned
Arial Unicode MS — the same bytes returned for Roboto and IBM Plex Mono).

So the real gap is narrower than stated: not wrong-font substitution, but that
sysfont cannot *find* an installed font it has no registry entry for. Scanning
the font directories would make `@font-face` a choice rather than the only
route. The fallback behaviour itself is correct.

**Workaround:** `@font-face` with `url()`, served through `WithResourceLoader`.

```css
@font-face { font-family: 'Barlow Condensed'; font-weight: 600;
  src: url('fonts/BarlowCondensed-SemiBold.ttf') format('truetype'); }
```

`internal/view.FontLoader` serves these from the embedded asset FS, so the
typefaces ship inside the binary (~1.1MB for 10 faces) and there is nothing to
deploy or lose. Verified on the panel: Barlow renders genuinely condensed, Plex
as true monospace, Roboto Bold correctly.

If you add faces: Google's per-weight TTFs declare their own family names —
`BarlowCondensed-SemiBold.ttf` declares family "Barlow Condensed SemiBold", not
"Barlow Condensed" weight 600. Only Regular and Bold declare the base family.
With `@font-face` this does not matter, because the `font-family` in the rule is
what the document sees; it only breaks OS-level resolution.

---

## Fixed upstream

These were all live against `fb42ebe` and are fixed as of `15ea0c4`. Each was
re-verified by re-running its probe, and the workaround has been removed from
this repo. Listed so the removals are traceable.

| # | Finding | Fixed by |
|---|---|---|
| 1 | `font-family` must end in a generic keyword | `c1741b3` font substitution |
| 2 | CSS does not cascade into inline `<svg>` | `0d0eb3f` |
| 2b | SVG presentation attributes do not inherit to children | `0465a9c` |
| 4 | `max-height` / `overflow:clip` do not clip | `117534f` |
| 5 | `-webkit-line-clamp` unimplemented | `7927202` |
| 6 | `color-mix()` unimplemented | `3bc10c3` |
| 7 | `line-height` ignored | `7999a8f` |
| 9 | `z-index` does not order positioned siblings | `ccd6dd2` |
| 10 | `top`+`bottom` does not size a box | `ccd6dd2` |
| 10b | `left` ignored on an absolute child of a flex box | `ccd6dd2` |
| 11 | a flex-derived height blocks `justify-content` | `ccd6dd2` |
| 12 | absolutely positioned / flex box never shrink-wraps | `ccd6dd2` (missed in the last pass) |
| 13 | comma-separated `background` list dropped | `5d311b5` |
| 8 | auto-height flex column omits child margins | [#143] |

Removing those workarounds took out roughly 200 lines: the whole of
`internal/view/textfit.go` (which measured glyph advances from the embedded TTFs
to truncate titles in Go, because CSS could not), the per-line box heights
throughout `style.css`, the stated card and stack heights in
`internal/model/agenda.go`, and the spacer-width scheme that stood in for the
agenda's scroll offset.

## Testing method

The probe for each gap below is checked in under `engine-probes/`, one file per
entry, named for the gap it demonstrates. Re-run them before trusting this
document if the engine has moved on — see that directory's README.

A browser preview cannot find any of this — browsers implement it all. Only
rasterizing through doctaculous shows the gap, and only a colour-specific count
distinguishes "painted correctly" from "painted black".

**Measure the box you are making a claim about.** Sampling page pixels is
necessary but not sufficient, and it is what produced the two wrong findings
corrected in this revision:

- Finding 8 was reported as "the element and everything after it stop painting".
  Every element painted. The container was the wrong size, and a parent block
  grew around the overflowing child, so the *page* looked plausible while the
  flex container itself was 40px short. A red-pixel count over the page cannot
  see that; asserting on the container's own height can, which is what the
  upstream regression test now does.
- Finding 12 reported a stretched badge. Re-measuring the badge's own painted
  span showed 59px, hugging its text, in every variant.

A page-level probe answers "does this look right", which is a different question
from "is this box the size CSS says it should be". When the claim is about a
specific box, assert on that box: query the layout, or measure the span of that
element's own background colour, rather than counting pixels page-wide.

```go
import "github.com/nathanstitt/doctaculous/pkg/doctaculous"

// Sample the centre pixel: coverage counts hide a wrong-colour paint.
func centre(html string) string {
    doc, _ := doctaculous.OpenHTMLBytes([]byte(html), doctaculous.WithPageSize(400, 200))
    img, err := doc.RasterizePage(context.Background(), 0, doctaculous.RasterOptions{
        MaxWidthPx: 400, MaxHeightPx: 200, Background: color.White})
    if err != nil {
        return "err"
    }
    r, g, b, _ := img.At(100, 50).RGBA()
    return fmt.Sprintf("rgb(%d,%d,%d)", r>>8, g>>8, b>>8)
}
```

Render against the real `#07080d` background, not white: a faint white-alpha
gradient is invisible on white and reads as a false "paints 0". That produced a
wrong `linear-gradient` finding in an earlier revision of this file.

Some defects only appear on the panel. The forecast row's clipped bottom line was
invisible in both the golden HTML and the host-side raster, because nothing clips
at the document level.
