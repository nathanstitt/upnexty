# TODO

Deferred findings from the captive-portal work (merged 2026-08-27, `8084d73`).
Each was reviewed, judged non-blocking, and consciously left. None is a known
crash or data-loss bug.

Line numbers are as of the merge commit.

## Worth doing

### Portal never shows calendar fetch errors

`cmd/dashboard/main.go:356` collects `ical(<name>): <err>` into `errs`, which
drives the panel's stale flag. The portal page does not read it. A user who
pastes a wrong or private feed URL sees a normal-looking settings page and no
indication anything failed — while the panel quietly shows the stale mark.

The portal is where the URL was entered, so it is where the error belongs. The
design doc already specifies the copy: "That calendar link didn't load. Check
the address, or the calendar may be private."

### Portal's MAC is read once at startup

`cmd/dashboard/main.go:105` sets `MAC: wc.MAC()` when the process starts. If
`wlan0` has not enumerated yet, that returns `""`, `DefaultPassword("")` returns
`""`, and **every** password fails for the life of the process.

`S99zdashboard` sorts after `S99wlan0`, so this is narrow — but `S99wlan0` gives
up after ~10s (`board/etc/init.d/S99wlan0:22-29`) and the USB radio is not
guaranteed inside that window. The panel recovers on its own because
`renderOnce` re-reads the MAC every tick; the portal does not. The two then
disagree: the panel displays a password the portal will not accept.

Fix: derive the MAC lazily in `auth`, or re-read when it is empty.

### Setup hint hierarchy

`internal/view/assets/style.css:40` — `.sh-lead` ("SET ME UP") is 15px dim
uppercase, the smallest text in the left block, sitting above two 22px steps.

On an unconfigured panel this is the only actionable thing on a 1920×480 screen
read from across a room, and it currently reads as a footnote. Transcribed
verbatim from the plan, so not an implementation defect — a product call about
whether the plan's hierarchy is right. One CSS value.

## Latent — no action unless the trigger happens

### Go/shell AP recursion, if `StopAP` ever gains a caller

`internal/wifi/ap.go`'s `restoreSTA()` ends with `/etc/init.d/S99wlan0 restart`,
and the shell's `start()` calls `start_ap()` when there is no lease. So
`StopAP()` → `restart` → `start()` can re-raise the AP that `StopAP` just tore
down, and the function returns success having stopped nothing.

**Not reachable today:** `StartAP`/`StopAP` have no non-test callers. The shell
script owns AP transitions end to end. Resolve this before wiring either to a
caller.

Note: the obvious fix — swapping the trailing `restart` for `stop` — is wrong.
`stop` downs the link entirely, contradicting `StopAP`'s contract of returning
the board to STA mode.

### Shell SSID derivation lacks Go's short-MAC guard

`board/etc/init.d/S99wlan0:84` uses `tail -c 5` unguarded; `wifi.APName`
(`internal/wifi/ap.go:14-25`) returns `upnext-setup` when the MAC is under 4
chars. A truncated sysfs read would make the panel name a network that is not
being broadcast.

Vanishingly unlikely against real sysfs, but it is the one place the two
derivations can disagree, and panel-vs-radio disagreement has no recovery path
for the user. `[ ${#mac} -ge 4 ]` closes it.

The hostapd and dnsmasq config bodies are otherwise **byte-identical** between
the shell script and `ap.go` — verified. Nothing fails a test if they drift, so
change both together.

## Cosmetic

### `os.IsNotExist` vs `errors.Is`

`internal/config/config.go:179`. Correct for the unwrapped `os.ReadFile` error it
inspects; `errors.Is(err, fs.ErrNotExist)` is the modern idiom.

### Constant-time compare on raw strings

`internal/portal/auth.go:61`. The MAC-default branch compares raw strings, so it
leaks whether a guess is 6 characters; the hash branch compares digests and does
not. Not exploitable under the stated threat model (a houseguest on the LAN) and
already commented in place at `auth.go:58`.

---

## Not a code issue, but unfinished

**Real calendar events have never rendered on hardware.** The board still holds
the `PASTE_ICAL_URL` placeholder, so Step 4 of the plan's hardware verification
was never run. Everything else in that plan was verified end to end, including
AP mode and a reboot.

This is the largest remaining unknown: the timeline's block placement and
floating-label collision handling have only ever seen synthetic fixtures. Paste
a real feed into the portal, then:

```bash
adb shell 'cat /dev/fb0' > /tmp/fb.raw
go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate
```

Look for overlapping labels, events misplaced relative to the NOW line, and
anything clipped at the document edge.
