# TODO

What is left before the display is *finished* rather than merely working.

The board boots, associates, fetches live weather, renders every minute, serves
the portal, and falls back to an AP when it cannot join a network — all verified
on hardware. What remains is mostly **appearance**: the panel currently renders
black-on-white instead of the intended dark theme, and one whole feature has
never been exercised with real data.

Line numbers are as of `956e6ef`. Engine findings re-tested 2026-08-27 against
doctaculous `957c9e1`.

---

## 1. The panel renders black-on-white — `var()` is unimplemented

**This is the single biggest gap between what is on screen and what was
designed.** `internal/view/assets/style.css` uses `var(--…)` in **18** places;
that is the entire palette — background, text, dim text, hairlines, accent.
`internal/portal/assets/portal.css` uses it in **20**.

The failure is not a fallback to a default color. A `background` set through
`var()` paints **zero** pixels where a literal color paints the full box: the
element vanishes. That is why the panel is white with black text rather than
`#0b0d12` with `#e8ecf3`.

Fix belongs in doctaculous (`docs/doctaculous-gaps.md` §1), per the standing
project rule: do not work around engine gaps here. The stylesheet is already
correct CSS and will render properly once `var()` lands.

Until then the panel is legible but wrong — worth knowing before showing it to
anyone as finished.

## 2. Precipitation shading is invisible — alpha colors unimplemented

`internal/view/assets/style.css:75` — `.wx-precip { fill: rgba(79,156,255,0.35); }`
is the only alpha color in either stylesheet, and it paints nothing (same
vanish-entirely mode as `var()`). The precipitation band on the hourly chart is
simply absent.

doctaculous §2. One declaration, so this resolves the moment alpha lands.

## 3. Real calendar events have never rendered

`config.sample.json:23` still ships `"url": "PASTE_ICAL_URL"`, and the board
still holds that placeholder. **Step 4 of the hardware verification was never
run.**

This is the largest correctness unknown left. The timeline's block placement and
floating-label collision handling have only ever seen synthetic fixtures — and
label overlap is exactly the class of bug that only appears with real,
irregularly-spaced events. The agenda is the reason the display exists.

To close it, paste a real feed into the portal, then:

```bash
adb shell 'cat /dev/fb0' > /tmp/fb.raw
go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate
```

Look for overlapping labels, events misplaced relative to the NOW line, and
anything clipped at the document edge. Note the engine does not break long words
(doctaculous §8), so a long event title will overflow rather than wrap.

## 4. Letter-spacing is silently ignored

Used **6** times in the dashboard stylesheet and **3** in the portal's — every
uppercase eyebrow label, including the panel's `SET ME UP`. The declaration
parses and then does nothing: glyph positions are byte-identical with and
without it.

doctaculous §6. Cosmetic, but it means those labels are tighter than designed.

---

## Portal behaviour

### Calendar fetch errors never reach the portal

`cmd/dashboard/main.go:356` collects `ical(<name>): <err>` into `errs`, which
drives the panel's stale flag. The portal page does not read it. A user who
pastes a wrong or private feed URL sees a normal-looking settings page with no
indication anything failed.

The portal is where the URL was entered, so it is where the error belongs. The
design doc already specifies the copy: "That calendar link didn't load. Check
the address, or the calendar may be private."

Note this compounds item 3 — the first thing a user does is paste a feed, and
that is precisely the operation with no error reporting.

### Portal's MAC is read once at startup

`cmd/dashboard/main.go:105` sets `MAC: wc.MAC()` at process start. If `wlan0`
has not enumerated, that returns `""`, `DefaultPassword("")` returns `""`, and
**every** password fails for the life of the process.

`S99zdashboard` sorts after `S99wlan0`, so this is narrow — but `S99wlan0` gives
up after ~10s (`board/etc/init.d/S99wlan0:22-29`) and the USB radio is not
guaranteed inside that window. The panel recovers on its own because
`renderOnce` re-reads the MAC every tick; the portal does not. The two then
disagree: the panel displays a password the portal will not accept.

Fix: derive the MAC lazily in `auth`, or re-read when empty.

### Setup hint hierarchy

`internal/view/assets/style.css:40` — `.sh-lead` ("SET ME UP") is 15px dim
uppercase, the smallest text in the left block, above two 22px steps.

On an unconfigured panel this is the only actionable thing on a 1920×480 screen
read from across a room, and it reads as a footnote. Transcribed verbatim from
the plan, so not an implementation defect — a product call about whether the
plan's hierarchy is right. One CSS value.

---

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

### Shell SSID derivation lacks Go's short-MAC guard

`board/etc/init.d/S99wlan0:84` uses `tail -c 5` unguarded; `wifi.APName`
(`internal/wifi/ap.go:14-25`) returns `upnext-setup` when the MAC is under 4
chars. A truncated sysfs read would make the panel name a network that is not
being broadcast, and panel-vs-radio disagreement has no recovery path for the
user. `[ ${#mac} -ge 4 ]` closes it.

The hostapd and dnsmasq config bodies are otherwise **byte-identical** between
the shell script and `ap.go` — verified. Nothing fails a test if they drift, so
change both together.

---

## Cosmetic

- **`os.IsNotExist` vs `errors.Is`** — `internal/config/config.go:179`. Correct
  for the unwrapped `os.ReadFile` error it inspects; `errors.Is(err,
  fs.ErrNotExist)` is the modern idiom.
- **Constant-time compare on raw strings** — `internal/portal/auth.go:61`. The
  MAC-default branch compares raw strings, leaking whether a guess is 6
  characters; the hash branch compares digests and does not. Not exploitable
  under the stated threat model (a houseguest on the LAN), already commented at
  `auth.go:58`.

---

## Recently fixed upstream — nothing to do

Re-tested against doctaculous `957c9e1`:

- **Inline `<svg>` now renders.** This was the top-priority gap: every weather
  icon was invisible. Icons now appear on the panel.
- **`border-radius` now renders.**

`docs/doctaculous-gaps.md` carries the full re-test table. That file goes stale
as the engine advances — re-run the probes before trusting any entry in it.
