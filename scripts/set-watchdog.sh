#!/bin/sh
# Enable the RK3506's hardware watchdog in the Luckfox Lyra boot partition's
# device tree.
#
# Why this exists: the SoC has two Synopsys watchdog timers and the kernel has
# the dw_wdt driver compiled in, but Luckfox's device tree carries no node for
# either, so /dev/watchdog never appears. This adds WDT0 the way rk3502.dtsi
# in the vendor kernel declares it, using the same in-place fdtput path as
# set-dsi-panel.sh (offsets from the FIT header, resource entry sha1 and size
# recomputed, verified against flash afterwards).
#
# Once the node is in flash and the board has rebooted, S20watchdog feeds
# /dev/watchdog. A board that locks up stops feeding it and the timer resets
# the SoC. See CLAUDE.md, "Watchdog".
#
# Run it on the board, as root. Reboot to apply.
#
#   ./set-watchdog.sh            # add the node
#   ./set-watchdog.sh remove     # take it back out
#   ./set-watchdog.sh --show     # what is in flash now
#
# The dtb sits inside the resource image directly in front of logo.bmp, with
# a few hundred bytes of slack. Adding the node grows it by ~170 bytes. The
# script measures the gap from the resource table and refuses to overrun the
# next file rather than corrupting the boot logo silently.

set -e

ACTION="${1:-apply}"

# WDT0, from rk3502.dtsi in the vendor kernel (rk3506.dtsi includes it):
#   reg        0xff260000 0x100
#   clocks     TCLK_WDT0 (84), PCLK_WDT0 (83)  -- rockchip,rk3506-cru.h
#   interrupts GIC_SPI 107 IRQ_TYPE_LEVEL_HIGH   -- <0 107 4>
NODE=/watchdog@ff260000
WDT_REG=0xff260000
WDT_REG_SIZE=0x100
TCLK_ID=0x54
PCLK_ID=0x53
IRQ=0x6b

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

HDR=/tmp/.wdt_hdr.dtb
CNT=/tmp/.wdt_content.bin
NXT=/tmp/.wdt_next.bin
DTB=/tmp/.wdt_fdt.dtb

le32() {
	# little-endian hex string -> decimal
	echo $((0x$(echo "$1" | awk '{print substr($1,7,2) substr($1,5,2) substr($1,3,2) substr($1,1,2)}')))
}

# Pull the fdt header, the resource entry, and rk-kernel.dtb out of the boot
# partition. Offsets come from the FIT header, not assumptions about layout.
read_fdt() {
	dd if="$MEDIA" of="$HDR" bs=1 count=2048 2>/dev/null
	POS="$(fdtget "$HDR" /images/resource data-position)" ||
		die "cannot read resource data-position (is $MEDIA the boot partition?)"
	dd if="$MEDIA" of="$CNT" bs=1 skip=$((POS + 512)) count=512 2>/dev/null
	[ "$(dd if="$CNT" bs=1 count=4 2>/dev/null)" = "ENTR" ] || die "resource entry 1 has no ENTR magic"
	# rk-kernel.dtb offset (in 512-byte blocks) and size, little-endian u32s
	# at 0x104 and 0x108 of the resource entry.
	DTB_OFF=$(($(le32 "$(xxd -s 0x104 -l 4 -p "$CNT")") * 512))
	DTB_SIZE=$(le32 "$(xxd -s 0x108 -l 4 -p "$CNT")")
	[ "$DTB_SIZE" -gt 0 ] || die "bad dtb size in resource entry"
	# The next entry's offset bounds how far the dtb may grow.
	dd if="$MEDIA" of="$NXT" bs=1 skip=$((POS + 1024)) count=512 2>/dev/null
	if [ "$(dd if="$NXT" bs=1 count=4 2>/dev/null)" = "ENTR" ]; then
		NEXT_OFF=$(($(le32 "$(xxd -s 0x104 -l 4 -p "$NXT")") * 512))
	else
		NEXT_OFF=$(fdtget "$HDR" /images/resource data-size)
	fi
	DTB_MAX=$((NEXT_OFF - DTB_OFF))
	dd if="$MEDIA" of="$DTB" bs=1 skip=$((POS + DTB_OFF)) count="$DTB_SIZE" 2>/dev/null
	[ "$(xxd -l 4 -p "$DTB")" = "d00dfeed" ] || die "extracted dtb has no FDT magic"
}

