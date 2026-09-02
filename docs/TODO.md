# TODO

What is left before the display is *finished* rather than merely working.

The board boots, associates, fetches live weather, renders every minute, serves
the portal, falls back to an AP when it cannot join a network, and now renders
the ported pi-dashboard design with real fonts and real calendar data — all
verified on hardware.

The visual-fidelity port is **essentially complete**: 27 of the 28 catalogued
differences are closed and verified on the panel. What is left is one item that
cannot be compared against the reference, a handful of changes awaiting a
hardware pass, and the non-visual work below.

Reference: the pi-dashboard served at `http://127.0.0.1:8777/` (its `style.css`
and `app.js` are the authority — a screenshot is not). Engine constraints are in
`docs/omnidoc-gaps.md`; re-run its probes before trusting any entry.

---

## 1. Visual fidelity vs the reference

Compared 2026-08-28 against a device capture with the sample feed populated.
**27 of 28 items are closed** — see the summary under Done. What is left:

- [ ] **All-day pill placement is unverified against the design.** The reference
      frame has no all-day event, so there is nothing to compare against. Check
      the reference app with one present before calling this finished. A
      coverage gap, not a defect: the pill itself renders correctly.

### Not yet seen on hardware

Written and passing host-side checks, but not eyeballed on the panel — the board
went unreachable mid-deploy (see the deploy gotcha in CLAUDE.md) and needs a
power cycle before the next verification pass:

- [ ] `ALL DAY` caption brightness — raised to `--text-mid` after the caption
      read as too faint to identify what the pill was.
- [ ] Framebuffer console caret — `S99zdashboard` now unbinds `fbcon` on start
      and rebinds on stop. The unbind itself was confirmed by probe (caret 0px,
      render intact); the init-script wiring has not survived a clean boot.
- [ ] `#now-content` centring — it was silently not centring because the box
      took its height from `flex: 1` (gap 11); now an explicit 345px. The panel
      had ~120px dead at the bottom.

The clock-row fix (the ~30px hole under the time, 98px → 21px) **was** measured
on the panel; it is only unconfirmed in combination with the three above.

---

## 2. Swap the board back to a stripped binary — **on or after 2026-09-08**

The deployed `/root/dashboard` is currently built **unstripped** (24MB rather
than 17MB) so that a repeat of the 2026-09-01 crash produces a stack naming the
code that faulted. Installed 2026-09-01 for one week.

```bash
scripts/build.sh dashboard          # stripped is the default
scripts/deploy.sh build/dashboard   # named file: does not touch S99wlan0
adb shell /etc/init.d/S99zdashboard restart
```

Revert unless the crash has recurred and is still unexplained; if it has, keep
the symbols and diagnose rather than swapping back on schedule.

**This costs disk, not speed.** `-s -w` drops `.symtab` and DWARF, which load at
`addr=0` and are never mapped: `.text` is byte-identical between the two builds.
Measured on the board, interleaved to cancel drift — 6.48/6.61/6.53s stripped
against 6.52/6.48/6.37s unstripped, one distribution. The reason to revert is
rootfs headroom: 30.8MB free (84% used) unstripped vs 44.9MB stripped, and a
full rootfs breaks more than a missing stack does.

`LUCKFOX_UNSTRIPPED=1 scripts/build.sh dashboard` rebuilds with symbols if this
is needed again.

### Why it was needed

The 2026-09-01 fault printed `runtime: traceback stuck` and named no
application frame — the unwinder had no symbol data to walk. What survived was
only `sigpanic` at `runtime/slice.go:432`, a segfault in slice growth, with the
caller unknown. It has not recurred in 30+ runs. Suspect the new allocation
paths in omnidoc `511b16c` (`perf/shadow-blur-alloc`,
`perf/gradient-shading-alloc`); a `-race` run on the host against those two is
the cheaper first move.

Note `.gopclntab` survives `-w`, so ordinary panics symbolize fine either way.
It is specifically hard faults the runtime cannot unwind that need `.symtab`.

---

## 3. Startup race: first frame after a restart shows a fetch error

