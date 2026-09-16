#!/bin/sh
# WiFi-down soak: does the board still lock up with aic8800_fdrv unloaded?
#
# Every software hypothesis in docs/lockups.md has been refuted, leaving the
# kernel or a vendor driver. aic8800_fdrv is the prime suspect by elimination --
# WiFi is always the first thing to die. This removes it from memory entirely
# while keeping the dashboard's workload identical, so a clean multi-hour run
# implicates the driver and a lockup exonerates it. Both outcomes are
# informative, which is the property the useful experiments in that doc share.
#
# The workload is kept identical by serving a 4.5MB / 5000-event iCal feed from
# 127.0.0.1 -- same parse, same unfolding passes, same ~91MB VmHWM spike, same
# ~10s render. Only the network is gone. This mirrors test 2d, the experiment
# that cleanly separated data from transport.
#
# Usage:  soak-nowifi.sh start | stop | status
#
# stop() restores the original config and reloads WiFi. Run it when done.

CONF=/root/config.json
SAVED=/root/config.json.presoak
FEED=/root/soak-feed.ics
PORT=8088
STATE=/root/soak.state
SRVLOG=/root/soak-httpd.log

die() { echo "error: $*" >&2; exit 1; }

start() {
	[ -f "$STATE" ] && die "soak already running (see $STATE); stop it first"
	[ -f "$FEED" ] || die "feed not found: $FEED"
	[ -f "$CONF" ] || die "config not found: $CONF"

	# Save the real config before touching it. Never overwrite an existing
	# save: if a previous run died before stop(), that file is the only copy
	# of the user's Google credentials and config.
	[ -f "$SAVED" ] || cp "$CONF" "$SAVED"

	echo "starting loopback feed server on :$PORT"
	# --bind 127.0.0.1 so this is never reachable off the board, and cd to /root
	# so it serves the feed by name.
	cd /root || die "cannot cd /root"
	nohup python3 -m http.server "$PORT" --bind 127.0.0.1 >"$SRVLOG" 2>&1 &
	srvpid=$!
	sleep 2
	kill -0 "$srvpid" 2>/dev/null || die "feed server died; see $SRVLOG"

	# Confirm it actually serves before rewriting config -- a soak that fails
	# to fetch is a different experiment than the one intended.
	wget -q -O /dev/null "http://127.0.0.1:$PORT/soak-feed.ics" ||
		{ kill "$srvpid" 2>/dev/null; die "feed server not serving"; }
	echo "feed server ok (pid $srvpid)"

	# Point the dashboard at the loopback feed as an iCal source. Kind "ical"
	# with a URL is the pre-Google path and is still fully supported.
	python3 - "$CONF" "$PORT" <<-'PY' || die "config rewrite failed"
		import json, sys
		path, port = sys.argv[1], sys.argv[2]
		c = json.load(open(path))
		c["calendars"] = [{
		    "name": "Soak",
		    "color": "#4f9cff",
		    "kind": "ical",
		    "url": f"http://127.0.0.1:{port}/soak-feed.ics",
		}]
		json.dump(c, open(path, "w"), indent=1)
	PY
	echo "config now points at loopback feed"

	# Take WiFi down cleanly first. S99wlan0 stop kills wpa_supplicant,
	# hostapd and dnsmasq and downs the link -- without this, rmmod races a
	# driver that still has an associated interface.
	/etc/init.d/S99wlan0 stop

	# Unload the driver. aic8800_fdrv first (it holds aic_load_fw), then the
	# loader. aic_btusb is the Bluetooth half and is left alone: it is a
	# separate module on a separate USB interface and is not under suspicion.
	echo "unloading wifi driver"
	rmmod aic8800_fdrv 2>&1
	sleep 1
	rmmod aic_load_fw 2>&1
	sleep 1
	if lsmod | grep -q aic8800_fdrv; then
		echo "WARNING: aic8800_fdrv still loaded -- rmmod refused"
	else
		echo "aic8800_fdrv unloaded"
	fi

	echo "started $(date '+%Y-%m-%dT%H:%M:%S') srvpid=$srvpid" > "$STATE"

	# Deadline watchdog. Unloading a vendor driver is the risky step here, and
	# an unattended soak that wedges is a lockup with no one watching. This
	# restores WiFi and the real config after SOAK_HOURS regardless of what
	# else happens, so the board comes back on its own.
	#
	# It cannot rescue a genuine kernel lockup -- nothing in userspace can --
	# but it does mean a forgotten soak self-terminates rather than leaving
	# the board on a loopback feed with no radio indefinitely.
	#
	# The deadline is an absolute timestamp in $STATE, not a `sleep N` in a
	# background shell. The first version was the latter and it did NOT work:
	# the 2026-09-15 soak locked up at 1h48m, the sleeping shell died with the
	# board, and the reboot came back with the soak still "RUNNING" on the
	# loopback feed. A timer that dies with the thing it is timing is not a
	# watchdog. Two mechanisms replace it, either of which suffices:
	#
	#   1. a sleeper for the live case (the board never crashes), and
	#   2. expire_if_due, called from `status` and at boot, for the case the
	#      sleeper did not survive.
	hours="${SOAK_HOURS:-8}"
	deadline=$(( $(date +%s) + hours * 3600 ))
	echo "deadline=$deadline" >> "$STATE"

	nohup sh -c "sleep $((hours * 3600)); '$0' expire" \
		>/root/soak-watchdog.log 2>&1 &
	echo "watchdog: auto-stop in ${hours}h (deadline recorded in $STATE)"

	# Restart the dashboard so it picks up the new config and the new binary
	# state. Deploying a binary does not update a running process.
	/etc/init.d/S99zdashboard restart

	echo
	echo "soak running. the board has NO WiFi -- reach it over adb only."
	echo "  watch:   adb shell tail -f /root/health.log"
	echo "  finish:  adb shell /root/soak-nowifi.sh stop"
}

