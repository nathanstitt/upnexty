# The lockups

The board freezes at unpredictable intervals. This records what was measured,
which explanations were tested, and how each one died — so the next attempt
starts from evidence instead of repeating it.

**Status: unresolved.** Every software-side hypothesis has been tested and
refuted. What survives points below userspace, where nothing available on this
board can see.

## The signature

Identical every time, across at least eight occurrences on 2026-09-14/15:

| | |
|---|---|
| Panel | frozen on its last frame |
| WiFi | gone — no ping, no SSH |
| `adb get-state` | **answers `device`** |
| `adb shell` | hangs, produces nothing |
| Recovery | power cycle only |

That third and fourth line together are the most informative thing here: the
USB gadget driver is alive and responding while userspace can no longer fork a
process. Something below the application is broken, but not everything.

**Timing is not periodic.** Observed survival times: 5.2 min, ~11 min, 11.6
min, ~85 min, ~2 h. Do not read a cadence into it — an early attempt to do so
("consistent 12–13 minutes") was wrong and sent the investigation down a blind
alley.

## What the health log shows

`/root/health.log` samples every 30s (see CLAUDE.md, *Health logging*). In
every captured lockup the final sample before death is **completely healthy**:

```
2026-09-14T18:20:17 up=312 avail=372540kB free=344064kB load=1.05
                    rss=50192kB hwm=91480kB thr=7 fd=7 cpu=5239j wlan0=up
```

- `avail` never below **315MB** of ~430MB usable
- threads and fds flat
- `cpu` still climbing into the final sample — it dies **mid-render**, not
  after stalling
- 54°C after a recovery; not thermal

So it is not resource exhaustion, and not a slow degradation. The board is fine
and then it is not.

## Hypotheses, and how each one died

Six theories. All tested. All refuted. Listed because knowing what it *isn't*
is most of what has been bought.

### 1. Stale calendar cache

*Claim:* a failed fetch left the panel serving old events, so a meeting looked
"missing".

*Refuted:* the events had been on the calendar for months, so no cache
predating them could explain it. (The missing events had a separate, real
cause — see **Two real bugs** below.)

### 2. A fixed ~12-minute period

*Claim:* deaths clustered near 12 minutes, suggesting a timer.

*Refuted:* the next lockup came at `up=312` — **5.2 minutes**. The spread is
wide and unpredictable; the apparent pattern was two samples.

### 3. Fetching 16MB over TLS stresses the WiFi driver

*Claim:* the iCal feeds total 15.9MB, and pulling that through the AIC8800DC
vendor driver every 10 minutes provokes the fault.

*Tested properly*, and this is the one that looked strongest for a while:

| Test | Configuration | Result |
|---|---|---|
| 1 | dashboard stopped entirely | survived 26 min |
| 2d | same real feed, 5,091 events, served from `127.0.0.1` | survived 25 min, full 95MB peak |
| 3 | real HTTPS feeds restored | **died at 11 min** |

Test 2d was the good experiment: identical data, identical parse, identical
memory spike, only the transport changed — and the board was fine. Test 3 put
the external URLs back and it died.

*Refuted the next day:* the same configuration ran **2 hours** without
incident. One death after one change is not causation when the failure is
intermittent. Also relevant: Go requests gzip automatically and Google serves
these feeds compressed, so the wire transfer was ~782KB per feed, not 7MB — the
magnitude the theory rested on was wrong.

### 4. Bad RAM

*Claim:* a SIGSEGV inside `bytes.Replace` with an intact binary and 375MB free
looks like memory corruption.

*Refuted:* `memtester 320M` — **16 tests, `Status: PASS`, 0 failures**. Stuck
Address, Random Value, all six Compare variants, Sequential Increment, Solid
Bits, Block Sequential, Checkerboard, Bit Spread, Bit Flip, Walking Ones,
Walking Zeroes.

*Caveat worth keeping:* memtester runs in userspace over `mlock`ed pages in a
tight single-threaded loop. It cannot test kernel pages, and it never
reproduces the access pattern that matters here — GC write barriers across the
heap while WiFi and USB DMA run concurrently. A clean pass narrows the field;
it does not clear the memory subsystem.

