# omnidoc gaps found while building the Luckfox dashboard

> **Re-verified 2026-08-31 against omnidoc `b59f54e`**, after the project was
> renamed from doctaculous. Every finding here is a measurement: rasterize the
> case and assert on the size of the box you are making a claim about.

Nine findings are live — two found on 2026-08-31 building the tap dialog, two
more (17, 18) building the left panel's vertical layout, and three (18b, 20,
21) on 2026-09-01 fixing spacing and alignment that had been reported as done.
All of them cost several wrong diagnoses before a control case settled them.
Everything else this document used to carry has been fixed upstream and
its workaround removed from this repo; the table at the bottom lists what, so
the removals stay traceable.

| # | Finding | Effect | State |
|---|---|---|---|
| 20 | `text-align` does not reach glyphs in a vertical `writing-mode` | letters of differing width read as ragged | **open** |
| 18b | a flex ROW sizes a stretched child to the full container height | child overshoots by padding-top + padding-bottom | **open** |
| 21 | a centred flex row swallows a child's own bottom spacing | text sits on the rule below it | **open** |
| 15 | a flex container drops a bare text child | button paints, label vanishes | **open** |
| 17 | child-combinator selector (`a > b`) never applies | rule parses, declarations silently ignored | **open** |
| 18 | column flex container ignores bottom spacing when sizing children | last child overruns the container edge | **open** |
| 19 | a flex row is far taller than its content and ignores its own padding | rows pad themselves out, opening dead space | **open** |
| 16 | `display: contents` unimplemented | grid rows collapse onto each other | **open** |
| 14b | vertical shrink-to-fit box sized on the horizontal axis | auto-width box far too wide | **open** |
| 3 | `sysfont` matches a registry, not the disk | installed font never found | open (by design) |

14b and 15 have runnable cases in `engine-probes/`. 3 does not: it is about
font resolution on the board, not layout, so a rasterized probe cannot show it.

"Performance findings" near the bottom is separate from this list — those are
not defects, and they are not counted above.

15 and 16 were both found building the tap dialog, and both are the same shape
of problem: **the box paints and its content does not**, so every structural
check passes and only a rasterized pixel count sees it.

---

## 19. A flex row is much taller than its content

A `display: flex` row with one line of text measures far more than the text
needs, and its declared vertical padding is not what is rendered.

Measured on `#clock-row`: box 108px tall for 37px of ink, with 33px above and
**38px below** — against a declared `padding: 14px 18px 2px`. The 2px bottom
padding rendered as 38. `line-height: 1` on the container (not just on the
children) recovers ~11px; the rest does not respond to padding, line-height, or
`flex: 0 0 auto`. Children as blocks with an explicit line-height made it worse
(90px). A control with `align-items` set to baseline/center/unset measures the
same, so alignment is not the cause — the flex line simply takes the font's full
ascent+descent.

Cost here: ~50px of dead space between the clock and the weather widget, and
another ~40px below it, on a 480px panel. It reads as a deliberate-looking
layout mistake, which is how it survived several rounds of review — the panel
looks *designed*, just badly, so nothing prompts you to measure it.

**Workaround:** state an explicit `height` on the row. That is what pins it, and
it is safe when the content is a known number of lines at a known size. Both
`#clock-row` (62px) and `#wx-widget` (84px) do this, with the ink measurements
in comments so the numbers can be re-derived rather than guessed at.

## 18. A column flex container ignores its bottom spacing

`padding-bottom` on a `flex-direction: column` container is not reserved when
its flex children are sized. `padding-top` is honoured, so the asymmetry is easy
to miss: content starts in the right place and runs off the wrong end.
`margin-bottom` on the last flex child is ignored the same way.

Verified with two identical columns, 200px tall, each holding a `flex-grow: 1`
box and a 40px fixed box. With `padding: 14px 0 20px` the fixed box lands at
174..214 -- 14px past the container. Replacing the padding with a zero-flex
20px spacer element puts it at 154..194, correctly inside.

Cost here: the left panel's "at 10:52 AM" line was clipped off the bottom edge
of the panel. `#now-content` clips its overflow, so nothing errored -- the line
was simply absent, which reads as a rendering glitch rather than a layout bug.

