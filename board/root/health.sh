#!/bin/sh
# Health sampler for post-mortem of a lockup.
#
# Writes to /root (UBI, persistent). NOT /tmp, /var/log or /run -- those are
# tmpfs and vanish on the reboot that follows a hang, which is exactly the
# evidence a hang destroys. The 2026-09-14 lockup left nothing to read for
# this reason.
#
# Appends one line per sample and fsyncs by closing the file each write, so a
# hard power cycle loses at most the current sample rather than the buffer.
LOG=/root/health.log
INTERVAL="${HEALTH_INTERVAL:-30}"
# Cap the log so this cannot fill a 90MB rootfs: rotate at ~2MB, keep one
# previous generation. At ~150 bytes a sample every 30s that is several days.
MAXBYTES="${HEALTH_MAXBYTES:-2000000}"

pid_of_dashboard() {
  for p in /proc/[0-9]*; do
    [ -r "$p/comm" ] || continue
    case "$(cat "$p/comm" 2>/dev/null)" in dashboard) echo "${p#/proc/}"; return;; esac
  done
}

rotate_if_big() {
  [ -f "$LOG" ] || return 0
  sz=$(wc -c < "$LOG" 2>/dev/null || echo 0)
  [ "$sz" -gt "$MAXBYTES" ] && mv "$LOG" "$LOG.1"
  return 0
}

