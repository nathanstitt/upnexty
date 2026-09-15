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
  # Heartbeat: the last time we know the board was alive. The next boot reads
  # this to report when an unclean shutdown happened.
  # Write via a temp file and rename: `>` truncates first, so a board that dies
  # in that window leaves an EMPTY lastseen and the next boot reports an
  # unclean shutdown with no time on it. Observed 2026-09-14. rename(2) is
  # atomic, so the file is always either the old stamp or the new one.
  echo "$now" > /root/health.lastseen.tmp && mv /root/health.lastseen.tmp /root/health.lastseen
  echo "$now up=$up avail=${avail}kB free=${memfree}kB load=$load pid=${pid:--} rss=${rss}kB hwm=${hwm}kB thr=$thr fd=$fds cpu=${cpu}j wlan0=$link" >> "$LOG"
}

rotate_if_big
echo "=== health.sh start $(date '+%Y-%m-%dT%H:%M:%S') interval=${INTERVAL}s ===" >> "$LOG"
while :; do
  sample
  sleep "$INTERVAL"
done
