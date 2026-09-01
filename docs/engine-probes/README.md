# Engine probes

One minimal HTML case per gap in `../omnidoc-gaps.md`, named for the gap it
demonstrates. Each isolates a single property so the result is unambiguous:
a case that renders sits beside one that does not, in the same document.

Only the **live** gaps have files here — currently just the vertical
shrink-to-fit sizing bug. Probes for the fixed ones were removed along with
their workarounds; the gaps doc lists what they were and which commit closed
each. Re-add a probe if a symptom returns.

`14-writing-mode.html` became `14-vertical-shrink-to-fit.html` when
`writing-mode` was fixed: the property works, and the same document now isolates
the narrower sizing gap that fixing it uncovered.

These exist because every claim in the gaps doc is a measurement, and a
measurement nobody can repeat is a rumour. Re-run them before trusting the doc
if the engine has moved on. Three entries turned out to be wrong on re-measuring
and one more was misdiagnosed, so treat an unverified claim here as a lead, not
a fact.

## Running one

Run from the omnidoc checkout, with an absolute path to the probe — the two
repos are not siblings, so a relative one depends on where you started:

```bash
cd "${OMNIDOC_DIR:-$HOME/code/omnidoc}"
go run ./cmd/omnidoc rasterize \
    ~/code/upnext/luckfox/docs/engine-probes/14-vertical-shrink-to-fit.html \
    -out /tmp/probe.png -page-size tall -dpi 96
```

`-page-size tall` renders one continuous page rather than paginating onto
Letter; `-dpi 96` makes 1 CSS px ≈ 1 device px. The output is still scaled
(1707px wide for a 1280px layout viewport, so ÷1.334 to get CSS pixels back).

## Reading the result

**Do not eyeball it.** Two of these gaps were initially mis-stated from a
glance at the image, and one "passing" check was a sampling artifact — a
horizontal scan across a vertical gradient, which found one colour and looked
flat. Sample specific pixels and compare against the case that is supposed to
work:

```python
from PIL import Image
im = Image.open("/tmp/probe.png").convert("RGB")
sc = im.size[0] / 1280          # raster px per CSS px
print(im.getpixel((int(20 * sc), int(40 * sc))))
```

For "did this paint at all" use a colour-specific test rather than a coverage
count: the panel background is near-black, so an unstyled element and a missing
one look identical (that is gap 2's whole story).

## Measure the box, not the page

The most important rule here, and the one that cost the most. Sampling page
pixels answers "does the page look right", which is a *different question* from
"is this box the size CSS says it should be". Two findings in this repo were
wrong because of that gap:

- Gap 8 was reported as "the element and everything after it stop painting".
  Every element painted. The flex container was 40px short and a parent block
  grew around the overflow, so the page looked plausible.
- Gap 12 reported a badge stretching to fill its card. Measuring the badge's own
  painted span showed it hugging its text all along — the page-wide scan was
  picking up neighbouring elements.

So: give each element under test a **unique background colour** and measure that
colour's span. Include a control in the same document — the same markup in a
context that works — and if the two measure the same, there is no bug.

## Caveat

A browser will render every one of these correctly — they are all standard CSS.
Opening them in Chrome proves nothing except that the probe is well-formed.
