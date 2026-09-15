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

The surviving explanation is the kernel or a vendor driver. Both candidates are
out-of-tree blobs on Linux 6.1.99:

- **`aic8800_fdrv`** — the AIC8800DC WiFi driver, loaded from `/root/*.ko`.
  WiFi is always the first thing to die.
- **`dwc2`** — the USB gadget. It keeps answering after userspace is gone,
  which is either a clue or a coincidence.

There is also a **permanent ~300 interrupt/sec I²C storm** from the touch
driver (see CLAUDE.md, *Touch*), with a `kworker` in D state in
`goodix_process_events`. It is constant from boot rather than climbing toward a
freeze, so it does not explain the timing — but it is continuous kernel-side
pressure on a board that is failing in kernel space, and it has not been ruled
out as a contributing factor.

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
