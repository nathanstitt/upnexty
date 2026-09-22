# Captured failures

Evidence from the lockup investigation (see `../lockups.md`). Kept because both
files are hard to reproduce on demand and easy to lose: `dashboard.log` is
truncated at every boot, and a lockup is followed by a reboot.

## `dashboard-crash-2026-09-16T2007Z.log`

The dashboard process died on its own at 15:07 CDT (20:07 UTC) while the board
stayed up. Not a lockup — the kernel, WiFi and SSH were all fine, and the board
went on running for another five hours.

What makes it worth keeping is the shape of the fault:

```
SIGSEGV: segmentation violation
PC=0xff520464 m=5 sigcode=1 addr=0xff520464
goroutine 0 gp=0x3405188 m=5 [idle]:
runtime: g 0 gp=0x3405188: unknown pc 0xff520464
```

and the kernel's view of the same event:

```
dashboard: unhandled page fault (11) at 0xff520464, code 0x80000005
[ff520464] *pgd=00000000
PC is at 0xff520464    LR is at 0x15510
CPU: 1 PID: 553 Comm: dashboard
```

Three things stand out. It faulted on **g0, the Go scheduler's own stack**,
inside `findRunnable`/`stealWork` — not in application code. `PC == addr` with
`code 0x80000005` is an *instruction fetch* from an address with no page table
entry (`*pgd=00000000`), i.e. control flow branched somewhere impossible, while
`LR` still held a valid address. And `0xff520464` sits in the SoC's device
register range, where a userspace PC has no business being.

That is the signature of a corrupted function pointer or return address, not a
logic bug. Hypothesis 4 in `lockups.md` (bad RAM) was refuted by memtester, but
memtester cannot reproduce GC write barriers racing DMA — which is exactly the
gap this crash lands in. It happened on **CPU 1**, the core the dwc2 interrupts
are pinned to.

One occurrence. Suggestive, not conclusive.

## `health-final-samples-2026-09-17.log`

The last 41 samples before the 2026-09-17T00:56:03Z lockup (19:56 CDT), ending
at the `UNCLEAN SHUTDOWN` marker the next boot wrote.

This is the first lockup captured with the kernel-side counters, and the point
of keeping it is that **everything is flat**. Over the 3h02m run `slab` moved 16
pages, `hi` 333→327, `sk` 44→45, `avail` 368MB→366MB. In the final sample
`ctxt` and `forks` are still climbing at their normal rates, so userspace was
scheduling and forking normally right up to the end.

`/sys/fs/pstore` was empty afterwards, on a board where ramoops is configured
and `panic_on_oops`, `softlockup_panic` and `hardlockup_panic` are all enabled —
so the kernel never panicked and its own lockup detectors never fired.

## `dashboard-crash-2026-09-17T2103Z.log`

The second unprompted dashboard death, 2026-09-17 16:03 CDT (21:03 UTC), on the
6.8-hour run — the longest the board has ever managed. The board itself stayed
up and healthy; only the process died. The panel held its last frame with the
clock frozen at 16:02, which is what a dead renderer looks like from the couch.

A different fault from the first one, and a more specific one:

```
runtime: bad span s.state=43 s.sweepgen=735325140 sweepgen=16110
fatal error: non in-use span in unswept list
```

This is Go's garbage collector finding its own heap metadata corrupted. A span
is the runtime's record for a run of heap pages. `state=43` is not a valid span
state at all (valid values are 0-3), and `sweepgen=735325140` against an
expected `16110` is not an off-by-one — it is a field holding garbage.

It was thrown from `runtime.sweepone` on the background sweeper while the main
goroutine was inside the rasteriser
(`vector.(*Rasterizer).rasterizeDstAlphaSrcOpaqueOpOver`, via omnidoc's
`raster.(*Device).PushClip`). So the corruption was found during GC of a heap
that a render was actively churning.

**Taken with the first crash, the pattern is the interesting part.** Two
unprompted deaths in two days, both memory corruption, neither in application
logic:

| | 2026-09-16 | 2026-09-17 |
|---|---|---|
| Fault | SIGSEGV, PC jumped to `0xff520464` | corrupted GC span metadata |
| Where | `g0`, scheduler stack, `findRunnable` | `sweepone`, background sweeper |
| Doing what | idle, looking for work | mid-render |
| Kernel noticed | yes — `unhandled page fault` | **no — nothing in dmesg** |

