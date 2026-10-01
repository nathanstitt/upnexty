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

## `dashboard-crash-2026-09-22T2218Z.log`

Fifth unprompted death, on the control board (base-Lyra image), 4h04m after
boot, between the 22:18:28Z and 22:18:58Z health samples. The board stayed up:
WiFi, SSH and the sampler all fine, load 0.02, 409MB available. The panel
froze on its last frame, which is what made it read as a lockup from across
the room. The dashboard was restarted at 22:33Z and the control soak continues.

```
runtime: bad span s.state=43 s.sweepgen=734276596 sweepgen=19078
fatal error: non in-use span in unswept list
```

Same fault, same place (`sweepone` on the background sweeper) as 2026-09-17,
and the garbage has the same shape. The two corrupt `sweepgen` values are
`0x2bd42bd4` (09-17) and `0x2bc42bf4` (09-22), and `state=43` is `0x2b` both
times. Every other byte of the overwrite is `0x2b`: the span header was
covered by a stream of 16-bit words with a constant high byte, not by random
bit flips. That is the shape of data landing where it should not, and it is
the first repeated signature across two crashes. Whatever produces 16-bit
values with a `0x2b` high byte is worth identifying.

`dmesg` also shows two `rockchip-otp ff4f0000.otp: ecc check error during read
setup` lines at 422s and 440s uptime, hours before the crash. Unexplained;
noted in case it recurs.

## `dashboard-crash-2026-09-23T1135Z.log`

Sixth unprompted death, and the first on the **stock Zero W image** (board 3,
hypothesis 13 in `../lockups.md`), 13h01m after the 22:34Z power cycle. The
board stayed up: WiFi, SSH, sampler all fine, 387MB available. Panel frozen
with the clock at 06:35 CDT, which is 11:35Z, matching the last live sample.

```
runtime: pointer 0x5549138 to unused region of span span.base()=0x3dc4000 span.limit=0x3dc7f00 span.state=1
fatal error: found bad pointer in Go heap (incorrect use of unsafe or cgo?)
```

Thrown from `findObject` inside `scanObjectsSmall` (the Green Tea GC mark
path, `mgcmark_greenteagc.go`) during a GC assist, while goroutine 1 was in
`textlayout`'s glyph outline reader under omnidoc's font code. A fifth distinct
runtime invariant, in a fifth place. The pointer is nowhere near the span the
runtime attributes it to, so the span lookup table itself is what is wrong.

**This removes the firmware image from the crash question.** Six crashes, two
boards, two images, one binary. What is common to all of them is the Go 1.26.3
runtime on `linux/arm`, and every fault is in its GC or allocator metadata.

## `dashboard-crash-2026-09-23T1638Z.log`

Seventh unprompted death, control board (base-Lyra image, Go 1.26.3, binary
`a195311`), between the 16:37:56Z and 16:38:26Z samples, 1h10m after its
15:27Z restart. Board fine: SSH, WiFi, sampler all up, load 0.00, pstore empty.

```
runtime: pointer 0x39a290e to unallocated span span.base()=0x3978000 span.limit=0x39a6000 span.state=0
fatal error: found bad pointer in Go heap (incorrect use of unsafe or cgo?)
```

Same fault as board 3's crash five hours earlier, and the same shape all the
way down: Green Tea's `scanObjectsSmall` finds a bad pointer during a GC
assist, while goroutine 1 is inside `textlayout`'s TrueType parser
(`parseGPOSPairSet` here, `buildSegments` on board 3). Two boards, two
images, one toolchain, the same two frames at the top.

Board 3 has been on the Go 1.25.14 build since 14:50Z (hypothesis 16 in
`../lockups.md`); this crash is the control side of that comparison.

## `dashboard-crash-2026-09-23T2038Z.log`

Eighth unprompted death, control board (base-Lyra, Go 1.26.3), between the
20:37:46Z and 20:38:16Z samples, 1h47m after its 18:51Z restart. Board fine,
pstore empty.

```
runtime: marked free object in span 0xa6f8c7c8, elemsize=64 freeindex=0
fatal error: found pointer to free object
```

The background sweeper's zombie check: an object the allocator had freed was
found marked by the collector. A sixth distinct runtime invariant. Goroutine 1
was in `fmt.Sprintf` under the view template's icon helper at the time, which
is as ordinary as code gets.

**The tally on Go 1.26.3 now stands at eight crashes in eight days**, across
two boards and two images, six different invariants, every one in the GC or
allocator. Board 3 on Go 1.25.14 passed 23 hours crash-free at the time of
this entry, against a longest 1.26.3 run of 13 hours.
