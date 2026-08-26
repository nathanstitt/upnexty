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

The Go commands (`html2fb`, `fbtouch`) live in the **doctaculous** repo under
`cmd/`. Override its location with `DOCTACULOUS_DIR`.

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
`html2fb`'s blit, covered by round-trip tests in the doctaculous repo.

Writes to `/dev/fb0` are not vsynced. Fine for static pages; animation would
want double-buffering via `FBIOPAN_DISPLAY` or the DRM path (`card0` exists).

Vendor display test, if you suspect the pipeline:

```bash
adb shell modetest -M rockchip -s 74@71:480x1920
```

## Dashboard

The `dashboard` service renders the UpNext display: it fetches iCal calendars
and Open-Meteo weather, generates HTML+SVG, rasterizes with doctaculous, and
writes `/dev/fb0`. Display-only — no touch, no HTTP server.

```bash
scripts/build.sh dashboard
scripts/deploy.sh build/dashboard
adb push config.sample.json /root/config.json     # then edit in the iCal URLs
adb shell /root/dashboard --once                  # single frame
adb shell 'nohup /root/dashboard >/root/dashboard.log 2>&1 &'
```

Measured on the board: **~1.05–1.18s per frame**, ~167MB RSS at the peak of a
render, settling to **~46MB between renders**. The loop wakes on the minute
boundary; verified re-rendering on rollover.

**`WithPageSize(1920, 480)` is required.** doctaculous defaults to a 1280px
layout viewport, and its fit-within sizing preserves aspect ratio — so without
it the page renders 1280×480 and is pillarboxed with white.

Verify what the panel actually shows:

```bash
adb shell cat /dev/fb0 > /tmp/fb.raw
go run ./tools/fb2png /tmp/fb.raw /tmp/panel.png 480 1920 unrotate
```

**Some of the design does not render yet.** doctaculous does not implement
inline `<svg>`, `var()`, alpha colors, and several other features, so the
weather icons are invisible and the dark theme renders black-on-white. These
are being fixed upstream, not worked around here — see
`docs/doctaculous-gaps.md` for the list, each with an isolated repro.

A browser preview cannot find these (browsers implement them all); only
rasterizing through doctaculous can. And some bugs only appear on the panel —
the forecast row's clipped bottom line was invisible in both the golden HTML
and the host-side raster, because nothing clips at the document level.

## Gotchas

- **`/tmp`, `/var/log`, `/run` are tmpfs** — they vanish on reboot. `/root` is
  persistent (UBI on NAND).
- **Rootfs has ~90MB free.** A Go binary is 2–15MB; put large data on a microSD
  card rather than NAND.
- **`upgrade_tool RSM`** (read secure mode) is not implemented on this loader —
  it reports "did not support this operation" regardless of mode.
- **Board clock resets to 1970** each boot; there's no RTC battery.
- Flash reads only work in **Loader** mode, not Maskrom — Maskrom has no loader
  resident to talk to the NAND.
