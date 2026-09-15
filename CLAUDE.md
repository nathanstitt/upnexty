# Luckfox Lyra Zero W

Host-side tooling for a Luckfox Lyra Zero W (Rockchip RK3506B) driving a
Waveshare 8.8" DSI touch panel. Build Go on the Mac, run it on the board.

## Hardware

| | |
|---|---|
| SoC | RK3506B — 3× Cortex-A7 @ 1.2GHz + Cortex-M0, **armv7l (32-bit)** |
| RAM | 512MB DDR3L (~430MB free) |
| Storage | **256MB SPI NAND** (W25N02KV), ~90MB free on rootfs |
| OS | Buildroot 2024.02, Linux 6.1.99 |
| Panel | Waveshare 8.8" `8.8-DSI-TOUCH-A`, **480×1920 portrait**, OTA7290B |
| Touch | Goodix, I²C `2-005d`, `/dev/input/event0`, reports 0–479 × 0–1919 |
| Backlight | `/sys/class/backlight/waveshare_bl/brightness` (0–255) |

The Zero W has **no eMMC** — Luckfox's comparison table claims 8GB, but the
flash ID reads ASCII `SNAND` and the vendor spec says 256MB SPI NAND. Trust the
hardware.

There is **no serial console on the USB-C ports** (both are OTG/host). The
board is reachable over **adb** once Linux boots; a USB-TTL adapter on the UART
pins is only needed to debug U-Boot before Linux starts.

## Layout

```
scripts/     host-side helpers (see below)
build/       cross-compiled binaries (gitignored)
tools/       vendored upgrade_tool (gitignored — see Setup)
```

Firmware images are **not** in the repo (~130MB, vendor-supplied). Default
location `~/Downloads/Luckfox_Lyra_Flash_250717/`; override with
`LUCKFOX_IMAGE_DIR`.

**`html2fb` and `fbtouch` no longer exist.** They used to be built out of the
renderer's repo, and the doctaculous→omnidoc rename did not carry them over —
omnidoc ships only `cmd/omnidoc` and `cmd/dumpfixtures`, and no copy is on disk.
The dashboard never needed them: it rasterizes in-process through `internal/fb`
and writes `/dev/fb0` itself. `scripts/build.sh` builds `./cmd/dashboard`.

The sections below that invoke `/root/html2fb` and `/root/fbtouch` describe how
the panel and touch rotation work and are kept for that; the commands themselves
would have to be recovered from git history or rewritten to run again.

The renderer is the **omnidoc** repo; override its location with `OMNIDOC_DIR`
(`go.mod` has a `replace` pointing at `../../omnidoc`).

## Setup

Requires Go, `adb` (Android platform-tools), and `dtc` (`brew install dtc`)
for inspecting device trees.