sample() {
  # The board has no RTC: it boots at 1970 until S99wlan0 runs rdate, so early
  # samples carry a useless wall clock. up= (seconds since boot) is always
  # meaningful and is what to order by; the date is for correlating with
  # events off the board once the clock is set.
  now=$(date '+%Y-%m-%dT%H:%M:%S')
  case "$now" in 1970-*) now="$now(preclock)";; esac
  up=$(cut -d' ' -f1 /proc/uptime)
  # MemAvailable is the number that matters -- "free" excludes reclaimable
  # cache and reads alarmingly low on a healthy board.
  avail=$(awk '/MemAvailable/{print $2}' /proc/meminfo)
  memfree=$(awk '/^MemFree/{print $2}' /proc/meminfo)
  load=$(cut -d' ' -f1-3 /proc/loadavg)
  pid=$(pid_of_dashboard)
  if [ -n "$pid" ]; then
    rss=$(awk '/VmRSS/{print $2}' "/proc/$pid/status" 2>/dev/null)
    hwm=$(awk '/VmHWM/{print $2}' "/proc/$pid/status" 2>/dev/null)
    # Threads: a goroutine leak shows up here as a climbing thread count.
    thr=$(awk '/Threads/{print $2}' "/proc/$pid/status" 2>/dev/null)
    fds=$(ls "/proc/$pid/fd" 2>/dev/null | wc -l)
  else
    rss=-1; hwm=-1; thr=-1; fds=-1
  fi
  # Is the process actually still working? CPU time (utime+stime, jiffies) is
  # the signal that survives here: /proc/pid/io does not exist on this kernel,
  # and /dev/fb0 is a device node so its mtime is the node's, not the last
  # write -- that check read a constant and would never have fired.
  #
  # A rendering dashboard burns ~10s of CPU a frame; a hung one burns none. A
  # cpu= that stops climbing while pid= stays the same IS the 12:17 symptom,
  # and is what distinguishes a wedged process from a dead one.
  if [ -n "$pid" ]; then
    cpu=$(awk '{print $14+$15}' "/proc/$pid/stat" 2>/dev/null)
  else
    cpu=-1
  fi
  link=$(cat /sys/class/net/wlan0/operstate 2>/dev/null || echo "-")
  # Whether the WiFi driver is actually resident, not just whether the
  # interface is up. wlan0= alone cannot answer this: `ip link set wlan0 down`
  # and `rmmod aic8800_fdrv` both leave wlan0= reading "-", so a log showing
  # "-" proves nothing about the module. During the 2026-09-15 soak that
  # distinction was the whole question -- whether the board died with the
  # driver unloaded -- and it had to be reconstructed indirectly from slab=
  # (the driver is worth ~5000 pages, so its absence is visible there). Record
  # it directly instead of making the next reader infer it.
  wdrv=$(grep -q '^aic8800_fdrv ' /proc/modules 2>/dev/null && echo y || echo n)
  # Kernel-side counters. Every sample up to 2026-09-15 watched userspace only,
  # which is why each captured lockup ends on a "completely healthy" line: the
  # failure is below the application and nothing here could see it.
  #
  # /proc/slabinfo does not exist on this kernel (CONFIG_SLUB_DEBUG is off), so
  # slab is read from vmstat's nr_slab_* instead -- same numbers, in pages.
  #
  # slab= reclaimable+unreclaimable pages. A kernel memory leak in a vendor
  # driver shows here and NOT in MemAvailable until it is far too late.
  slab=$(awk '/^nr_slab_reclaimable/{r=$2} /^nr_slab_unreclaimable/{u=$2} END{print r+u}' /proc/vmstat 2>/dev/null)
  # Highest-order free block in buddyinfo. Order 0 is 4KB, so the last column
  # is 4MB blocks. Fragmentation collapse -- this tail draining to 0 -- makes
  # high-order allocations fail while MemAvailable still looks fine, which is
  # exactly the shape of a healthy-looking board that cannot fork.
  hi=$(awk '/zone[ \t]+Normal/{print $NF}' /proc/buddyinfo 2>/dev/null)
  # Sockets and TCP memory: a socket-buffer leak in the WiFi driver would
  # accumulate here. sk= total sockets, tcpm= TCP pages charged.
  sk=$(awk '/^sockets:/{print $3}' /proc/net/sockstat 2>/dev/null)
  tcpm=$(awk '/^TCP:/{print $NF}' /proc/net/sockstat 2>/dev/null)
  # Interrupt totals for the two drivers under suspicion, summed across CPUs.
  # dwc2 (USB gadget) keeps answering adb after userspace is gone; i2c is the
  # permanent touch-controller storm. A rate that departs from its baseline in
  # the samples before a freeze is the thing worth catching.
  irqs=$(awk '/dwc2_hsotg/{d=0; for(i=2;i<=NF-2;i++) d+=$i} /ff060000\.i2c/{c=0; for(i=2;i<=NF-2;i++) c+=$i} END{printf "%d,%d", d, c}' /proc/interrupts 2>/dev/null)
  # Context switches and forks since boot. The signature lockup is "userspace
  # cannot fork while the kernel still services USB" -- if that state persists
  # for even one sample, ctxt/fork stop advancing while dwc2 keeps climbing.
  ctxt=$(awk '/^ctxt/{print $2}' /proc/stat 2>/dev/null)
  forks=$(awk '/^processes/{print $2}' /proc/stat 2>/dev/null)
  # Heartbeat: the last time we know the board was alive. The next boot reads
  # this to report when an unclean shutdown happened.
  # Write via a temp file and rename: `>` truncates first, so a board that dies
  # in that window leaves an EMPTY lastseen and the next boot reports an
  # unclean shutdown with no time on it. Observed 2026-09-14. rename(2) is
  # atomic, so the file is always either the old stamp or the new one.
  echo "$now" > /root/health.lastseen.tmp && mv /root/health.lastseen.tmp /root/health.lastseen
  echo "$now up=$up avail=${avail}kB free=${memfree}kB load=$load pid=${pid:--} rss=${rss}kB hwm=${hwm}kB thr=$thr fd=$fds cpu=${cpu}j wlan0=$link wdrv=$wdrv slab=${slab}p hi=$hi sk=$sk tcpm=$tcpm irq=$irqs ctxt=$ctxt forks=$forks" >> "$LOG"
}

rotate_if_big
echo "=== health.sh start $(date '+%Y-%m-%dT%H:%M:%S') interval=${INTERVAL}s ===" >> "$LOG"
while :; do
  sample
  sleep "$INTERVAL"
done
