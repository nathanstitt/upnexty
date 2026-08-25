#!/bin/bash
# Report what the board is and what it's doing. Read-only; safe any time.

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

mode="$(board_usb_mode || true)"

if [ -n "$mode" ]; then
	# In Maskrom/Loader there's no Linux to ask, so report what the ROM exposes.
	info "USB mode: $mode (Linux is not running)"
	query_upgrade_tool LD | tail -2
	if [ "$mode" = "Loader" ]; then
		# Flash is only reachable once a loader is resident, i.e. not in Maskrom.
		echo
		info "flash"
		query_upgrade_tool RFI 2>/dev/null | sed -n '2,9p'
		query_upgrade_tool PL 2>/dev/null | tail -5
	fi
	echo
	echo "to boot Linux: power-cycle without holding BOOT"
	exit 0
fi

require_adb

info "system"
adb shell 'printf "  %-12s %s\n" model "$(cat /proc/device-tree/model 2>/dev/null | tr -d "\0")"
	printf "  %-12s %s\n" kernel "$(uname -r)"
	printf "  %-12s %s\n" arch "$(uname -m)"
	printf "  %-12s %s\n" distro "$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")"
	printf "  %-12s %s\n" uptime "$(cut -d. -f1 /proc/uptime)s"'

echo
info "memory / storage"
adb shell 'free -m | sed -n "2p" | awk "{printf \"  %-12s %sMB total, %sMB free\n\", \"ram\", \$2, \$4}"
	df -h / | sed -n "2p" | awk "{printf \"  %-12s %s total, %s free (%s used)\n\", \"rootfs\", \$2, \$4, \$5}"'

echo
info "display"
adb shell 'printf "  %-12s %s\n" fb "$(cat /sys/class/graphics/fb0/virtual_size 2>/dev/null | tr , x)"
	printf "  %-12s %s\n" mode "$(sed -n "s/.*Display mode: //p" /sys/kernel/debug/dri/0/summary 2>/dev/null)"
	printf "  %-12s %s\n" connector "$(cat /sys/class/drm/card0-DSI-1/status 2>/dev/null)"
	for bl in /sys/class/backlight/*/; do
		[ -d "$bl" ] || continue
		printf "  %-12s %s/%s\n" "$(basename $bl)" "$(cat $bl/brightness)" "$(cat $bl/max_brightness)"
	done'

echo
info "input"
adb shell 'grep -A4 "^N: Name" /proc/bus/input/devices | sed -n "s/^N: Name=\"\(.*\)\"/  \1/p"'