### 5. Large-allocation churn during GC

*Claim:* the crash dump is specific — the calendar fetch goroutine
(`main.go:196`), inside `bytes.Replace`, with a stack full of GC internals
(`mcentral.cacheSpan`, `gcControllerState.heapGoalInternal`, `gcTrigger.test`).
The four `ReplaceAll` unfolding passes allocate four 16MB buffers in quick
succession — the program's largest allocation burst.

*Refuted:* switching to the Google Calendar backend removes it entirely — no
16MB buffers, no unfolding passes, peak RSS **60MB instead of 91–126MB**. The
board locked up anyway, after ~85 minutes.

### 6. My own activity (deploys, restarts, adb traffic)

*Claim:* lockups clustered around deploys and service restarts.

*Refuted:* two lockups occurred unattended on the evening of 2026-09-14
(20:23, 22:10) with nobody touching the board.

## Two real bugs found along the way

Both were silent-failure bugs, found while chasing the lockups, and both are
fixed. Neither causes the freezes.

**The 8MB feed cap truncated a calendar on every fetch.** `httpGet` read
through `io.LimitReader(body, 8<<20)`; `io.ReadAll` stops at the limit and
returns **no error**. One feed is 8,947,334 bytes against an 8,388,608-byte
cap, so 559KB was discarded every ten minutes and the truncated iCal parsed
"successfully" with its tail events missing. This is what made events vanish
from the panel with a green fetch.

**A deploy can corrupt the binary silently.** A dashboard arrived at exactly
the right size with a different md5. The flipped bytes landed in the Go
runtime's startup path, so it died in `runtime.osinit` before `main`, nothing
wrote to `/dev/fb0`, and the panel showed boot logs — indistinguishable from a
board that failed to boot. `adb push` reported success. `scripts/deploy.sh` now
verifies checksums.

The second one matters for reading this document: **some events recorded as
"lockups" may have been this crash instead**, since a binary dying at startup
looks much like a hang from outside. The unattended ones cannot be explained
that way.

## What is left

The surviving explanation is the kernel or a vendor driver on Linux 6.1.99.

- ~~**`aic8800_fdrv`** — the AIC8800DC WiFi driver.~~ **Refuted 2026-09-15**
  (hypothesis 7): the board died at 1h48m with the module not resident.
- **`dwc2`** — the USB controller, and now the leading candidate by
  elimination. The gadget (IRQ 53) keeps answering `adb get-state` after
  userspace is gone, and the host side (IRQ 54) runs a permanent 8,000
  interrupt/sec SOF storm — see *The dwc2 SOF interrupt storm* below.
- **The kernel itself**, or the Rockchip BSP's other out-of-tree pieces.

WiFi dying first is still the most consistent symptom, but hypothesis 7 shows
that is a *consequence* rather than the cause — the board locks up the same way
with no WiFi driver in memory at all.

There is also a **permanent ~300 interrupt/sec I²C storm** from the touch
driver (see CLAUDE.md, *Touch*), with a `kworker` in D state in
`goodix_process_events`. It is constant from boot rather than climbing toward a
freeze, so it does not explain the timing — but it is continuous kernel-side
pressure on a board that is failing in kernel space, and it has not been ruled
out as a contributing factor.

## The second-board test (2026-09-15) — attempted, not completed

The obvious way to separate "this board is defective" from "this design locks
up" is a second board. One was connected on 2026-09-15. **The test never ran**
— the board was destroyed during setup, before it rendered a single frame. The
board-defect question is still open.

What was established before it died is worth keeping:

**The replacement shipped with stock Zero W firmware**, not the base-Lyra image
this project flashes. `aic8800_fdrv` and `aic_load_fw` were already resident,
`/root` was empty, and the panel was at stock 800×1280. So a second board is not
a drop-in comparison: to reproduce this stack it has to be reflashed with
`Luckfox_Lyra_Flash_*` and then have the WiFi driver grafted on by
`setup-wifi.sh`, exactly as CLAUDE.md describes.