The calendar fetch runs before the portal's listener is up, so the first render
after `S99zdashboard restart` shows
`ical(Personal): ... 127.0.0.1:8080: connect: connection refused` and an empty
agenda. It self-corrects on the next tick.

Only visible with the built-in sample feed (`/sample.ical` on the portal port),
since that is the only source served by the same process. A remote feed is
unaffected.

- [ ] Start the portal listener before the first fetch, or retry the first fetch
      once after a short delay.

**Touch makes this more visible than it was.** The empty agenda is not just a
cosmetic first frame any more: with no cards there is nothing to tap, so the
panel is genuinely non-interactive until the next calendar refresh — up to ten
minutes after a restart. That is long enough for someone to conclude the
touchscreen does not work. This moved up the list because of it.

---

## Portal behaviour

### Calendar fetch errors never reach the portal

`cmd/dashboard/main.go` collects `ical(<name>): <err>` into `errs`, which drives
the panel's stale flag. The portal page does not read it. A user who pastes a
wrong or private feed URL sees a normal-looking settings page with no indication
anything failed.

The portal is where the URL was entered, so it is where the error belongs. The
design doc already specifies the copy: "That calendar link didn't load. Check
the address, or the calendar may be private."

- [ ] Surface calendar fetch errors on the portal settings page.

### Portal's MAC is read once at startup

`cmd/dashboard/main.go:105` sets `MAC: wc.MAC()` at process start. If `wlan0`
has not enumerated, that returns `""`, `DefaultPassword("")` returns `""`, and
**every** password fails for the life of the process.

`S99zdashboard` sorts after `S99wlan0`, so this is narrow — but `S99wlan0` gives
up after ~10s (`board/etc/init.d/S99wlan0:22-29`) and the USB radio is not
guaranteed inside that window. The panel recovers on its own because
`renderOnce` re-reads the MAC every tick; the portal does not. The two then
disagree: the panel displays a password the portal will not accept.

- [ ] Derive the MAC lazily in `auth`, or re-read when empty.

### Setup hint hierarchy

`.sh-lead` ("SET ME UP") is the smallest text in the left block, above two 22px
steps. On an unconfigured panel this is the only actionable thing on a 1920×480
screen read from across a room, and it reads as a footnote. Transcribed verbatim
from the plan, so not an implementation defect — a product call.

- [ ] Decide whether the setup hint should lead the left panel.

---

## Known gaps

### The NOW headline names one of several simultaneous events

`findCurrentAndNext` (`internal/model/nowblock.go:149-169`) takes the first
in-progress event as `current` and ignores the rest. The card row stacks
overlapping events into one slot, so during a live conflict the panel shows two
or three meetings side by side while the headline to their left names only one
of them.

Deliberately out of scope when conflict stacking was built: the row is where the
conflict is expressed, and the headline is a single-line summary with no obvious
place to put a second title. Worth revisiting if a live conflict turns out to
read as the headline being wrong rather than being brief.

- [ ] Decide whether the headline should signal a conflict, e.g. "+1 more".

## Latent — no action unless the trigger happens

### Go/shell AP recursion, if `StopAP` ever gains a caller

`internal/wifi/ap.go`'s `restoreSTA()` ends with `/etc/init.d/S99wlan0 restart`,
and the shell's `start()` calls `start_ap()` when there is no lease. So
`StopAP()` → `restart` → `start()` can re-raise the AP that `StopAP` just tore
down, returning success having stopped nothing.

**Not reachable today:** `StartAP`/`StopAP` have no non-test callers; the shell
script owns AP transitions end to end. Resolve before wiring either to a caller.

The obvious fix — swapping the trailing `restart` for `stop` — is wrong. `stop`
downs the link entirely, contradicting `StopAP`'s contract of returning the
board to STA mode.

- [ ] Resolve before `StartAP`/`StopAP` gain a caller.

### Shell SSID derivation lacks Go's short-MAC guard

`board/etc/init.d/S99wlan0:84` uses `tail -c 5` unguarded; `wifi.APName`
(`internal/wifi/ap.go:14-25`) returns `upnext-setup` when the MAC is under 4
chars. A truncated sysfs read would make the panel name a network that is not
being broadcast, and panel-vs-radio disagreement has no recovery path for the
user.

