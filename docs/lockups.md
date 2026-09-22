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

### 8. Something visible in kernel-side counters

*Claim:* every captured lockup ends on a completely healthy sample because the
sampler only ever watched userspace. Watch the kernel — slab, fragmentation,
socket buffers, interrupts, context switches — and the failure will show.

*Tested* by adding those counters to `health.sh` (2026-09-16) and running until
the next lockup. The board died 2026-09-17T00:56:03Z after 3h02m.

*Refuted.* They are as flat as everything else. The final sample, 30 seconds
before death:

```
ctxt=1353/s  forks=2.8/s  dwc2=8016/s  i2c=299/s
slab=12543p  hi=327  sk=45  avail=366612kB
```

Across the whole 3-hour run: `slab` moved **+16 pages** (~64KB), `hi` 333→327,
`sk` 44→45, `avail` 368MB→366MB. No kernel memory leak, no fragmentation
collapse, no socket-buffer leak, no memory pressure. The interrupt rates never
depart from their constant baselines.

One sub-hypothesis died with it, and it is worth stating separately because the
signature section implies otherwise: **`ctxt` and `forks` climb normally into
the final sample.** "Userspace cannot fork" is something that happens *at* the
freeze, not a state the board degrades into beforehand. There is no runway.

This closes the "sample kernel-side metrics" suggestion below. Do not re-add it
hoping for a different answer; the counters are in `health.sh` and they are
flat. What it bought is the knowledge that **everything `/proc` exposes says the
board is healthy one sample before it dies** — which is no longer a hypothesis
about the tooling's blindness but a measurement of it.

### 9. A marginal power supply

*Claim:* sustained WiFi TX/RX is the board's largest current draw, and a supply
browning out under load fits every observation including the healthy final
sample. Listed below as untested and free to try.

*Refuted 2026-09-16/17.* The board ran on a wall charger, physically
disconnected from the Mac, and locked up after **2h12m** (panel clock frozen at
19:05 CDT; `health.lastseen` 00:56:03Z agrees within the sampling interval).

It also disposes of a dwc2 sub-hypothesis: with no host attached there was no
adb traffic and no USB host activity at all, and the board died on schedule
anyway. The *host controller's* SOF storm is untouched by this — it runs
regardless of what is plugged in — but "the Mac's USB traffic provokes it" is
out.

### 10. The kernel panics or oopses

*Claim:* the kernel hits a panic, oops, or BUG, and dies where no userspace
sampler can see it. This is the assumption behind "get a UART and read the
dying words."

*Tested for free, and it is at best half true.* This board has **ramoops
configured and active** — 180KB reserved at `ramoops@83000` in the device tree,
`pstore` mounted at `/sys/fs/pstore` with the `ramoops` backend, a 128KB console
buffer, and `max_reason=2` (panic and oops). Ramoops survives a reboot by
design: that is its entire purpose.

After the 2026-09-17 lockup, `/sys/fs/pstore` was **empty**.

So the kernel did not panic and did not oops. It never reached its own crash
handler. Whatever stops this board stops it *before* the kernel notices anything
is wrong — which rules out a clean software fault and points at something
lower: a hardware hang, a bus lockup, a clock or power-domain failure, or an
interrupt storm with interrupts masked.

This raises the odds that a UART sees **nothing at all** at the moment of death.
Worth knowing before buying one, and worth checking `/sys/fs/pstore` first after
any future lockup — it costs one command.

Check it with:

```bash
adb shell ls -la /sys/fs/pstore/    # empty = no panic, no oops
```

### 11. The I2C touch storm

*Claim:* the Goodix digitizer generates ~300 interrupts/sec from boot with
nobody touching the glass, and parks a kworker in D state. It is the only
continuous kernel-side load that can be removed with one write, so it is the
cheapest remaining variable to eliminate.

*Tested* by unbinding `2-005d` from the `Goodix-TS` driver at boot
(`S97lockupprobe`). Measured effect: i2c interrupts 296/s to **0/s**, the
D-state kworker gone, load average 1.0 to ~0.1.

*Not the cause, but the largest single effect measured so far.* The board still
locked up — at **32.6 hours** (2026-09-19T05:45:50Z), against a previous record
of 6.8h and a typical survival of ~2h.