**How it was destroyed.** The flash was run as `upgrade_tool UF … | head`. `head`
exited at 3%, the kernel delivered SIGPIPE, and the write stopped having erased
the bootloader and written almost none of the image. The board fell back to
Maskrom — recoverable in principle — but then stopped enumerating on USB
entirely and never powered up again. The same cable and port worked on the
original board immediately afterward, which rules out the host side.

No mechanism connects a NAND write to a power failure: mask ROM is on-die and
unwritable, and a bootloader-wiped board should still present a USB gadget. The
likeliest reading is an unrelated hardware failure with very unlucky timing.
That is not certain, and the interrupted write was avoidable regardless.

`scripts/flash.sh` now ignores SIGPIPE for the duration of the write and tees
its own log, so no caller's pipe can interrupt a flash again.

### Two tooling bugs found, both fixed

**`flash.sh` could never have worked on a Mac.** `run_upgrade_tool` called
`sudo` unconditionally. Under `sudo`, `upgrade_tool` segfaults immediately —
`EXC_BAD_ACCESS`, `KERN_INVALID_ADDRESS at 0x8`, four frames from `start`,
before it touches a device. Run as the ordinary user it flashes normally. Note
this is **not** the arm64 crash CLAUDE.md documents: this was the x86_64 slice
under `arch -x86_64`, and the trigger is `sudo`, not the architecture. macOS
gives the logged-in user USB access; root was never required.

**Maskrom → Loader never completes on this hardware.** `DB MiniLoaderAll.bin`
reports `Download boot ok` and returns 0, but the board stays in Maskrom through
30s of polling, and `UF` from Maskrom fails at `Wait For Maskrom Fail` every
time. `UF` from **Loader** mode worked on the first try. If a future board lands
in Maskrom, expect to need a real power cycle with BOOT held to reach Loader —
the software transition does not work here.

### For the next attempt

- **Flash from Loader, not Maskrom**, and never pipe the flash into anything.
- **`reboot loader` is unreliable on stock Zero W firmware.** Plain `adb shell
  reboot loader` returned `Waiting for SIGTERM`, rc=255, and the board kept
  running with uptime climbing. Detaching it (`nohup sh -c "sleep 1; reboot
  loader" &`) did reboot it. Holding BOOT while connecting is the dependable
  route.
- **`upgrade_tool` reports `connected(0)` when it cannot access USB** — the same
  output as a genuinely absent board. Confirm with `ioreg -p IOUSB -l -w 0 |
  grep 'idVendor" = 8711'` before concluding anything about the board's state.
  Reading that empty list as "the board is gone" wasted a cycle here.
- **Capture the real `config.json` first.** Only `config.sample.json` with
  `PASTE_ICAL_URL` is in the repo; the working config lived on the board. A soak
  against an empty calendar never exercises the fetch path that hypothesis 3 was
  built on, so it cannot refute — or confirm — the most interesting theory.
- **Soak well past 2 hours.** Recorded survival ranges from 5.2 minutes to ~2
  hours, so anything shorter than several hours of clean running says nothing.

### 7. The AIC8800 WiFi driver

*Claim:* `aic8800_fdrv` is an out-of-tree vendor blob, WiFi is always the first
thing to die, and every other software explanation had been eliminated. This was
the leading hypothesis going into 2026-09-15.

*Tested* by unloading the driver entirely and keeping the workload identical.
`soak-nowifi.sh` serves a 4.5MB / 5,000-event iCal feed from `127.0.0.1`, so the
parse, the unfolding passes, the ~66MB `VmHWM` spike and the ~10s render all
still happen -- only the radio is gone. This is test 2d's design (same data,
different transport) applied to the driver instead of the feed.

*Refuted.* The board locked up at **1h48m** (started 20:21:27, died 22:09:50),
squarely inside the established 5min--2h range, with the driver not resident.

The evidence that it really was unloaded matters, because `wlan0=-` alone does
not prove it -- `ip link set wlan0 down` and `rmmod` produce an identical
reading. What settles it is `slab=`: the driver is worth ~5,000 pages (~20MB) of
kernel memory, and across all 211 samples of the soak `slab` stayed within 71
pages of 7429, against 12,485 with the driver loaded. It never returns. (The
sampler now records `wdrv=y/n` from `/proc/modules` directly, so this does not
have to be reconstructed again.)

