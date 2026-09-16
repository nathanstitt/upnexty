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

# Ignore SIGPIPE for the write, and never let the caller's pipe reach it.
#
# On 2026-09-15 a flash was run as `upgrade_tool UF ... | head`. head exited at
# 3%, the kernel sent SIGPIPE, and the write died having erased the bootloader
# but written almost none of the image. The board dropped to Maskrom and never
# came back. A partial write is the one outcome worth engineering against here:
# it is the difference between "retry it" and "the board no longer boots".
#
# Progress goes to the terminal on stderr regardless; stdout is teed to a log so
# a transcript survives without a pipe being able to kill the writer.
flash_log="${TMPDIR:-/tmp}/luckfox-flash-$(date +%Y%m%d-%H%M%S).log"
info "progress log: $flash_log"

set +e
(
	trap '' PIPE
	run_upgrade_tool uf "$IMAGE"
) > >(tee "$flash_log") 2>&1
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
	die "flash FAILED (exit $rc) -- see $flash_log

The board is probably in Maskrom now with a partial image. That is recoverable:
hold BOOT while reconnecting the OTG port, then run this script again. Do not
pipe this script's output into a command that exits early (head, grep -q, less
that you quit) -- that is what interrupts the write."
fi

info "flashed. the board reboots on its own; give it ~15s."
echo "next: scripts/deploy.sh, then set the panel timings (see CLAUDE.md)"