The fix is a spacer element (`.nb-pad`), since an explicit height on a
zero-flex box IS honoured. Do not "simplify" it back into padding or a margin.

## 18b. A flex row sizes a stretched child to the full container height

The row variant of 18, and worse: `padding-top` is honoured as an *offset* but
not subtracted from the child's height, so an `align-items: stretch` child
overshoots the bottom by padding-top + padding-bottom.

Measured with a 240px row declared `padding: 10px 18px 12px` holding one
stretched child. The child spans **10..249** -- correctly offset by the 10, then
240 tall from there -- against the 10..227 it should occupy.
`box-sizing: border-box` on either box makes no difference.

Cost here: every event card's 1px bottom border was drawn 22px below the agenda
band and clipped away, so the cards read as open-bottomed boxes. It also put
the render 22px out of step with `model.CardRects`, which computes tap targets
from the same constants and was correct all along -- so a tap near a card's
bottom edge hit nothing.

18's fix does not transfer: a zero-flex spacer element can inset a *column*'s
children, but nothing equivalent works across a row's block axis.

Two things do work, both verified:

| approach | child | row's flow height |
|---|---|---|
| `height: 218px` on the row (content-box) | 10..227 correct | **218** -- shrinks |
| margins on the children, no vertical padding on the row | 10..227 correct | 240 correct |

The height fix renders correctly but shrinks the row's flow height to 218,
which breaks the 480px vertical budget below it. The margins are what this repo
uses. Write them as plain class selectors: `#agenda-row > *` parses and is
silently ignored (17), so the rule would apply to nothing.

---

## 17. A child-combinator selector never applies

`#parent > .child { ... }` parses without error and is silently ignored — the
declarations never reach the element. A plain class selector carrying the exact
same declarations applies normally.

Verified with both forms in one document, on adjacent columns of identical
markup: `.nowplain { margin-top: auto; margin-bottom: auto }` centres its box in
a `justify-content: space-between` column; `#a > .now { ... }` with the same two
declarations leaves the box at the top.