Both are the runtime's own structures being wrong, not application state. That
is what memory corruption looks like from inside a managed heap, and it is the
same class of event that produced the `bytes.Replace` SIGSEGV that hypothesis 4
in `../lockups.md` attributed to bad RAM before memtester cleared it.

The second one leaving **no kernel trace at all** is worth noting: `/sys/fs/pstore`
held only the routine `console-ramoops-0`, and `dmesg` had no fault. The
corruption happened without the MMU ever being asked for an invalid address.

## `dashboard-crash-2026-09-18T0737Z.log`

Third unprompted death, 2026-09-18 02:37 CDT (07:37 UTC), **10.5 hours** into
what became the longest board run on record. The board itself never faltered —
it was still up at 10.8h with every counter nominal.

```
fatal error: span has no free objects
runtime.(*mcentral).cacheSpan  mcentral.go:187
runtime.(*mcache).refill       mcache.go:205
runtime.(*mcache).nextFree     malloc.go:1006
runtime.mallocgc               malloc.go:1143
runtime.growslice              slice.go:265
  omnidoc/render.(*Path).Close
  omnidoc/layout/paint.fillRect
  omnidoc/layout/paint.paintBorder
```

A third distinct allocator invariant, broken in a third place: the allocator
took a span from the central free list and found it had no free objects, which
is a contradiction in terms — `cacheSpan` only selects spans that claim to have
some. Triggered by an ordinary `growslice` during border painting.

No kernel fault accompanied it (`dmesg` clean, pstore empty).

## The pattern across three crashes

| | 2026-09-16 | 2026-09-17 | 2026-09-18 |
|---|---|---|---|
| Fault | SIGSEGV, PC → `0xff520464` | `non in-use span in unswept list` | `span has no free objects` |
| Runtime area | scheduler (`findRunnable`) | sweeper (`sweepone`) | allocator (`cacheSpan`) |
| Doing what | idle | mid-render | mid-render |
| Kernel noticed | yes, page fault | no | no |
| Board survived | yes | yes | yes |

Three failures, three different runtime subsystems, three different invariants,
none in application logic. Every one is the Go runtime discovering that memory
it owns holds values it could not have written.

**What this is not.** It is not an omnidoc or dashboard bug: application code
does not touch span metadata or the scheduler's g0 stack, and the same binary
runs these paths millions of times between crashes. It is not a Go bug for the
same reason — these are the runtime's own consistency checks firing, which is
them working correctly. It is not the touch driver: the digitizer has been
unbound since 2026-09-17 and the touch goroutine is not even started (the log's
first line records `taps disabled`), yet the crashes continue.

**What it points at.** Something outside the process writes to its memory.
Hypothesis 4 in `../lockups.md` blamed bad RAM and was refuted by memtester —
but that document already records the caveat that memtester cannot reproduce GC
write barriers racing DMA, and all three crashes land in exactly that gap. The
board has a DMA-capable peripheral (dwc2) in a documented-misconfigured state,
running 8,000 interrupts/sec continuously.

**Relationship to the lockups is unresolved.** These kill a process and leave
the board healthy; the lockups kill the board. They may share a cause or be
unrelated. Worth noting that the two longest board runs on record (6.8h, 10.8h+)
both ended with a dashboard crash rather than a lockup.

## `dashboard-crash-2026-09-22T2001Z.log`

**The first crash on a second board.** Board 3, flashed that afternoon with the
same base-Lyra image and WiFi graft as the control board, running the same
binary and a byte-identical `config.json`. The process died between the
20:00:53Z and 20:01:24Z health samples, 1h45m after boot, with the board
otherwise healthy: WiFi up, SSH and adb answering, load 0.02, 409MB available.
The panel kept its last frame, which reads as a lockup from across the room.

```
fatal error: index out of range
runtime.(*pageAlloc).update(0x10da478, 0xdcdb0000, 0xf, 0x1, 0x1)
    mpagealloc.go:503
runtime.(*pageAlloc).allocRange / alloc / (*mheap).allocSpan
```

reached from `mallocgcLarge` in omnidoc's `blurShadowSurface` during a render.
The page allocator indexed its summary tables with a chunk address it does not
own: a fourth corrupted runtime invariant, after the g0 instruction fetch
(09-16), "non in-use span in unswept list" (09-17) and "span has no free
objects" (09-18). All four are the Go runtime finding its own metadata wrong,
in four different places, on two boards. That removes the board-defect
hypothesis for these crashes. What is left is memory being corrupted under a
correct process: the kernel or a driver writing where it should not, or the
runtime on this kernel and ARMv7 combination.