Incidentally also refutes **a marginal power supply**: the board was on Mac USB
power when it died at 22:09, and the move to a wall charger came afterwards.

## The dwc2 SOF interrupt storm

Found while reading the new kernel counters. Not yet linked to the lockups, but
it is a real defect and it is on one of the two remaining suspects.

**The USB *host* controller takes ~8,000 interrupts/sec, permanently.** Measured
2026-09-16 on an idle board:

| | |
|---|---|
| IRQ 53 `ff740000.usb` (gadget/adb) | 59 total -- idle |
| IRQ 54 `ff780000.usb` (host) | 7.2M, **8,039/s**, all on CPU1 |

It is not traffic. A `ping -f` flood over WiFi moved it from 8,039/s to
8,048/s -- nine interrupts per second of actual data on an 8,000/s floor. It is
also independent of everything else tested: identical at 8,000/s during the
2026-09-15 soak with `aic8800_fdrv` unloaded and no network at all, and
identical with the adb host disconnected.

8,000/s is exactly the USB 2.0 high-speed **microframe (SOF) rate** -- one
interrupt every 125µs. The registers confirm the mechanism:

```
GINTMSK = 0xf300080e    bit 3 (SOF) SET -- the interrupt is unmasked
GINTSTS = 0x04600001    serviced and cleared between reads
HPRT0   = 0x00001005    port enabled, connected, high-speed
```

A correctly configured dwc2 masks SOF when it has no periodic transfers to
schedule. This one leaves it enabled and wakes the CPU 8,000 times a second to
do nothing, on a 1.2GHz Cortex-A7. The only devices on that bus are the onboard
hub (`1a86:8091`) and the AIC8800DC (`a69c:88dc`).

Worth noting what this is *not*: it is not the gadget, so it is unrelated to
adb, and disconnecting the host cable would not change it. The earlier reading
in CLAUDE.md that treated IRQ 54 as the adb gadget was wrong -- IRQ 54 is shared
between `ff780000.usb` and `dwc2_hsotg:usb1`, and the gadget is IRQ 53.

Whether it causes the lockups is **unresolved**, and the honest case against is
the same one that applies to the I²C storm: the rate is constant from boot and
does not climb toward a freeze, so it does not by itself explain a hang at a
variable 5 minutes to 2 hours. What makes it worth pursuing anyway is that it is
continuous kernel-side pressure on one of the two named suspects, and it is
measurable and fixable in a way the rest of this investigation has not been.

## The next step

**A USB-TTL adapter on the UART pins.** There is no serial console on the
USB-C ports (both are OTG/host), so this needs the header. It is the only
diagnostic that sees the kernel's dying words — a panic, an oops, a watchdog
trace — and every remaining hypothesis lives exactly where userspace tooling is
blind. That blindness is why the health sampler kept reporting a healthy board
one second before death.

Cheap, and the only thing likely to move this forward.

Secondary, if a UART is not available:

- **Sample kernel-side metrics** in `health.sh` — `/proc/slabinfo`,
  `/proc/vmstat`, `/proc/net/sockstat`. The current sampler watches userspace
  only, so a kernel memory or socket-buffer problem would be invisible to it.
- **Try a different power supply.** Sustained WiFi TX/RX is the board's largest
  current draw, and a marginal supply browning out under load fits every
  observation here — including the healthy final sample. Untested, and it costs
  nothing to swap.
- **Run with WiFi down** (adb only, no calendar fetches) for several hours. If
  the board survives, that implicates the WiFi driver directly rather than by
  elimination.

## A note on method

Six hypotheses were proposed with some confidence and six were refuted by
testing. The pattern worth carrying forward: each one was believable because it
explained the symptoms available *at that moment*, and each died when given a
test that could have gone either way.

The two experiments that actually moved things were the ones designed so both
outcomes were informative — test 2d (same data, different transport) and the
memtester run. The ones that wasted time were inferences from correlation: "it
died after I changed X" is nearly worthless when the baseline failure rate is
unknown and the interval ranges from 5 minutes to 2 hours.

Measure the baseline before attributing anything to a change.
