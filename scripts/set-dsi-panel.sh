#!/bin/sh
# Set the DSI panel timings in the Luckfox Lyra boot partition's device tree.
#
# Why this exists: luckfox-config knows the timings for every Waveshare panel,
# but its loader never applies them. In luckfox_load_cfg it reads DSI_SIZE into
# the variable dsi_type (a copy/paste of the DSI_TYPE line above it), so dsi_size
# is always empty, the `[ -n "$dsi_size" ]` guard fails, and luckfox_dsi_app is
# never called. Setting DSI_TYPE/DSI_SIZE in /etc/luckfox.cfg therefore has no
# effect. This script performs the writes that luckfox_dsi_app would have made.
#
# Run it on the board, as root, after any update.img reflash. Reboot to apply.
#
#   ./set-dsi-panel.sh            # default panel below
#   ./set-dsi-panel.sh 7_0        # another luckfox-config size key
#   ./set-dsi-panel.sh --show     # print the timings currently in flash
#
# Adding a panel: copy the values from the matching `dsi_size` branch of
# luckfox_dsi_app in /usr/bin/luckfox-config.

set -e

PANEL="${1:-8_8}"

# Waveshare 8.8" (480x1920, OTA7290B) -- the panel this board ships with.
set_timings_8_8() {
	lanes=2
	clock_frequency=83333000
	hactive=480
	vactive=1920
	vsync_len=20
	vback_porch=20
	vfront_porch=20
	hsync_len=50
	hback_porch=50
	hfront_porch=50
}

# Waveshare 7.0" (720x1280) -- second entry to show the shape; unverified here.
set_timings_7_0() {
	lanes=2
	clock_frequency=84000000
	hactive=720
	vactive=1280
	vsync_len=30
	vback_porch=20
	vfront_porch=2
	hsync_len=50
	hback_porch=239
	hfront_porch=33
}

# Stock Lyra default (800x1280) -- use to undo this script.
set_timings_default() {
	lanes=4
	clock_frequency=70000000
	hactive=800
	vactive=1280
	vsync_len=4
	vback_porch=10
	vfront_porch=30
	hsync_len=20
	hback_porch=20
	hfront_porch=40
}

die() {
	echo "error: $*" >&2
	exit 1
}

# Match luckfox-config's own storage detection.
detect_media() {
	if [ -e /dev/mtdblock2 ]; then
		echo /dev/mtdblock1
	elif [ -e /dev/mmcblk0p2 ]; then
		echo /dev/mmcblk0p2
	else
		die "no SPI NAND or eMMC boot partition found"
	fi
}

PANEL_NODE=/dsi@ff640000/panel@0
TIMING_NODE="$PANEL_NODE/display-timings/timing0"

HDR=/tmp/.dsi_hdr.dtb
CNT=/tmp/.dsi_content.bin
DTB=/tmp/.dsi_fdt.dtb

# Pull the fdt header, the resource entry, and rk-kernel.dtb out of the boot
# partition. Offsets come from the FIT header, not assumptions about layout.
read_fdt() {
	dd if="$MEDIA" of="$HDR" bs=1 count=2048 2>/dev/null
	POS="$(fdtget "$HDR" /images/resource data-position)" ||
		die "cannot read resource data-position (is $MEDIA the boot partition?)"
	dd if="$MEDIA" of="$CNT" bs=1 skip=$((POS + 512)) count=512 2>/dev/null
	# rk-kernel.dtb size is a little-endian u32 at 0x108 of the resource entry.
	size_le="$(xxd -s 0x108 -l 4 -p "$CNT")"
	DTB_SIZE=$((0x$(echo "$size_le" | awk '{print substr($1,7,2) substr($1,5,2) substr($1,3,2) substr($1,1,2)}')))
	[ "$DTB_SIZE" -gt 0 ] || die "bad dtb size in resource entry"
	dd if="$MEDIA" of="$DTB" bs=1 skip=$((POS + 2048)) count="$DTB_SIZE" 2>/dev/null
	[ "$(xxd -l 4 -p "$DTB")" = "d00dfeed" ] || die "extracted dtb has no FDT magic"
}

