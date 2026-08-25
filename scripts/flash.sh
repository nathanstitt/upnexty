#!/bin/bash
# Flash the full firmware image to the board's SPI NAND.
#
# This erases everything on the board -- bootloader, kernel, and rootfs. The DSI
# panel timings live in the boot partition, so afterwards you need:
#   scripts/deploy.sh && adb shell /root/set-dsi-panel.sh && adb shell reboot

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

IMAGE="${1:-$IMAGE_DIR/update.img}"

[ -f "$IMAGE" ] || die "image not found: $IMAGE
set LUCKFOX_IMAGE_DIR or pass the path to update.img"

# RKFW is the Rockchip firmware container upgrade_tool expects. A raw disk image
# or a bare kernel would be silently rejected or half-written.
magic="$(xxd -l 4 -p "$IMAGE" 2>/dev/null || true)"
[ "$magic" = "524b4657" ] || die "$IMAGE is not an RKFW image (magic: ${magic:-none})"

mode="$(board_usb_mode || true)"
case "$mode" in
Maskrom | Loader) ;;
"")
	if adb_online; then
		die "board is running Linux. Reboot into flashing mode first:
  adb shell reboot loader
or hold BOOT while plugging in the USB-C OTG port."
	fi
	die "no Rockchip USB device found -- hold BOOT while connecting the OTG port"
	;;
*) die "unexpected USB mode: $mode" ;;
esac

info "flashing $IMAGE ($(du -h "$IMAGE" | cut -f1)) -- board is in $mode mode"
echo "    this erases the board. ctrl-c within 5s to abort."
sleep 5

run_upgrade_tool uf "$IMAGE"

info "flashed. the board reboots on its own; give it ~15s."
echo "next: scripts/deploy.sh, then set the panel timings (see CLAUDE.md)"
