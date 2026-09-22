# Second board: resolved

Record of the 2026-09-22 attempt to bring up a replacement board. An earlier
version of this file described the loader download as a blocker on the host.
It was not. The second replacement board is defective; a third board flashed
and now runs the control board's stack. This file keeps what was learned.

Read `lockups.md` first for why a second board matters.

## State at the end of the day

| | |
|---|---|
| Board 2 (second replacement) | defective, unflashed, **return it** |
| Board 3 | base Lyra `250717` + WiFi graft, `192.168.1.115`, dashboard rendering, soaking |
| Control board | untouched, back on wall power, `192.168.1.86`, soaking |
| `scripts/setup-wifi.sh` | fixed: `strings` is an Xcode shim that refuses to run without the licence; now `grep -a` |

Both boards run `S97lockupprobe`, which unbinds the touch driver at boot, so
"taps disabled" in the dashboard log is the soak configuration, not a fault.

## What was actually wrong

**The `LD` label after `DB` was misread.** On RK3506 the usbplug that `DB`
loads re-enumerates as `USB-MSC` (serial `rockchip`) but keeps `bcdUSB 0x0200`,
and `upgrade_tool LD` derives `Mode=` from that bit alone. So a board with a
working loader prints `Mode=Maskrom`. Every "DB succeeds but the board stays in
Maskrom" observation, on 2026-09-15 and again on 2026-09-22, was this. The
right check after `DB` is `TD` or `RCI`.

Proven with the control board on the same host, cable, tool and loader:

| time | event |
|---|---|
| `RD 3` from Loader | reboots into Maskrom without touching NAND |
| `DB` +1.1 s | re-enumerates as `USB-MSC`, `bcdUSB 0x0200` |
| `DB` +4 s | `Download boot ok` |
| after | `TD`, `RCI`, `RID` (`SNAND`), `RFI` (255 MB) all answer |

**Board 2 is defective.** Same procedure, twice, with both tools: the ROM
accepted the loader, no `USB-MSC` ever appeared, and every probe afterwards
failed with `RKU_Write failed, err=-1`. usbplug dies in DRAM. Its NAND did not
boot with no button held either. The `rkdeveloptool db` "hang" is the same
failure: its `CMD_TIMEOUT` is 0, so the control transfer the ROM stops
answering blocks forever, where upgrade_tool logs `vendor=0x471 ... err=-7`.

**The 2026-09-15 control was not a control.** That Maskrom test ran on the
first replacement after its NAND was half erased and shortly before it stopped
enumerating. The healthy board had never been through `DB` until today.

## Corrected dead ends

- **The base-Lyra and Zero W loaders are the same build.** 232 of 268,736
  bytes differ, starting at the build-date field. Trying the other one was
  never going to change anything.
- **`rkdeveloptool` is not "missing SPI NAND support".** Storage selection is
  the loader's job; the host tool only issues LBA commands. `wl` failed because
  no loader was resident. Its infinite control-transfer timeout is the real
  reason not to use it on a board that does not answer.
- **The `upgrade_tool` crash is the arm64 slice**, as CLAUDE.md now says. Both
  recorded crashes were `UF` runs started by absolute path from a directory
  with no `config.ini`; runs from the tool's own directory did not crash. That
  is a correlation, not a confirmed cause.

## What board 3 taught

- **BOOT held at power-on gives Loader, not Maskrom.** It is U-Boot's recovery
  key. A board only shows Maskrom when its NAND does not boot.
- **Every board reports the same serial** `b57290249a9b3206` over adb and
  rockusb. It is a Luckfox constant. Two boards on one Mac collide on adb, so
  keep the second on wall power and WiFi only.
- **Board 3 drops off USB on its own.** Three times in one hour it vanished
  from the bus entirely, from Loader and from Linux, with nothing touched.
  Three `flash.sh` runs found no device for that reason; the fourth passed
  seconds later with the same board. Use another cable and reseat it at the
  board end before any USB work on it. On wall power and WiFi it is fine.
- **The stock `/etc/wpa_supplicant.conf` on the base image is an open-network
  stub**, and `setup-wifi.sh` only replaces a file containing `ssid="SSID"`,
  so it reports "already configured" and leaves the stub. Copy the control
  board's file over it.

## Method notes

- **Never pipe a flashing tool.** `flash.sh` traps SIGPIPE for that reason.
- **`ld` working does not mean the tool works.** Enumeration needs no loader.
- **Check `ioreg` before believing a tool that reports no device**:
  `ioreg -p IOUSB -l -w 0 | grep -A12 '"idVendor" = 8711'`. The product
  name there (`USB download gadget`, `USB-MSC`, `rk3xxx`, or a bare
  `IOUSBHostDevice`) says more than `LD` does.
- **A polled `DB` is the diagnostic.** Poll `ioreg` for `sessionID` every
  half second during `DB`: a healthy board changes it once and comes back as
  `USB-MSC`; a dead one either never changes it or changes it and never
  answers again.