That is roughly a 5x increase in the record and ~16x the typical figure. Two
consecutive runs both beat the old record, so it is unlikely to be the long tail
of the old distribution. The Goodix driver is implicated as a **contributing
factor**, not the trigger.

The final samples are as flat as every previous lockup. Thirty seconds before
death:

```
dwc2=8008/s  i2c=0/s  ctxt=72/s  forks=3.3/s
slab=12468p  hi=346  sk=42  avail=384316kB  cpu still climbing
```

Note `i2c=0/s` throughout — the storm really was gone, and the board died
anyway.

### 12. The kernel leaves a trace in the console buffer

*Claim:* with `console-ramoops-0` configured and verified working, the next
lockup will capture the kernel's dying printk output and finally show what
happens.

*Refuted.* After the 2026-09-19 lockup, `/sys/fs/pstore` was **empty**. Not a
truncated log, not a partial line — nothing at all.

The setup was verified working beforehand: a test message written to
`/dev/kmsg` and a full shutdown sequence were both read back from
`console-ramoops-0` after a deliberate reboot on 2026-09-17. Ramoops remains
configured after the lockup (128KB console buffer, pstore mounted, `ramoops`
backend).

So the kernel emitted **nothing** on its way down. Combined with hypothesis 10
(no panic, no oops, `softlockup_panic` and `hardlockup_panic` both enabled and
never firing), the picture is consistent and now well-supported:

**The CPU stops executing without the kernel ever noticing.** No fault handler
runs, no printk is emitted, no watchdog fires. That is not a software crash. It
is the behaviour of a clock stopping, a power domain collapsing, or a bus
wedging hard enough to take the core with it.

This also lowers the expected value of a UART console. A serial line can only
show what the kernel prints, and the kernel prints nothing.

## What the instrumentation is worth now

Seven hypotheses refuted by testing, two more here, and the sampler is
comprehensive. That is the state to reason from:

- **Userspace**: RSS, HWM, threads, fds, CPU jiffies — flat, and CPU still
  climbing into the final sample every time (it dies mid-render).
- **Kernel**: slab, buddyinfo, sockets, TCP pages, interrupts, ctxt, forks —
  flat.
- **Hardware**: memtester 16 tests 0 failures; two power sources; two boards
  (the second died during setup, so this is weaker than it sounds).

Nothing observable from userspace changes before the freeze. The next piece of
evidence has to come from outside userspace, and there is exactly one way to
get it.

## The next step

**Read hypotheses 10 and 12 first.** They change what is worth doing, and they
argue against the UART that the rest of this section recommends.

The kernel does not panic, does not oops, never fires `softlockup_panic` or
`hardlockup_panic` (both enabled), and emits **nothing** into a
verified-working ramoops console buffer. A UART shows what the kernel prints.
The kernel prints nothing. Expect silence mid-line — which is real evidence,
but it is one bit of information for the cost of wiring up a header.

Every software-side suggestion that used to live here has been carried out:

- ~~Sample kernel-side metrics~~ — done, hypothesis 8. They are flat.
- ~~Try a different power supply~~ — done, hypothesis 9. It died on wall power.
- ~~Run with WiFi down~~ — done, hypothesis 7. It died with the driver unloaded.
- ~~Capture the kernel's dying words~~ — done, hypothesis 12. There are none.
- ~~Remove the I2C storm~~ — done, hypothesis 11. Survival 2h to 32.6h, still died.

**What the evidence now supports.** Twelve hypotheses tested, twelve refuted.
Everything observable from software says the board is healthy in the sample
before it stops. The failure is below the level any software on this board can
observe, which means more instrumentation of *this* board has a poor expected
return.

Two things are worth more than a UART:

1. **A second board, properly tested.** The one genuine attempt (2026-09-15)
   ended with the replacement destroyed during setup, so the board-defect
   question has never actually been answered. If a second unit runs for a week
   on the same image, this one is defective and the investigation is over.
