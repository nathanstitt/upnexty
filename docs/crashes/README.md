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