stop() {
	echo "restoring wifi driver"
	modprobe aic_load_fw 2>&1
	sleep 1
	modprobe aic8800_fdrv 2>&1
	sleep 2
	/etc/init.d/S99wlan0 start

	if [ -f "$SAVED" ]; then
		cp "$SAVED" "$CONF" && rm -f "$SAVED"
		echo "config restored"
	else
		echo "WARNING: no saved config at $SAVED -- $CONF left as-is"
	fi

	# Kill the feed server by the pid recorded at start, rather than
	# `killall python3` -- the dashboard's own tooling may use python and a
	# blanket kill would take it out too.
	srvpid=$(sed -n 's/.*srvpid=\([0-9]*\).*/\1/p' "$STATE" 2>/dev/null)
	[ -n "$srvpid" ] && kill "$srvpid" 2>/dev/null
	rm -f "$STATE"

	/etc/init.d/S99zdashboard restart
	echo "soak stopped; wifi and config restored"
}

# Stop the soak if its deadline has passed. Safe to call any time: it is a
# no-op when no soak is running or the deadline is still in the future.
#
# This is what makes the watchdog survive a lockup. The board reboots into a
# soak that is still nominally running -- config pointing at the loopback feed,
# WiFi driver reloaded by S03modules_init.sh at boot but $STATE still present.
# Called from S98health at boot, this notices the deadline passed while the
# board was down and unwinds the soak instead of leaving it half-applied.
expire_if_due() {
	[ -f "$STATE" ] || return 0
	deadline=$(sed -n 's/^deadline=\([0-9]*\)/\1/p' "$STATE" 2>/dev/null)
	[ -n "$deadline" ] || return 0
	now=$(date +%s)
	# The board has no RTC and boots at 1970, so `date +%s` is ~0 until
	# S99wlan0 runs rdate. Treat an obviously-unset clock as "cannot judge"
	# rather than "deadline is in the future" -- otherwise a soak started
	# before a reboot would never expire.
	[ "$now" -lt 1000000000 ] && { echo "clock unset; cannot judge deadline"; return 0; }
	if [ "$now" -ge "$deadline" ]; then
		echo "soak deadline passed ($(($((now - deadline)) / 60))m ago) -- stopping"
		stop
	fi
	return 0
}

status() {
	expire_if_due
	if [ -f "$STATE" ]; then
		echo "soak: RUNNING -- $(cat "$STATE")"
	else
		echo "soak: not running"
	fi
	echo "wifi driver: $(lsmod | grep -q aic8800_fdrv && echo loaded || echo UNLOADED)"
	echo "wlan0: $(cat /sys/class/net/wlan0/operstate 2>/dev/null || echo absent)"
	# No pgrep on this busybox -- ps|grep is what is available.
	echo "feed server: $(ps 2>/dev/null | grep -q '[h]ttp.server' && echo up || echo down)"
	echo "dashboard pid: $(pidof dashboard 2>/dev/null)"
	echo "config source: $(grep -o '"kind": "[a-z]*"' "$CONF" 2>/dev/null | head -1)"
	echo "--- last health sample:"
	tail -1 /root/health.log 2>/dev/null
}

case "${1:-}" in
start) start ;;
stop) stop ;;
status) status ;;
expire) expire_if_due ;;
*) die "usage: $0 start|stop|status|expire" ;;
esac