2. **The dwc2 misconfiguration**, which is the one concrete defect found and
   never corrected: `rk3066-usb` parameter fallback, `power_down = NONE`,
   `no_clock_gating = true`, a permanent 8,000/s SOF storm and `Mode Mismatch`
   interrupts. Fixing it needs a kernel rebuild (`~/code/vendor/linux-rockchip`,
   `drivers/usb/dwc2/params.c:104`); the module is loadable, so only `dwc2.ko`
   has to be replaced. Note the version gap: the board runs 6.1.99, the armbian
   branches are 6.1.115.

The three dashboard crashes (see `crashes/README.md`) are the other live thread
— three different Go runtime invariants corrupted in three different
subsystems, on a board with a DMA-capable peripheral in a known-bad state.

### Neither USB-C port can do this

Worth stating plainly, because it is the obvious thing to try first. The two
ports are different controllers and neither carries a console:

| Port | Controller | `dr_mode` | What it is |
|---|---|---|---|
| power/adb | `ff740000.usb` | `peripheral` | the gadget — power in, adb out |
| the other | `ff780000.usb` | `host` | a **host** port; already runs the internal hub and the AIC8800DC |

A host port talks *to* devices; it does not emit a console. The console UART is
at `0xff0a0000` (UART0, per `earlycon=uart8250,mmio32,0xff0a0000` in
`/proc/cmdline`) and is routed to header pins. USB-C can carry UART over its SBU
pins on boards designed for it; this is not one.

The host port is still useful for something else — see *Capturing to USB storage*
below.

### What to buy

Any 3.3V USB-TTL adapter: CP2102, CH340, FT232RL. **It must be 3.3V** — a 5V
adapter on RK3506 UART pins can damage the SoC. Most sell as "3.3V/5V" with a
jumper; set it to 3.3V before connecting anything.

### Wiring

Three jumper wires to the board's UART0 header, crossed TX↔RX:

| Adapter | Board |
|---|---|
| GND | GND |
| RX | TX |
| TX | RX |

**Do not connect the adapter's VCC.** The board has its own supply; back-feeding
it through the header is a good way to lose a second board.

Console settings are the Rockchip default: **1500000 baud, 8N1**, no flow
control. That is not a typo — 1.5 Mbaud, not 115200.

```bash
ls /dev/cu.usbserial-* /dev/cu.SLAB_USBtoUART* /dev/cu.wchusbserial*
screen /dev/cu.usbserial-XXXX 1500000     # ctrl-a k to quit
```

### Capturing to USB storage

The second USB-C port (`ff780000.usb`) is a **host** port, so a flash drive
plugged into it mounts like any other disk. That does not capture a panic, but
it removes one doubt about the health log: `/root` is UBI on NAND, and a hard
freeze can lose whatever the last write had not flushed.

`usb_storage` is already loaded. If the sampler writes to a mounted stick with
`sync` on every line, the log on the stick is guaranteed to be on disk at the
moment of the freeze rather than in a page cache that never flushes.

Worth doing only if the NAND log is ever suspected of truncation. So far its
last line has always been a plausible 30s before death, so it probably is not
lying — but the option is free and the port is otherwise idle.

### What it is for

Capture to a file and leave it running until the board dies:

```bash
screen -L -Logfile uart-$(date +%F).log /dev/cu.usbserial-XXXX 1500000
```

The dying words are the whole point — a watchdog trace, a dwc2 error, a stall
warning, anything at all. Ten hypotheses have been refuted by inference because
the board reports perfect health right up to the last sample. The UART is the
one channel that does not go through the userspace that stops working.

**Temper the expectation, though.** Hypothesis 10 shows the kernel never
panics or oopses — `/sys/fs/pstore` is empty after a lockup, on a board where
ramoops is configured and would have caught either. A UART may therefore show
nothing at the moment of death, and *that itself is the finding*: a console
that goes silent mid-line without a trace says the CPU stopped executing, which
is a very different problem from a kernel that crashed.

What a UART adds beyond pstore:

- **Pre-death chatter** ramoops would not record — driver warnings, timeouts,
  link resets in the seconds before the freeze.
- **The nothing.** Silence mid-line, with no oops, is positive evidence for a
  hardware-level stop.
- **U-Boot and early boot**, which no userspace tool can reach.

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