# Write the dtb back and fix up the resource entry's sha1 and size, the way
# luckfox_fdt_overlay does. Without this U-Boot rejects the resource image.
write_fdt() {
	new_size="$(wc -c <"$DTB" | tr -d ' ')"
	[ "$new_size" -le "$DTB_MAX" ] ||
		die "dtb would be $new_size bytes but only $DTB_MAX fit before the next resource file; not writing"
	sha1="$(sha1sum "$DTB" | awk '{print $1}')"
	printf "%s" "$sha1" | xxd -r -p |
		dd of="$CNT" bs=1 seek=$((0xe0)) conv=notrunc 2>/dev/null
	hex="$(printf '%08X' "$new_size")"
	rev="$(echo "$hex" | sed 's/../& /g' | awk '{for (i=NF; i>=1; i--) printf $i}')"
	printf "%s" "$rev" | xxd -r -p |
		dd of="$CNT" bs=1 seek=$((0x108)) conv=notrunc 2>/dev/null
	# conv=notrunc: a no-op on the block device, but without it dd truncates
	# a regular file at the end of the write, which is how the host dry run
	# (WDT_MEDIA) lost everything after the dtb.
	dd if="$CNT" of="$MEDIA" bs=1 seek=$((POS + 512)) count=512 conv=notrunc 2>/dev/null
	dd if="$DTB" of="$MEDIA" bs=1 seek=$((POS + DTB_OFF)) count="$new_size" conv=notrunc 2>/dev/null
	sync
}

# The clock controller's phandle, found by compatible rather than assumed:
# the node's name and phandle are build details of the vendor tree.
find_cru() {
	for n in $(fdtget -l "$DTB" /); do
		case "$(fdtget "$DTB" "/$n" compatible 2>/dev/null)" in
		*rk3506-cru*)
			# fdtget prints the phandle in decimal; fdtput -t x reads hex.
			printf '0x%x\n' "$(fdtget "$DTB" "/$n" phandle)"
			return 0
			;;
		esac
	done
	die "no rockchip,rk3506-cru node in the device tree"
}

has_node() {
	fdtget "$DTB" "$NODE" compatible >/dev/null 2>&1
}

show_current() {
	echo "media: $MEDIA   resource at: $POS   dtb: $DTB_SIZE bytes (room for $DTB_MAX)"
	if has_node; then
		echo "watchdog node: present"
		for p in compatible reg clocks clock-names interrupts status; do
			printf '  %-12s %s\n' "$p" "$(fdtget "$DTB" "$NODE" "$p" 2>/dev/null)"
		done
	else
		echo "watchdog node: absent"
	fi
	if [ -e /dev/watchdog ]; then
		echo "running kernel: /dev/watchdog present"
	else
		echo "running kernel: no /dev/watchdog (reboot after applying)"
	fi
}

for tool in fdtget fdtput sha1sum xxd dd; do
	command -v "$tool" >/dev/null 2>&1 || die "missing required tool: $tool"
done

# WDT_MEDIA overrides the boot partition, so the dtb editing can be exercised
# against a copy of boot.img on a host without touching a board.
MEDIA="${WDT_MEDIA:-$(detect_media)}"
read_fdt

case "$ACTION" in
--show | -s)
	show_current
	exit 0
	;;
apply)
	if has_node; then
		echo "watchdog node already present -- nothing to do"
		exit 0
	fi
	cru="$(find_cru)"
	echo "adding $NODE (cru phandle $cru)"
	fdtput -c "$DTB" "$NODE"
	fdtput -t s "$DTB" "$NODE" compatible snps,dw-wdt
	fdtput -t x "$DTB" "$NODE" reg "$WDT_REG" "$WDT_REG_SIZE"
	fdtput -t x "$DTB" "$NODE" clocks "$cru" "$TCLK_ID" "$cru" "$PCLK_ID"
	fdtput -t s "$DTB" "$NODE" clock-names tclk pclk
	fdtput -t x "$DTB" "$NODE" interrupts 0x0 "$IRQ" 0x4
	fdtput -t s "$DTB" "$NODE" status okay
	write_fdt
	read_fdt
	has_node || die "verify failed: node not found in flash after write"
	[ "$(fdtget "$DTB" "$NODE" status)" = "okay" ] || die "verify failed: node status is not okay"
	echo "written and verified in flash. reboot to apply, then check /dev/watchdog."
	;;
remove)
	if ! has_node; then
		echo "watchdog node not present -- nothing to do"
		exit 0
	fi
	# -r removes a node; -d is for properties and leaves the node in place.
	fdtput -r "$DTB" "$NODE"
	write_fdt
	read_fdt
	has_node && die "verify failed: node still present after removal"
	echo "removed and verified. reboot to apply."
	;;
*)
	die "usage: $0 [apply|remove|--show]"
	;;
esac
