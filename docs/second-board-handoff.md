# Second board: flashing blocked

Handoff for the 2026-09-22 attempt to bring up a replacement board. The board is
**undamaged and sitting in Maskrom**; no write ever reached its flash. What
stopped us is the loader download, and it stops both available tools at the same
step.

Read `lockups.md` first for why a second board matters. The short version: twelve
hypotheses tested and refuted, and the board-defect question has never been
answered because the first replacement was destroyed during setup on 2026-09-15.

## State right now

| | |
|---|---|
| New board | Maskrom, `Vid=0x2207 Pid=0x350f`, nothing written |
| Control board (old) | up on wall power, reachable at `192.168.1.86` |
| `build/dashboard` | built, `ELF 32-bit ARM`, md5 `dae59ffba546414479b0b96d840e7b3e` |
| Images | both verified `RKFW` (`524b4657`) in `~/Downloads/` |

The plan agreed with the user: flash **base Lyra** (`Luckfox_Lyra_Flash_250717`)
then graft WiFi with `setup-wifi.sh`, so the new board matches the control
board's stack exactly, and keep the old board running as a same-window control.

## The blocker

**The loader will not stay resident.** Every flash operation needs a loader
downloaded into the board first; the board accepts it (sometimes reporting
success) but never transitions out of Maskrom, so every subsequent command that
has to address the flash fails.

Observed across both tools:

| Command | Result |
|---|---|
| `upgrade_tool DB <MiniLoaderAll.bin>` | `Download boot ok.` (rc=0), board **stays Maskrom** |
| `upgrade_tool UF <update.img>` | `Download Boot Start` → `Download Boot Fail` |
| `rkdeveloptool db <MiniLoaderAll.bin>` | hangs indefinitely, no output, no timeout of its own |
| `rkdeveloptool wl 0x2000 uboot.img` | `Write LBA failed!` — immediate, nothing written |
| `rkdeveloptool rfi` / `rci` / `td` | all fail — they need the loader |
| `rkdeveloptool ld` | **works** — enumeration needs no loader |

The `ld`-works/everything-else-fails split is the signature: USB communication is
fine, the flash is unreachable.

**This is not new.** The original board showed the same Maskrom→Loader failure on
2026-09-15 (`Wait For Maskrom Fail`, and a `DB` that returned success without
changing mode). It was worked around then, not solved.

### Two dead ends, recorded so they are not retried

**`rkdeveloptool` does not support this board's storage.** Source at
`~/code/vendor/rkdeveloptool` (upstream main, HEAD 2025-03-07) has **zero**
matches for `spinand`/`SPINAND`. Its `cs` options are `1=EMMC, 2=SD, 9=SPINOR` —
SPI **NOR**. This board is W25N02KV SPI **NAND**. Rebuilding will not help;
the feature is not implemented upstream.

**Repeated attempts degrade the board's state.** After several failed loader
pushes, `upgrade_tool DB` began returning `Download boot failed! ... please check
ddr, please reset device and retry`. A fresh Maskrom entry (unplug, hold BOOT,
reconnect) cleared it and `DB` succeeded again on the first try. **If a loader
push fails, reset before retrying** rather than hammering it.

## What to try next

1. **Flash from another machine.** A Linux box or a different Mac with a working
   vendor-tool path. This is the lowest-risk option — the images and the
   procedure are known-good, only the host is the problem.

2. **Investigate the loader itself.** `MiniLoaderAll.bin` from
   `Luckfox_Lyra_Flash_250717` is what both tools are pushing. The Zero W image
   ships its own (`~/Downloads/Luckfox_Lyra_Zero_W_Flash_250717/`) — worth trying
   that one, since it is built for the board that actually has this SoC variant.
   Nothing has tested whether the base-Lyra loader is even correct for a Zero W.

3. **Soak the new board on its shipped firmware.** Answers "is this board
   defective?" without flashing. Be clear about what it buys — the test is
   **one-sided**:

   - *It locks up* → decisive. Two boards, two software stacks, same failure;
     rules out both a defect in the old board and anything specific to the
     base-Lyra stack.
   - *It survives* → ambiguous. Board and stack both changed, so neither can be
     attributed.

   Given the old board's last run was 32.6h, "survives" needs days to distinguish
   from "has not failed yet" — and that is exactly the branch where matching the
   stack would have mattered. Worth running as a cheap **negative** test; not a
   substitute for a proper flash.

## Correction to CLAUDE.md

CLAUDE.md currently says the `upgrade_tool` segfault is caused by running it
under `sudo`, and that the arm64 slice is fine. **That edit (2026-09-15) was
wrong and should be reverted.**

The crash report from 2026-09-22 (`~/Library/Logs/DiagnosticReports/
upgrade_tool-2026-09-22-090918.ips`) shows:

```
arch: ARM-64
EXC_BAD_ACCESS, KERN_INVALID_ADDRESS at 0x0000000000000008
  libsystem_pthread.dylib  arm64e
  upgrade_tool             arm64
```

`libsystem_pthread` on top of an arm64 stack is the `pthread_mutex_init` startup
crash CLAUDE.md described **originally**. The Sept 15 `sudo` theory confused a
correlation (sudo bypassed the arch wrapper) for the cause. The original warning
was right.

Note this crash is a *startup* failure and is separate from the loader problem
above — when `upgrade_tool` does run, it reaches `Download Boot Fail`, the same
wall `rkdeveloptool` hits.

## Method notes

- **Never pipe a flashing tool.** `rkdeveloptool db` was run through `tail` on
  the first attempt here. It caused no damage (a loader push is not a flash
  write) but it is the exact mistake that destroyed the 2026-09-15 board, and
  `flash.sh` traps SIGPIPE specifically to prevent it.
- **`ld` working does not mean the tool works.** Enumeration needs no loader and
  no storage driver, so it succeeds even when nothing else can.
- **Check `ioreg` before believing a tool that reports no device.** A tool
  without USB access reports an empty list, which is indistinguishable from
  absent hardware:
  `ioreg -p IOUSB -l -w 0 | grep -A12 rk3xxx`
