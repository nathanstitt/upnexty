# Rasterization performance — what the dashboard needs from omnidoc

Measured 2026-08-31 on the Luckfox Lyra Zero W (RK3506B, 3× Cortex-A7 @ 1.2GHz,
armv7l) against an M4 Pro host, using `tools/renderprof` in this repo. The
dashboard document is 1920×480, ~42KB of HTML with inlined CSS, 10 `@font-face`
rules, and an inline SVG weather chart.

This is a request-for-work writeup for the omnidoc side. Nothing here is a bug:
the engine is correct, it is just slow on this CPU, and the dashboard has become
interactive so per-frame cost now shows up as input latency.

## The measurement

Per frame, no network (the store is pre-populated), median of 3:

| Stage | Board | Host | Share of frame |
|---|---|---|---|
| `model.Build` (ours) | 1ms | 0ms | 0.0% |
| template execution (ours) | 10–22ms | 0ms | 0.2% |
| `OpenHTMLBytes` total | ~880ms | 14ms | 9.7% |
| — `html.Parse` (tokenize+tree) | 14–21ms | 0ms | 0.2% |
| — `BuildWithFontsPagesRunningMedia` | 62–72ms | 1ms | 0.8% |
| — `FaceCache`+`Engine` ctor | ~0ms | 0ms | 0.0% |
| — `LayoutPagedDoc` | 748–794ms | 14ms | 8.7% |
| **`RasterizePage`** | **~8.0s** | **~180ms** | **88%** |
| `fb.Pack` (ours, rotate+XR24) | ~197ms | 3ms | 2.2% |
| **Total** | **~9.0s** | **~180ms** | |

A full `--once` on the board is ~10.3s; the extra ~1.3s over the 9.0s here is
the calendar/weather fetches and a `wpa_cli` subprocess, which `renderprof`
excludes deliberately.

> **Update 2026-09-01, omnidoc `dbab5c4`.** `RasterizePage` now measures ~7.6s
> (median of 9, rasterize-only against a frame captured from the board) and a
> full `--once` ~9.0–9.5s. About 230ms of that is the glyph outline cache; the
> rest is not attributable from these runs and some is likely fetch variance
> between sessions. **The 88% conclusion is unchanged** — rasterization still
> dominates, and every ask below still stands. See "Performance findings" in
> `omnidoc-gaps.md` for the cache A/B and the method it took to get a signal.

Three conclusions that shaped the asks below:

1. **Rasterization is 88% of the frame.** Nothing else is worth attacking; the
   model, template, parse, and CSS cascade together are under 1.3%.
2. **Parse/DOM caching is not the lever.** `html.Parse` is 0.2% of a frame.
   Exposing or reusing a parsed DOM would save ~20ms of ~9,000ms. Even caching
   the entire open phase (parse + CSS + fonts + layout) caps out at ~9.7%.
3. **There is no cold-start penalty.** Frame 1 and frame 3 differ by under 2%,
   so fonts are already effectively cached across documents within a process.
   Font loading is ~70ms inside `BuildWithFonts`, not a hidden cost.

The board/host ratio is ~45× for paint and ~55× for layout — roughly uniform.
This is not one feature falling off a fast path; it is general painting work on
a slow in-order core. So we do not expect a single hot CSS feature to remove.

## Ask 1 — rasterize a sub-region (the one that unblocks touch)

**Priority: high.** This is what makes the panel interactive.

Tapping an event card opens a detail sheet. Today that re-renders and re-paints
the whole 1920×480 document, so the sheet takes ~10s to appear and ~10s to
dismiss — which reads as "close is broken" long before the frame lands.

The sheet is a small box (roughly 400×300, ~4% of the page's pixels). If omnidoc
could paint just that region, the dashboard would composite it over a cached
copy of the last full frame and write the result. Expected tap latency ~300ms
rather than ~10s.

Either of these shapes would work for us:

- `RasterizePageRegion(ctx, page, rect, opts)` — paint a clipped sub-rect of the
  laid-out page, returning an image of that rect. Preferred: it reuses the
  existing layout and needs no second document.
- Rasterizing a small standalone document (e.g. a 400×300 dialog-only page) fast
  enough that we composite two independently-rendered images. Workable, but it
  means the sheet cannot participate in the main document's cascade, and we would
  have to duplicate its styling context.

Constraint worth stating: we need the region's output to be pixel-identical to
the corresponding crop of a full-page render, or the composite will seam
visibly against the cached frame.

If neither is feasible, our fallback is drawing the sheet with Go's `image/draw`
and hand-rolled text — giving up CSS for the dialog entirely. We would rather
not; that is a second rendering path to maintain and to keep visually in step.

## Ask 2 — parallel rasterization (the one that fixes the baseline)

**Priority: medium.** Independent of Ask 1, and complementary to it.

The board has **3× Cortex-A7 cores** and the Go runtime already sees all of them
(`GOMAXPROCS` defaults to the core count; `CGO_ENABLED=0` does not change that).
`RasterizePage` appears to be single-threaded, so two of the three cores are idle
for the 8s that dominates every frame.

Horizontal bands are the obvious decomposition: split the page into N scanline
strips, paint each into its own sub-image concurrently, then composite. Painting
should be close to a pure function of the laid-out tree.

Expected caveats, which is why this is an omnidoc-side change and not something
we can do from here:

- The glyph/face cache is shared mutable state and would need to be safe for
  concurrent readers (or warmed before the fan-out).
- Anything painted in z-order across a band boundary has to compose correctly —
  the dashboard uses `z-index`, layered `background` lists, and `box-shadow`.
- Band count should be tunable; 3 cores means the useful ceiling is ~3×, and in
  practice we would expect 2–2.5× after overhead.

That would take the frame from ~9s to roughly ~4s. On its own it does **not**
fix touch — a 4s dialog still reads as broken — but it improves every frame
including the once-a-minute tick, and it requires no changes in this repo.

## What we are not asking for

- **DOM/parse caching.** Measured at 0.2% of a frame. Not worth the API surface.
- **Cutting expensive CSS features.** Cost is spread across the page rather than
  concentrated, so trimming shadows or gradients looks like a 10–20% win —
  10s to 8s, still unusable for touch. We would rather keep the design intact.
  The glyph outline cache is the worked example: a change profiled at ~18% of a
  text-dense showcase returned 2–3% here, because this document's cost is not
  where that document's cost was.
- **Skipping unchanged frames.** The clock advances every minute by
  construction, so consecutive frames always differ.

## Reproducing

`tools/renderprof` in this repo times each stage and, with `-deep`, breaks
`OpenHTMLBytes` into parse / CSS+boxes / layout. It replicates the internals of
`pkg/omnidoc/html_backend.go`'s `htmlDocument` because those seams are not
individually exported — if that pipeline changes, `deepProfile` needs the same
change or it will misattribute cost.

```bash
go run ./tools/renderprof -config config.sample.json -n 3 -deep   # host
scripts/build.sh ./tools/renderprof && adb push build/renderprof /root/
adb shell '/root/renderprof -config /root/config.json -n 3 -deep' # board
```

Note: running `renderprof` alongside the live dashboard service wedged the board
(both hold a full-page image plus a 3.7MB pack buffer on 512MB RAM). Stop
`S99zdashboard` before profiling on hardware, or expect to power cycle.