The hostapd and dnsmasq config bodies are otherwise **byte-identical** between
the shell script and `ap.go` — verified. Nothing fails a test if they drift, so
change both together.

- [ ] Add `[ ${#mac} -ge 4 ]` to the shell path.

---

## Cosmetic

- [ ] **`os.IsNotExist` vs `errors.Is`** — `internal/config/config.go:179`.
      Correct for the unwrapped `os.ReadFile` error it inspects; `errors.Is(err,
      fs.ErrNotExist)` is the modern idiom.
- [ ] **Constant-time compare on raw strings** — `internal/portal/auth.go:61`.
      The MAC-default branch compares raw strings, leaking whether a guess is 6
      characters; the hash branch compares digests and does not. Not exploitable
      under the stated threat model (a houseguest on the LAN), already commented
      at `auth.go:58`.

---

## Done

- [x] **Engine gaps** — all ten resolved upstream (omnidoc `fb42ebe`) and
      adopted: `var()`, alpha colours, `linear-gradient`, `border-radius`,
      `box-shadow`, `letter-spacing`, `overflow-wrap`, context cancellation,
      `.notdef` fallback, inline `<svg>`.
- [x] **Real calendar events render on the panel** — closed with a date-shifted
      feed, then with the built-in `/sample.ical` generator. Block placement, the
      NOW line, and label handling all exercised against real parsed events.
- [x] **Sample data endpoint** — `GET /sample.ical` on the portal port generates
      a clock-relative fixture, so the panel always has events for design work.
      Board config points at `http://127.0.0.1:8080/sample.ical`.
- [x] **Real fonts** — Roboto, Barlow Condensed, IBM Plex Mono embedded and
      served via `@font-face`; OS installation cannot work (see the gaps doc).
- [x] **Card-based agenda** replacing the proportional timeline. The NOW bar
      sweeps the entry containing "now" and the row shifts a whole entry at a
      time, keeping one entry behind it for context; the earlier fixed 30%
      pre-scroll needed a 462px lead pad that showed as dead space on a quiet
      morning.

### Visual fidelity — 27 of 28 items, all verified on the panel

Closed 2026-08-28 by region: the left panel (icon, clipping, divider, spacing),
the NOW bar (glow, vertical label, full height, crossing the current card), the
agenda cards (centring, borders, badge, truncation, gaps, chip height), the
all-day pill, the weather chart (padding, label size and count, curve weight,
time window), the forecast row (icon size, spacing, precipitation format), and
the overall tone (vignette, agenda background).

**Almost none of these were styling mistakes.** The CSS was largely faithful to
the reference and was failing silently against the engine. Nine gaps came out of
this work — SVG stroke inheritance, `line-height`, `margin`/`transform` on flex
children, `z-index` ordering, `top`+`bottom` sizing, flex-derived heights
blocking `justify-content`, absolute boxes never shrink-wrapping, layered
`background` lists, and `writing-mode`. Read `docs/omnidoc-gaps.md` before
assuming a rule in `style.css` does what it says.

All but one of those are now **fixed upstream** and their workarounds removed;
the shrink-wrap entry turned out not to reproduce at all. What remains is a
vertical shrink-to-fit box being sized on the horizontal axis, with a probe
under `docs/engine-probes/`. The probes for the closed gaps went with them.

Three entries in the original catalogue were **misdiagnoses**, corrected rather
than "fixed": the all-day pill was clipped at the bottom (not the top), the
forecast already showed precipitation the way the reference does, and the
current card never carried a duration. Two more — the warm vignette and the
inches-vs-percent note — came from reading the reference's *screenshot* rather
than its stylesheet, which is why the header above insists the CSS is the
authority.

Guardrails added, since most of these failed invisibly:
`TestNowBarLandsOnTheCurrentCard` walks the template's flex packing so the
model's arithmetic and the rendered layout cannot drift apart again;
`TestStylesheetMatchesModelGeometry` pins the shared constants (both were
verified to fail on a deliberate regression, not just to pass);
`TestIconStrokesAreNotInheritedFromRoot` catches the SVG gap that no screen
would show.