Cost here: the left panel's headline stayed pinned under the weather rule on a
day with nothing upcoming, while the two-group case looked right — so it read as
a flexbox free-space problem, not a selector problem. Two plausible-but-wrong
diagnoses (`:only-child` not matching, then gap #15's anonymous text items) were
each disproved by a probe before this one landed. A rule that silently does
nothing is worth reaching for a control early.

`style.css` therefore states `.nb-now` as a plain class and says so inline. If
the engine gains combinator support, that comment can go — but a passing render
is the only evidence that should retire it.

## 15. A flex container drops a bare text child

`<div style="display:flex">Label</div>` paints its background and **not its
text**. Measured on the dialog's action button: 120 dark pixels inside the
amber box with flex, ~1400 with the text in an element.

Bare `display: flex` is enough -- no `align-items`, no `justify-content`. That
is what makes it easy to misdiagnose: the first read here was "align-items
breaks centring", and the control case that settled it was a box with flex and
nothing else. The engine is not wrapping the anonymous text in a flex item.

This is how a button is normally built, which is why it matters more than the
size of the bug suggests. The failure is an **unlabelled control that still
paints its background** -- it reads as a deliberate blank box, not as damage,
and on a panel with no other affordance it is simply dead.

**Workaround:** either wrap the text in an element (`<div class=btn><span>` --
keeps flex), or drop flex and centre with `line-height` equal to the box
height. The dialog's buttons use the second: no extra element, and a single
line of text is all a button holds.

Pinned by `TestDialogButtonLabelsAreVisible`, which counts glyph ink inside the
button's rect and was verified to fail when the rule is switched back to flex.

---

## 16. `display: contents` is unimplemented

A `display: contents` element is laid out as a normal block rather than being
removed from the box tree, so its children do not become grid items of the
grandparent. A two-column grid whose rows are `display: contents` wrappers
collapses: every row lands in column one, stacked on its neighbour.

Found on the dialog's label/value rows, which were built as a grid with
`.dlg-row { display: contents }` -- the standard way to keep row markup while
letting the grid align columns.

**Workaround:** one flex row per line with a fixed-width label. The column edge
lines up the same way; what is lost is the grid's ability to size the label
column to its widest member, so that width is stated (`132px`) instead.

---

## 20. `text-align` does not reach glyphs in a vertical `writing-mode`

Sibling of 14b — same element, `#now-bar-label`, and again the axes not being
swapped. 14b is the box's *size*; this is the *placement of glyphs inside it*.

With `writing-mode: vertical-rl; text-orientation: upright`, each letter is
placed on the block axis by its own advance width and `text-align` has no
effect. In Barlow Condensed 700 at 20px, N and O carry 8px of ink against W's
12px, so the three letters centre 2px apart and the stack reads ragged.

Probed four ways against a control, all measuring the spread between per-letter
centres:

| markup | spread |
|---|---|
| `width: 20px; text-align: center` | 2.5px |
| `width: 20px`, no `text-align` | 2.5px |
| no stated width | 2.5px |
| per-letter `<span>`s, each `text-align: center` | 2.5px |
| **stacked blocks, no `writing-mode`** | **0.0px** |

Identical in all four vertical cases, including per-letter spans with their own
alignment — which is what rules out a CSS fix rather than a mistake in ours.

**Workaround:** drop `writing-mode` and stack one block per letter, so each
letter is an ordinary horizontal line box where `text-align` applies normally.
The template spells out `<div>N</div><div>O</div><div>W</div>` for this reason.

A flex column (`align-items: center`, or `justify-content: center` per letter)
measures the same 0.0px and is equally correct. Both were tried; `text-align`
was kept because it needs no flex container and so cannot meet 15. Note that
the per-letter flex variant *does* meet 15 if the letter is a bare text child:
`<div style="display:flex">N</div>` rendered **no ink at all**. Wrap the letter
in an element if you use flex here.

`TestNowLabelLettersAreCentred` measures the rendered spread; it fails at 2.0px
against the `writing-mode` markup.

---

## 21. A centred flex row swallows a child's own bottom spacing

`#wx-widget` is `display: flex; align-items: center` with a fixed height. Its
child `#wx-temp-block` holds the temperature over the condition text, and the
condition text was sitting directly on the row's `border-bottom`.

Neither `margin-bottom` nor `padding-bottom` on that text moves it. Because the
row centres its children, height added inside the child pushes the whole block
*down* by half of what it adds beneath — so the gap below barely changes:

| rule | rendered gap |
|---|---|
| `#wx-desc { margin-bottom: 6px }` | 2px |
| `#wx-desc { padding-bottom: 6px }` | 2px |
| **`#wx-widget { padding: 6px 18px 10px }`** | **3px+** |

This is arguably correct centring rather than a bug, but it is worth recording
because the failure is invisible in the stylesheet: the declaration is present,
plausible, and does nothing. It shipped twice as "fixed" for that reason.

Related to 18 and 19, which are also about flex containers and spacing being
dropped, but distinct: those concern a *column* container ignoring its own
bottom spacing, this one a *centred row* absorbing a child's.

**Workaround:** move the spacing to the flex row's own padding, which shifts the
centred block as a unit. `TestConditionTextClearsTheHairline` measures the
rendered gap between the glyph ink and the rule.

---

## 14b. A vertical shrink-to-fit box is sized on the horizontal axis

Uncovered by the `writing-mode` fix, and reported by upstream in `4543fa3`'s own
commit message rather than found here. A shrink-to-fit box — `inline-block`,
float, table cell, flex/grid item, or anything absolutely positioned — is sized
by the intrinsic measure helpers, and those shape content and break it at a
*width*. In a vertical writing mode the axes swap, so the box comes out as wide
as its text is long instead of about one em.

Measured on `#now-bar-label` in the real dashboard page, which is
`position: absolute` and so takes this path:

| `width` | box | ink |
|---|---|---|
| `16px` (ours) | **16 x 60** | 18 x 54 |
| `auto` | **36 x 60** | 18 x 54 |

Only the cross size is wrong; the text itself lays out correctly either way.
`engine-probes/14-vertical-shrink-to-fit.html` isolates it at 51px vs 16px for
the same ~18px of ink.

Transposing this means changing `measureContent`, which table, grid, flex and
inline-block sizing all share — upstream deliberately left it out of the
writing-mode branch rather than turn that seam without its own tests.

**Workaround:** state an explicit `width`. `#now-bar-label` already had one, so
adopting `writing-mode` cost nothing here; the rule now carries a comment saying
why it cannot be dropped.

The engine logs `a shrink-to-fit box in a vertical writing-mode is sized on the
horizontal axis`. Note it is a `warnOnce` **per document**, so the line appears
even when every such box states a width and is sized correctly — the log marks
"this document has one of these", not "this box is wrong". Measure the box.

---

## 3. `sysfont`'s registry lacks most families

Installing a font on the board does **not** make it available, in any directory.

omnidoc resolves OS fonts through `adrg/sysfont`, which matches against a
hardcoded registry (`fonts.go`) rather than scanning what is on disk. That
registry has 32 DejaVu entries and **zero** for Roboto, Barlow Condensed, or IBM
Plex Mono. An unregistered family is never found, whatever directory it is in.

Confirmed: the fonts were installed to `/usr/share/fonts/upnext/` — the real
`xdg.FontDirs` path, alongside the DejaVu that does resolve — and still rendered
as DejaVu. Even `font-family: 'DejaVu Sans Condensed'`, a face the board
genuinely ships, did not resolve distinctly.

**It does not silently substitute.** `LoadStyled`
(`pkg/layout/font/osfont.go`) decodes whatever sysfont returns, compares the
font's *declared* family against the request, and rejects a mismatch — logging
`osfont: %q resolved to %q (%s); rejecting mismatch, falling back` before
falling through to the bundled face. The logger is wired on both the HTML and
PDF paths, so the fallback is reported.

That check is there for exactly the reason this entry once assumed was
unhandled: `sysfont.Match` never reports a miss, and returns "a suitable
default" for an unknown family. Upstream's own comment calls the check
load-bearing and records the measurements behind it (a request for `"ZZZZ
Totally Fake 12345"` returned Arial Unicode MS — the same bytes returned for
Roboto and IBM Plex Mono).

So the gap is narrow: not wrong-font substitution, but that sysfont cannot
*find* an installed font it has no registry entry for. Scanning the font
directories would make `@font-face` a choice rather than the only route. The
fallback behaviour itself is correct, which is why this is listed as by design.

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

## Performance findings

Not gaps — the engine is correct here, just slow on this CPU. Kept in this file
because the same discipline applies: measure the thing you are claiming about,
with a control, or the number will be wrong.

### The glyph outline cache is worth ~2–3% here, not ~20%

omnidoc `dbab5c4` ("perf(font): memoize decoded glyph outlines") memoizes
`Face.Outline` per `(program, GID)`. Its commit message reports ~18% of a render
in `program.outline` and a 23% speedup, measured on omnidoc's `testdata/htmldoc`
showcase.

**On the dashboard document that is ~2–3%.** Measured 2026-09-01 on the board,
A/B against the same commit reverted in a worktree, rasterize-only (no network),
three interleaved rounds of three iterations each:

| | mean | median | range |
|---|---|---|---|
| cache reverted | 7757ms | 7744ms | 7655–7914 |
| **cache present** | **7615ms** | 7599ms | 7460–7811 |

141ms, 1.8%. Discarding round 1, where both arms were cold: 7771ms → 7544ms,
**227ms / 2.9%**. The win is consistent — every warm cached iteration beat every
uncached one — it is just small.

The difference is the document, not the engine. The showcase page is text-dense
(50,841 outline requests over 380 distinct glyphs, per that commit). The
dashboard frame is largely SVG — weather icons and the chart — so glyph decoding
was never a fifth of its cost, and removing nearly all of it recovers a few
percent. **A profile share from one document does not transfer to another**;
that is the whole finding.

This does not argue against the cache. It is free, correct, and byte-identical
in output. It is an argument against expecting it to move the frame budget: the
remaining ~7.6s is layout and SVG rasterization, which is where
`docs/omnidoc-render-perf.md`'s asks still point.

### Do not A/B this through `--once`

The first attempt timed `adb shell /root/dashboard --once` end to end and showed
the **cache arm slower** (9.4s vs 9.0s) with a 14.4s first-trial outlier. That
was wrong, and wrong in the direction that invites a bogus revert.

`--once` includes `fetchAll` — two network fetches and a `wpa_cli` subprocess —
whose variance is several hundred ms, comfortably larger than the ~200ms effect
being measured. The signal only appeared after isolating the rasterize step
against a fixed document captured from the board:

```bash
adb shell '/root/dashboard --once --html-out /tmp/frame.html'   # real frame
# then rasterize that same file N times, no network, interleaving both builds
```

Interleave the arms rather than running all of one then all of the other, so
thermal or scheduler drift hits both equally, and discard the first round of
each binary — a cold page cache on a 12MB binary read from NAND costs seconds.

Two operational notes: stop `S99zdashboard` first (two full-page images on
512MB is enough to wedge the board — the same warning `omnidoc-render-perf.md`
gives), and watch `df`. Two 17MB dashboard variants alongside the installed one
took `/root` to 99% full, which is its own source of timing noise.

---

## Fixed upstream

Each was re-verified by re-running its probe before the workaround came out of
this repo. Findings 1–13 were live against `fb42ebe` and fixed as of `15ea0c4`;
8 and 14 were closed after that. Listed so the removals are traceable — the
probes went with them.

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
| 12 | absolutely positioned / flex box never shrink-wraps | `ccd6dd2` |
| 13 | comma-separated `background` list dropped | `5d311b5` |
| 8 | auto-height flex column omits child margins | [#143] |
| 14 | `writing-mode` ignored | `8f7a2f9`, `4543fa3` |

[#143]: https://github.com/nathanstitt/omnidoc/pull/143

Two of those were never real. **12** did not reproduce when re-measured — the
badge hugged its text in every variant, and the original probe had been picking
up neighbouring elements. **8** was real but misdiagnosed: reported as "the
element and everything after it stop painting", when in fact everything painted
and only the container's own height was short.

Removing these workarounds took out roughly 200 lines: the whole of
`internal/view/textfit.go` (which measured glyph advances from the embedded TTFs
to truncate titles in Go, because CSS could not), the per-line box heights
throughout `style.css`, the stated card and stack heights in
`internal/model/agenda.go`, the spacer-width scheme that stood in for the
agenda's scroll offset, the `padding`-for-margin spacing inside flex containers,
and the `<span>`-per-letter stack behind the vertical NOW label.

## Testing method

Probes for the live gaps are checked in under `engine-probes/`, one file per
entry, named for the gap it demonstrates. Re-run them before trusting this
document if the engine has moved on — see that directory's README.

A browser preview cannot find any of this — browsers implement it all. Only
rasterizing through omnidoc shows the gap, and only a colour-specific count
distinguishes "painted correctly" from "painted black".

**Measure the box you are making a claim about.** Sampling page pixels is
necessary but not sufficient, and it is what produced both of the wrong findings
noted above: a page-level probe answers "does this look right", which is a
different question from "is this box the size CSS says it should be". A parent
block grows around an overflowing child, so the page can look plausible while
the box under test is 40px short.

When the claim is about a specific box, assert on that box: give it a unique
background colour and measure that colour's span, or query the layout directly.
Include a control in the same document — the same markup in a context that
works — and if the two measure the same, there is no bug.

```go
import "github.com/nathanstitt/omnidoc/pkg/omnidoc"

// Sample the centre pixel: coverage counts hide a wrong-colour paint.
func centre(html string) string {
    doc, _ := omnidoc.OpenHTMLBytes([]byte(html), omnidoc.WithPageSize(400, 200))
    img, err := doc.RasterizePage(context.Background(), 0, omnidoc.RasterOptions{
        MaxWidthPx: 400, MaxHeightPx: 200, Background: color.White})
    if err != nil {
        return "err"
    }
    r, g, b, _ := img.At(100, 50).RGBA()
    return fmt.Sprintf("rgb(%d,%d,%d)", r>>8, g>>8, b>>8)
}
```

`omnidoc.WithLogf` is worth wiring into any probe: the engine reports most of
what it does not implement, and 14b was confirmed from its log line before it
was measured.

Render against the real `#07080d` background, not white: a faint white-alpha
gradient is invisible on white and reads as a false "paints 0". That produced a
wrong `linear-gradient` finding in an earlier revision of this file.

Some defects only appear on the panel. The forecast row's clipped bottom line was
invisible in both the golden HTML and the host-side raster, because nothing clips
at the document level.