`upgrade_tool` is not in the repo. Download `upgrade_tool_v2.44_for_mac.zip`
from the [Luckfox image-flashing wiki](https://wiki.luckfox.com/Luckfox-Lyra/Getting-Started/Image-flashing/)
and extract to `tools/`:

```bash
ditto -x -k ~/Downloads/upgrade_tool_v2.44_for_mac.zip tools/
chmod +x tools/upgrade_tool_v2.44_for_mac/upgrade_tool
```

Use `ditto`, not `unzip` — the archive contains a Chinese-named PDF that
macOS `unzip` fails on mid-extraction.

**v2.44's arm64 slice segfaults** in `pthread_mutex_init` at startup. The
scripts always invoke it via `arch -x86_64`; do the same if you call it
directly. v2.44 is the newest macOS build Rockchip ships.

## Daily loop

```bash
scripts/build.sh      # cross-compile to build/
scripts/deploy.sh     # adb push to /root, chmod +x
adb shell /root/html2fb -rotate 270 /root/page.html
```

Nothing else is needed for Go work — no Docker, no SDK, no network on the board.

Cross-compile settings (in `scripts/common.sh`): `GOOS=linux GOARCH=arm GOARM=7
CGO_ENABLED=0`, stripped with `-ldflags="-s -w"`. **`CGO_ENABLED=0` matters** —
a static binary has no libc dependency, so it doesn't care what Buildroot ships.

Build a package from elsewhere:

```bash
scripts/build.sh -C ~/code/otherproject ./cmd/thing
```

## Scripts

| Script | What it does |
|---|---|
| `board-info.sh` | Board state: kernel, RAM, display mode, backlight, inputs. Read-only, works in Maskrom/Loader too. |
| `build.sh` | Cross-compile Go for armv7 into `build/`. Verifies the ELF is really 32-bit ARM. |
| `deploy.sh` | `adb push` + `chmod +x` to `/root`. |
| `flash.sh` | Write full firmware to SPI NAND. **Erases the board.** |
| `set-dsi-panel.sh` | Set DSI panel timings in the boot partition. **Runs on the board.** |

## Flashing

Full reflash wipes bootloader, kernel, and rootfs:

```bash
adb shell reboot loader          # or hold BOOT while plugging in USB-C OTG
scripts/flash.sh                 # ~2 min for 127MB
```

`flash.sh` refuses to run unless the board is in Maskrom or Loader mode, and
checks the image is a real `RKFW` container first.

**After every reflash the panel reverts to 800×1280** and shows a garbled,
sheared image. Restore it:

```bash
scripts/deploy.sh
adb shell /root/set-dsi-panel.sh
adb shell reboot
```

### Recovery

A bad `boot` partition cannot brick the board — Maskrom lives in unwritable
mask ROM. Hold BOOT while connecting the OTG port and reflash.

## The DSI panel fix

**The board ships configured for an 800×1280 panel.** Feeding that to a
480×1920 panel produces a recognizable-but-sheared image with diagonal lines —
the row stride is wrong, so each line wraps.

Luckfox's device tree already contains timings for ~18 Waveshare panels
including `ws-8inch8`, and `luckfox-config` is *supposed* to select one. It
doesn't work: in `luckfox_load_cfg`, `/usr/bin/luckfox-config` reads `DSI_SIZE`
into the variable `dsi_type` — a copy/paste of the `DSI_TYPE` line above it — so
`$dsi_size` is always empty, the `[ -n "$dsi_size" ]` guard fails, and
`luckfox_dsi_app` never runs. Setting `DSI_TYPE`/`DSI_SIZE` in
`/etc/luckfox.cfg` therefore has no effect. Worth reporting upstream.

`set-dsi-panel.sh` performs the writes that function would have made:

```bash
adb shell /root/set-dsi-panel.sh --show     # what's in flash now
adb shell /root/set-dsi-panel.sh            # apply 8.8" (480×1920)
adb shell /root/set-dsi-panel.sh default    # revert to stock 800×1280
adb shell reboot                            # required — DSI is set at boot
```

It's idempotent (no write when flash already matches), reads offsets from the
FIT header rather than hardcoding them, and re-reads flash afterwards to verify.

It **must recompute the SHA-1 and size** in the resource entry — U-Boot checks
them. Patching `boot.img` on the host without doing so produces an image that
won't boot. The `boot` FIT is also RSA-signed with a `dev` key; the in-place
`fdtput` path sidesteps that entirely, which is why it's the approach used.

To add a panel, copy values from the matching `dsi_size` branch of
`luckfox_dsi_app` in `/usr/bin/luckfox-config`. Only `8_8` and `default` are
verified on this hardware; the `7_0` entry is transcribed but untested.

## Display and touch

`/dev/fb0` is **480×1920, XR24** (`XRGB8888` little-endian → bytes `B,G,R,X`).
Stride is 3200 = width×4, no row padding.

The panel is physically portrait. For a landscape UI, rotate:

```bash
/root/html2fb -rotate 270 page.html      # renders 1920×480 landscape
/root/fbtouch -rotate 270                # touch in matching coordinates
```

At 90/270 `html2fb` rasterizes the document to the *swapped* extent, so text is
rendered at landscape resolution rather than scaled up from portrait.

Touch reports in panel-native portrait, already aligned with the framebuffer —
no calibration needed. `-rotate` just undoes the display rotation. **Use the
same angle for both tools**; `fbtouch`'s mapping is the exact inverse of
`html2fb`'s blit, covered by round-trip tests in the omnidoc repo.

Writes to `/dev/fb0` are not vsynced. Fine for static pages; animation would
want double-buffering via `FBIOPAN_DISPLAY` or the DRM path (`card0` exists).

**The kernel console shares `/dev/fb0` and draws over us.** `fbcon` is bound to
the framebuffer, so its caret — a 2x8px grey block near the top-left, blinking
about once a second — paints on top of every frame. It is not in the rendered
HTML; rasterizing the same document on the host shows nothing there, which makes
it easy to hunt for a phantom CSS bug.

`echo 0 > /sys/class/graphics/fbcon/cursor_blink` only freezes the caret
visible. The console has to be unbound:

```bash
for vt in /sys/class/vtconsole/vtcon*; do
  grep -qi "frame buffer" "$vt/name" && echo 0 > "$vt/bind"
done
```

`S99zdashboard` does this on start and rebinds on stop — without the rebind a
stopped dashboard leaves a frozen frame and no way to see kernel messages on the
panel. Pick the vtcon whose `name` says "frame buffer"; the numbering is not
guaranteed.

Vendor display test, if you suspect the pipeline:

```bash
adb shell modetest -M rockchip -s 74@71:480x1920
```

## WiFi

**The stock `Luckfox_Lyra_Flash_*` image has no WiFi driver.** It is built for
the base Lyra, which has no radio. The Zero W's **AIC8800DC** (USB `a69c:88dc`)
is present and enumerated either way — its Bluetooth half binds `btusb` and
works — but USB interface 1.2, the WiFi function, is left unclaimed. The kernel
side is all there (`cfg80211`, `mac80211`, `S35wifibt-poweron.sh`,
`rfkill_wlan_init` at boot); only the chip driver is absent.

The fix is to take the driver from the **Zero W** image, which ships it. Both
images use the same kernel, so the modules load as-is:

```bash
scripts/setup-wifi.sh                  # extract from the Zero W image + install
adb shell '/usr/bin/wifi-connect.sh <SSID> <PASSWORD>'
# make it survive reboot -- wifi-connect.sh only writes /tmp, which is tmpfs:
adb shell 'sed "s/SSID/<SSID>/; s/PASSWORD/<PASSWORD>/" /etc/wpa_supplicant.conf > /tmp/w && cp /tmp/w /etc/wpa_supplicant.conf'
adb reboot
```

`setup-wifi.sh` needs `Luckfox_Lyra_Zero_W_Flash_<date>.zip` unzipped in
`~/Downloads` (override with `LUCKFOX_ZERO_W_DIR`). Get it from the wiki's
Google Drive under **Firmware → Buildroot** — note the `_W_` and `Flash`
(SPI NAND, not MicroSD; this board has no card slot). It installs:

| | |
|---|---|
| `aic8800_fdrv`, `aic_load_fw`, `aic_btusb` | into `/lib/modules/6.1.99/kernel/drivers/net/wireless/` |
| `modules.dep` entries | written by hand — **there is no `depmod`** on this image |
| `/lib/firmware/aic8800DC/` | 20 blobs; `/lib/firmware` does not otherwise exist |
| `wifi-connect.sh` + WPA2 template | ours ships an open-network stub |
| **CA certificates** | `/etc/ssl/certs/` is empty in *both* images |
| `board/etc/init.d/S99wlan0` | associate, DHCP, then set the clock |

`S03modules_init.sh` modprobes every `.ko` under `/lib/modules/$(uname -r)/kernel/`
at boot, so the driver auto-loads once it is installed there and listed in
`modules.dep`.

**The CA bundle is easy to miss.** Without it WiFi associates and pings fine,
but every HTTPS request fails with `x509: certificate signed by unknown
authority` — so the dashboard connects and still shows STALE.

**The clock** has no RTC and boots at 1970. There is no `ntpd`/`ntpdate`/
`chrony`, but `rdate` is present; `S99wlan0` uses it once an address exists.
The system stays on UTC — the dashboard converts via its configured timezone.

Verified from a cold boot: modules auto-load, `wlan0` associates, DHCP lease,
clock set, dashboard fetches live weather. ~2.0–2.5s per frame with the fetch.

**No network means the board becomes one.** If `S99wlan0` cannot associate — no
credentials, wrong password, router down — it raises an open AP named
`upnext-<4 hex of the MAC>` at `192.168.4.1` and serves the portal there.
`dnsmasq` answers every DNS query with that address, which is what makes a phone
offer its "sign in to network" sheet; there is no `iptables` on this image, so
that wildcard is the entire redirect.

The portal is on `:80` in both modes — the port a phone's captive-portal probe
actually hits. It answers the probe endpoints (`/hotspot-detect.html`,
`/generate_204`, `/connecttest.txt`, …) with the settings page itself, which is
what raises the "sign in to network" sheet; the DNS wildcard only points the
name at the board, it cannot answer a request. Serving the page in place rather
than redirecting is deliberate: a redirect to another port made the iOS sheet
render the destination's response as a bare error, which reads as a blank page.
Auth is a **session cookie**, not Basic auth: a captive-portal sheet does not
render a `WWW-Authenticate` challenge — it displays the 401 body instead, which
reads as a blank page with nothing to type into. `POST /login` sets the cookie;
the session is an HMAC over the stored password hash, so changing the password
invalidates every existing session for free, and the secret is minted per
process so sessions do not survive a restart. A POST that carries
`device_password` inline authenticates and performs the action in one step,
which is what makes submitting the settings form from a fresh sheet work.
The admin password defaults to the last
6 hex of the WiFi MAC and is shown on the panel whenever the board is **not
associated** — which covers a fresh board, but also a wrong password, a router
that went away, and a move out of range. Gating on saved credentials instead
would hide the hint in exactly the case a user needs it.

Verified end to end on hardware (2026-08-27), including the AP path:

| | |
|---|---|
| AP raised | `upnext-1bfd` at `192.168.4.1`, hostapd + dnsmasq up |
| Panel showed | `SET ME UP / Join Wi-Fi upnext-1bfd / Password 4c1bfd` |
| Settings changes | apply live — **same PID**, no restart, no dropped frame |
| After reboot | brightness and clock format persisted and re-applied |

The SSID on the panel is the SSID hostapd actually broadcasts: `S99wlan0` and
`internal/wifi`'s `APName` derive it identically from the MAC. If you change one,
change the other — nothing fails a test when they drift.

**`S99wlan0 restart` while in AP mode is safe.** `stop()` tears down hostapd,
dnsmasq, and the `192.168.4.1/24` address before downing the link, so `start()`
does not try to associate against an interface AP mode still owns. Getting this
wrong is how the board gets stranded.

## Dashboard

The `dashboard` service renders the UpNext display: it fetches iCal calendars
and Open-Meteo weather, generates HTML+SVG, rasterizes with omnidoc, and
writes `/dev/fb0`. It also serves the configuration portal on `:80`
as a goroutine in the same process — a settings save swaps the config pointer
under the `Store` mutex and the next tick picks it up, so there is no IPC, no
second binary, and nothing to restart.

```bash
scripts/build.sh dashboard
scripts/deploy.sh                                 # binaries + board/etc/init.d/
adb push config.sample.json /root/config.json     # then edit in the iCal URLs
adb shell /root/dashboard --once                  # single frame
adb shell /etc/init.d/S99zdashboard restart       # run it as a service
```

`scripts/deploy.sh` with no arguments also installs everything under
`board/etc/init.d/`; passing explicit files skips that, so an iteration loop
does not restart services. `S99zdashboard` sorts after `S99wlan0` (rcS runs
these sequentially, and wlan0 blocks up to ~45s on DHCP) so the first frame has
live data. It has `start|stop|restart|status` and truncates its log at boot —
`/root` is UBI with ~50MB free and this runs every minute forever.

**The bare `deploy.sh` reinstalls `S99wlan0` too.** That makes it a wlan0-touching
operation, subject to the stranding gotcha below — a bare deploy followed by a
service restart took out adb and WiFi together and needed a power cycle. When
only the dashboard changed, name the files (`deploy.sh build/dashboard`), and
deploy an init-script change on its own rather than alongside anything that
restarts networking.

**Deploying the binary does not update a running service.** `deploy.sh` replaces
the file; the already-running process keeps executing the old image. `--once`
picks up the new binary immediately, so a one-shot render can look correct while
the panel still shows the old frame. Restart the service, then verify against
`/dev/fb0` rather than a `--once` capture.

**A frame takes ~10.3s on the board** (measured 2026-08-31, three consecutive
`--once` runs: 10.1 / 10.3 / 10.4s, including the calendar and weather fetches).
The same document rasterizes in 172ms on an M4 Pro, so this is the renderer on a
1.2GHz Cortex-A7, not the network.

An earlier note here claimed **~1.05–1.18s per frame**. That figure is stale —
it predates the portal, the font embedding, and the current design, and it was
never re-measured. Do not plan against it.

Once a minute this is invisible. It stops being invisible the moment anything is
interactive: a tap re-renders, so the detail sheet takes ~10s to appear and ~10s
to dismiss. That is the dominant open problem with touch (see docs/TODO.md).

Those figures predate the portal. With it running in the same process, idle RSS
measured 33–38MB across two observations on 2026-08-27 (354MB free), so the
portal costs nothing meaningful at rest.

**A frame peaks far higher than idle RSS suggests.** Measured 2026-09-14:
`VmHWM` reached **91MB** during the first render after boot, against a 38–53MB
`VmRSS` between frames. The memory is returned afterwards — RSS falls back and
HWM stays flat across later frames — so this is a per-render spike, not a leak.
It is ~2.5× the idle figure above, which is the number to plan against on a
477MB board. Read `VmHWM`, not `VmRSS`: RSS between ticks says nothing about
what a render costs.

## Health logging

`/root/health.sh` (started by `board/etc/init.d/S98health`) samples uptime,
`MemAvailable`, the dashboard's RSS/HWM/threads/fds, its cumulative CPU, and
`wlan0` state every 30s to **`/root/health.log`**.

`/root` because it is UBI and persists; `/tmp`, `/var/log` and `/run` are tmpfs
and are erased by the reboot that follows a hang — which is exactly why the
2026-09-14 lockup (panel frozen at 12:17, no ping, no SSH, no adb, recovered
only by a power cycle) left nothing to examine.

Two signals it exists to capture:

- **`cpu=` stops climbing while `pid=` stays the same.** A render burns ~800
  jiffies (~8s) a frame, so a flat total across several minutes means the
  process is alive but no longer rendering. `/proc/pid/io` does not exist on
  this kernel, and `/dev/fb0`'s mtime is the device node's rather than the last
  write, so neither of those works as a render heartbeat — CPU time is what is
  left.
- **`!!! UNCLEAN SHUTDOWN`** at the top of a boot's samples. `S98health`
  writes `running` to `/root/health.state` on start and `stopped` on a clean
  stop, so finding `running` at boot means the previous run was cut off.
  `/root/health.lastseen` carries the last live timestamp. Verified with a
  sysrq hard reset (`echo b > /proc/sysrq-trigger`), which reproduces a lockup
  closely enough to test the detector, and confirmed silent on a clean stop.

Timestamps before `S99wlan0` runs `rdate` are tagged `(preclock)` — the board
has no RTC and boots at 1970. Order by `up=` instead.

### What the first captured lockup showed (2026-09-14 18:06)

The board froze with the sampler running, so for once there is data. It rules
out more than it confirms — **the board died abruptly while completely
healthy**:

| | |
|---|---|
| `avail` | never below 315MB; **369MB** in the final sample. Not an OOM. |
| RSS | oscillated 33–63MB per render and returned every time. No leak. |
| threads / fds | flat at 7 and 7–8 for the whole 12 minutes. |
| `cpu` | climbing to the last sample (+720j). It died mid-render, not after stalling. |
| load | ~1.0 throughout — one busy process, as expected. |
| thermal | 54°C after recovery; not heat. |

So it is **not** userspace resource exhaustion, which is what the sampler was
built to catch. Both of that day's lockups happened during heavy USB/adb
traffic — one mid-`adb push` of the 17MB binary — and in the second,
`adb get-state` kept answering `device` while `adb shell` could not fork. A
live gadget with a userspace that cannot fork points at the kernel or a vendor
driver (the USB gadget and the AIC8800DC are both out-of-tree blobs on 6.1.99),
not at the dashboard.

One unexplained correlation, on a single data point: `hwm` stepped 68MB → 95MB
at 18:05:09, the largest render peak recorded, and the board died ~60s later.
Memory was returned and 374MB stayed free, so it is not exhaustion — treat it
as a lead, not a cause.

A third lockup the same afternoon (froze 18:20:17, `up=312` — **5.2 minutes**)
looked identical in every metric: 372MB available, RSS 50MB, threads 7, fds 7,
`cpu` still climbing into the final sample. **The survival time is not fixed**
— 11.6 min, then 5.2 min — so do not read a period into it. What is consistent
across all three is the shape: a healthy board that stops instantly, WiFi
first, with the USB gadget still answering `adb get-state` while `adb shell`
can no longer fork.

### The lockup is the external iCal fetch (2026-09-14)

Four tests on one afternoon, each changing one thing:

| Test | Configuration | Result |
|---|---|---|
| baseline | real HTTPS iCal feeds | **died at 5.2 and 11.6 min** |
| 1 | dashboard stopped entirely | survived 26 min |
| 2d | **the same real feed, 5091 events, served from `127.0.0.1`** | **survived 25 min, full 95MB peak** |
| 3 | real HTTPS feeds restored | **died at 11 min**, at the refetch |

Test 2d is the one that matters: identical data, identical parse, identical
memory spike, fetched over loopback instead of TLS — and the board was fine.
Test 3 put the external URLs back on a board that had been up 96 minutes and it
died 11 minutes later, at the 10-minute refetch.

That eliminates rendering, parsing, and the memory peak. It is not HTTPS in
general either: the weather fetch (`api.open-meteo.com`, small JSON, every 15
min) ran successfully throughout all of it. What is left is **pulling ~16MB
over TLS through the AIC8800DC vendor driver**, every 10 minutes.

The feeds are big: `Personal` 2290 VEVENTs / 7.0MB and `Rice` 2801 / 8.9MB,
**5091 events and 15.9MB total, to yield 43 events in the 7-day window**.

Every lockup looks the same and none of them is resource exhaustion — see the
health-log evidence below.

### Isolation test 1 (2026-09-14): the dashboard is implicated

With `S99zdashboard` **stopped** and only the health sampler running, the board
**survived 26 minutes** — healthy the whole way (408MB available, WiFi up,
`load` steady at 0.9 from the I²C storm described under Touch). The same board
had died at **5.2** and **11.6** minutes with the dashboard running.

That is one run, not a proof: with only two failure samples the spread is wide
enough that a single quiet window is possible. Repeat it before treating the
dashboard as the confirmed cause. But it is the first evidence that separates
the two, and it argues **against** the pure kernel/driver theory the health log
seemed to support — a healthy board dying mid-stride looked like a driver
fault, yet the dashboard is what changes the outcome.

Next cut, if the repeat agrees: run the dashboard with `calendars` set to `[]`
(weather still fetches — `weather_minutes` cannot be disabled, config
validation forces any value ≤ 0 back to 15), which separates iCal fetching from
rendering.

**A transfer can corrupt the binary silently.** On 2026-09-15 a dashboard
arrived at exactly the right size with a different checksum:

```
board:  03de1a99d0715f563df3da850ef76cb8
local:  09db2ace36f3de8daa0411353ec49fe2
```

The flipped bytes landed in the Go runtime's startup path, so it died in
`runtime.osinit` before `main` -- `fatal error: index out of range`, `panic
before malloc heap initialized`. Nothing wrote to `/dev/fb0`, the console was
never unbound, and the panel showed boot logs. It looked exactly like a board
that had failed to boot; adb, WiFi and the kernel were all fine.

`scripts/deploy.sh` now md5s every file after pushing it and refuses a
mismatch. **Check the checksum after any hand-rolled deploy too** -- a failed
transfer is loud, a corrupted one is not, and the symptom points at the kernel
rather than at the copy.

This also casts doubt on some of the "lockups" below: several happened
immediately after a deploy, and a binary crashing at startup presents much like
a hang from the outside.

**Deploy over SSH, not `adb push`.** A push that dies mid-transfer takes the
binary with it, and adb is implicated in both freezes. Stage and swap so an
interrupted copy cannot leave the board with no binary:

```bash
scp build/dashboard root@<board>:/root/dashboard.new
ssh root@<board> 'mv /root/dashboard.new /root/dashboard && chmod +x /root/dashboard'
```

The next step for this, if it recurs, is a USB-TTL adapter on the UART pins:
the kernel's dying words are the one thing no userspace sampler can capture.

## Touch

**The I²C bus runs a permanent ~300 interrupt/sec storm**, with nobody touching
the glass. Measured 2026-09-14: `ff060000.i2c` (IRQ 44) accumulated 316,701
interrupts in 1055s of uptime — 300/s average since boot, steady rather than
escalating. A `kworker` sits in D state in `goodix_process_events`, which is
what holds load average near 0.9 on a board that is otherwise 93% idle.

Read that load figure correctly: it is D-state I/O wait, not CPU work, so it
does not mean something is spinning on the processor.

The device tree also declares two I²C devices this hardware does not have, and
both fail to probe at boot:

```
Goodix-TS 2-0014: Error reading 1 bytes from 0x8140: -6
Goodix-TS 2-0014: I2C communication failure: -6
edt_ft5x06 2-0038: touchscreen probe failed
```

`2-0014` and `2-0038` end up with no driver bound; the real digitizer is
`2-005d` (ID 9271) and the backlight is `2-0045`. The storm comes from the
*working* driver at `2-005d`, not from the failed probes.

Whether this is related to the lockups is **unresolved** — the rate is constant
from boot and does not climb toward a freeze, so it does not on its own explain
a hang at a variable 5–12 minutes. It is documented here because it is real,
costs power and wakeups continuously, and was found while chasing the hangs.

The panel's Goodix digitizer is on `/dev/input/event0`, reporting **portrait**
coordinates (0–479 x, 0–1919 y) whatever the framebuffer rotation is.
`internal/touch` decodes `ABS_X`/`ABS_Y` plus `BTN_TOUCH` and rotates into
landscape panel space; a tap is emitted on **release**, so a press-and-drag off
a control does not fire it.

`struct input_event` is **16 bytes** here — a 32-bit kernel. Decoding with the
64-bit layout yields plausible garbage rather than an error, so the size is
pinned by a test.

Tapping an event card opens a detail sheet with a **Hide from panel** action.
Hidden events are stored in `config.json` under `muted`, keyed by iCal UID +
occurrence start (a series shares one UID, so the start is what makes a single
occurrence targetable), and are dropped in `model.Build` before the timed and
all-day split — so a mute removes the event from the card row, the ribbon, and
the NOW/NEXT headline together. Unhide them from the settings page.

Taps and the minute loop both render, serialized by `renderMu`. The dialog
pointer has its **own** mutex: sharing one deadlocked the process on the first
tap and froze the panel, which is now covered by a test.

**Taps cannot be injected on this board — test with a finger.** There is no
`/dev/uinput` in this kernel, and writing `input_event` records to
`/dev/input/event0` is not event injection: the write *succeeds* (rc=0) and the
kernel discards it, because writes to an evdev node are for force-feedback, not
input. A synthetic-tap tool was built, deployed, and confirmed useless this way
— its success return is a false signal, which is why this note exists.

What can be verified without touching the glass: the decoder, the rotation, the
hit-testing, the mute round-trip, and the dialog's rendering, all of which have
tests. What cannot: that a real finger produces the events the decoder expects.

Panel↔device coordinates, for reading a tap by hand:
`deviceX = 479 - panelY`, `deviceY = panelX`.

**`WithPageSize(1920, 480)` is required.** omnidoc defaults to a 1280px
layout viewport, and its fit-within sizing preserves aspect ratio — so without
it the page renders 1280×480 and is pillarboxed with white.

Verify what the panel actually shows:

```bash
adb shell cat /dev/fb0 > /tmp/fb.raw
go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate
```

**The design renders in full as of omnidoc `15ea0c4`** (verified
2026-08-29). Two rounds of engine gaps hit this project and both are now closed
upstream, so `style.css` is ordinary CSS again — `line-height`, `color-mix()`,
`-webkit-line-clamp`, `text-overflow`, layered `background` lists, `z-index`,
`top`+`bottom` sizing, `transform`, and CSS cascading into inline `<svg>` all
work. Roughly 200 lines of workaround came back out, including a Go module that
measured glyph advances from the embedded TTFs to truncate titles.

**One engine gap remains**, in `docs/omnidoc-gaps.md` with a runnable case in
`docs/engine-probes/`: a shrink-to-fit box in a vertical `writing-mode` is sized
on the horizontal axis, so an auto-width vertical box comes out as wide as its
text is long. The fix is to state a `width`; `#now-bar-label` does, and says so.
The other standing item is `sysfont`, which matches a registry rather than the
disk — that is why fonts ship via `@font-face` and `view.FontLoader` instead of
being installed on the board.

`writing-mode` itself works as of `8f7a2f9`/`4543fa3`, so the NOW label is the
string `NOW` with `writing-mode: vertical-rl; text-orientation: upright` rather
than one `<span>` per letter. The flex-margin and shrink-wrap gaps this file
used to list are closed; `padding`-for-margin and `inline-block`-for-sizing are
no longer needed anywhere.

Re-run the probes before trusting either file if the engine moves again. Several
entries were mis-stated on a first reading and only a runnable case with a
working control settled them.

**`docs/TODO.md` is the running list** of what is left before the display is
finished — unfinished verification and deferred findings, ranked. The
visual-fidelity port is closed out there (27 of 28 items); what remains is
mostly portal behaviour and latent issues. Start there rather than re-deriving
it.

A browser preview cannot find these (browsers implement them all); only
rasterizing through omnidoc can. And some bugs only appear on the panel —
the forecast row's clipped bottom line was invisible in both the golden HTML
and the host-side raster, because nothing clips at the document level.

## Gotchas

- **Anything that touches `wlan0` can strand the board.** adb rides the USB
  gadget stack and the board's other route is WiFi, so an experiment that
  disrupts the interface can take out both at once. Running `hostapd` directly
  against `wlan0` did exactly that and needed a physical power cycle. Make such
  experiments self-restoring — background them with an unconditional restore:

  ```bash
  adb shell 'nohup sh -c "hostapd /tmp/ap.conf & sleep 20; killall hostapd; /etc/init.d/S99wlan0 restart" >/tmp/probe.log 2>&1 &'
  ```

  Recovery, in order: `adb kill-server && adb start-server` (the gadget often
  re-enumerates as `rk3xxx` while the adb function is wedged — this clears it),
  then SSH to the WiFi address, then a power cycle.

- **`/tmp`, `/var/log`, `/run` are tmpfs** — they vanish on reboot. `/root` is
  persistent (UBI on NAND).
- **Rootfs has ~90MB free.** A Go binary is 2–15MB; put large data on a microSD
  card rather than NAND.
- **`upgrade_tool RSM`** (read secure mode) is not implemented on this loader —
  it reports "did not support this operation" regardless of mode.
- **Board clock resets to 1970** each boot; there's no RTC battery.
- Flash reads only work in **Loader** mode, not Maskrom — Maskrom has no loader
  resident to talk to the NAND.