**Touch landed 2026-08-31.** Tapping an event card opens a detail sheet with a
Hide-from-panel action; hidden events are listed on the settings page and can be
restored there. `internal/touch` decodes the Goodix digitizer, `CardRects` /
`EventAt` hit-test, and `cmd/dashboard/touch.go` holds the interaction rules.

Three defects came out of building it, all caught by tests rather than by
inspection, and all worth knowing about:

- **Card rects extended under the left panel.** The row is pre-scrolled with a
  negative offset, so a past card's rect ran to negative x. On screen
  `#timeline-zone`'s `overflow:hidden` clips it, but a rect does not know that —
  a tap on the clock opened a sheet for an invisible event. `CardRects` now
  clips to the viewport.
- **The first tap deadlocked the process.** The dialog pointer and the
  framebuffer shared one `sync.Mutex`, and the touch callback took it twice.
  The panel froze on whatever frame it had, with the tick loop stopped too.
  They have separate locks now.
- **Pruning ran after muting**, so muting an event that had already ended
  deleted the mute that had just been written — the card came straight back.
  Prune now runs first, and mutes outlive their event by `config.MuteGrace`.

Two engine gaps also came out of it (15 and 16 in `docs/omnidoc-gaps.md`), both
of the same kind: the box paints and its content does not.

- [ ] **A render takes ~10s on the board, so the dialog is unusably slow.**
      Confirmed by use: the sheet takes about ten seconds to appear and the
      same to dismiss, which reads as "close doesn't work" long before the
      frame lands. Measured 10.1–10.4s per `--once` on hardware against 172ms
      for the same document on an M4 Pro, so it is the rasterizer on a 1.2GHz
      Cortex-A7 — not the fetches, and not the dialog. Every frame has always
      cost this; only interactivity made it matter.

      Options, roughly in order of payoff: render the dialog as a small
      composited overlay instead of re-rendering the whole 1920x480 document;
      cache the last full frame and blit the sheet over it; or cut what the
      page costs to rasterize. Worth measuring which part of the render
      dominates before choosing.

- [x] **A real finger has touched the panel.** Confirmed working on hardware
      2026-08-31: tapping a card opens the sheet with the right event. Two
      defects came out of that first real use, both fixed —

      - **Close appeared not to work.** The tap channel was unbuffered and the
        consumer sits inside a ~10s render, so the decode loop blocked on the
        send, stopped reading the device, and the close tap queued in the
        kernel behind the tap that opened the dialog. The reader now drops the
        oldest queued tap rather than blocking, so a late consumer gets the
        user's most recent touch.
      - **Coordinate state leaked between taps.** `haveX`/`haveY` were never
        reset, and the digitizer only reports an axis when it changes, so a
        frame carrying no coordinates decoded as a tap at the previous
        position.

Note for whoever tests this next: taps **cannot be injected on this board** —
there is no `/dev/uinput`, and writing to `/dev/input/event0` returns success
while the kernel discards it (see CLAUDE.md). Touch changes have to be tried
with a finger; both defects above were found that way and neither was visible
to any host-side test.

**A tenth defect was ours, not the engine's** (found 2026-08-31, on hardware).
`#timeline-zone` had no `overflow: hidden`, and `#agenda-row` is both wider than
that zone and pre-scrolled with `transform: translateX(-Npx)`. As soon as the
agenda scrolls, the row's background paints out past the zone's left edge and
washes over the entire left panel — clock, weather, and headline all vanish
behind a dim blue rectangle. A browser does exactly the same; nothing was
clipping it.

It hid for the same reason several engine gaps did: **the golden fixture's
agenda sits at offset 0**, where there is nothing to overflow. Every host-side
check passed while the panel was visibly wrong. It was caught by pulling the
board's own generated HTML and re-rendering it on the host, which reproduced it
exactly — that technique is worth reaching for before suspecting the engine.

`TestAgendaScrollDoesNotPaintOverLeftZone` builds a model whose agenda really is
scrolled, rasterizes, and asserts no agenda ink lands left of the 380px
boundary. Verified to fail with the fix reverted.