# Write the dtb back and fix up the resource entry's sha1 and size, the way
# luckfox_fdt_overlay does. Without this U-Boot rejects the resource image.
write_fdt() {
	new_size="$(wc -c <"$DTB" | tr -d ' ')"
	sha1="$(sha1sum "$DTB" | awk '{print $1}')"
	printf "%s" "$sha1" | xxd -r -p |
		dd of="$CNT" bs=1 seek=$((0xe0)) conv=notrunc 2>/dev/null
	hex="$(printf '%06X' "$new_size")"
	rev="$(echo "$hex" | sed 's/../& /g' | awk '{for (i=NF; i>=1; i--) printf $i}')"
	printf "%s" "$rev" | xxd -r -p |
		dd of="$CNT" bs=1 seek=$((0x108)) conv=notrunc 2>/dev/null
	dd if="$CNT" of="$MEDIA" bs=1 seek=$((POS + 512)) count=512 2>/dev/null
	dd if="$DTB" of="$MEDIA" bs=1 seek=$((POS + 2048)) count="$new_size" 2>/dev/null
	sync
}

show_current() {
	echo "media: $MEDIA   resource at: $POS   dtb: $DTB_SIZE bytes"
	printf '  %-16s %s\n' "dsi,lanes" "$(fdtget "$DTB" "$PANEL_NODE" dsi,lanes)"
	for p in clock-frequency hactive vactive vsync-len vback-porch vfront-porch \
		hsync-len hback-porch hfront-porch; do
		printf '  %-16s %s\n' "$p" "$(fdtget "$DTB" "$TIMING_NODE" "$p")"
	done
}

for tool in fdtget fdtput sha1sum xxd dd; do
	command -v "$tool" >/dev/null 2>&1 || die "missing required tool: $tool"
done

MEDIA="$(detect_media)"
read_fdt

if [ "$PANEL" = "--show" ] || [ "$PANEL" = "-s" ]; then
	show_current
	exit 0
fi

case "$PANEL" in
8_8) set_timings_8_8 ;;
7_0) set_timings_7_0 ;;
default) set_timings_default ;;
*) die "unknown panel '$PANEL' (known: 8_8, 7_0, default)" ;;
esac

# Skip the write when flash already matches, so this is safe to run every boot.
if [ "$(fdtget "$DTB" "$TIMING_NODE" hactive)" = "$hactive" ] &&
	[ "$(fdtget "$DTB" "$TIMING_NODE" vactive)" = "$vactive" ] &&
	[ "$(fdtget "$DTB" "$PANEL_NODE" dsi,lanes)" = "$lanes" ]; then
	echo "panel $PANEL already set (${hactive}x${vactive}, ${lanes} lanes) -- nothing to do"
	exit 0
fi

echo "setting panel $PANEL: ${hactive}x${vactive} @ ${clock_frequency}Hz, ${lanes} lanes"

fdtput "$DTB" "$PANEL_NODE" dsi,lanes "$lanes"
fdtput "$DTB" "$TIMING_NODE" clock-frequency "$clock_frequency"
fdtput "$DTB" "$TIMING_NODE" hactive "$hactive"
fdtput "$DTB" "$TIMING_NODE" vactive "$vactive"
fdtput "$DTB" "$TIMING_NODE" vsync-len "$vsync_len"
fdtput "$DTB" "$TIMING_NODE" vback-porch "$vback_porch"
fdtput "$DTB" "$TIMING_NODE" vfront-porch "$vfront_porch"
fdtput "$DTB" "$TIMING_NODE" hsync-len "$hsync_len"
fdtput "$DTB" "$TIMING_NODE" hback-porch "$hback_porch"
fdtput "$DTB" "$TIMING_NODE" hfront-porch "$hfront_porch"
fdtput "$DTB" "$TIMING_NODE" vsync-active 0
fdtput "$DTB" "$TIMING_NODE" hsync-active 0
fdtput "$DTB" "$TIMING_NODE" de-active 0
fdtput "$DTB" "$TIMING_NODE" pixelclk-active 0

write_fdt

# Confirm against flash, not the staged copy -- a silent dd failure would
# otherwise look like success.
read_fdt
got_h="$(fdtget "$DTB" "$TIMING_NODE" hactive)"
got_v="$(fdtget "$DTB" "$TIMING_NODE" vactive)"
[ "$got_h" = "$hactive" ] && [ "$got_v" = "$vactive" ] ||
	die "verify failed: flash reads ${got_h}x${got_v}, wanted ${hactive}x${vactive}"

echo "written and verified in flash. reboot to apply."
