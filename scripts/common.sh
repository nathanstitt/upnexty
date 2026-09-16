#!/bin/bash
# Shared settings and helpers for the host-side scripts. Source, don't run.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The board is 32-bit ARM (Cortex-A7). CGO stays off so binaries are static and
# don't care that the rootfs is Buildroot with its own libc.
export GOOS=linux
export GOARCH=arm
export GOARM=7
export CGO_ENABLED=0
# Strip debug info by default: it is ~6MB of a ~24MB binary and the rootfs has
# ~45MB free (not the ~90MB this comment used to claim).
#
# Stripping costs nothing at runtime -- .text is byte-identical and the debug
# sections load at addr=0, so they are never mapped. Measured on the board,
# interleaved: 6.48/6.61/6.53s stripped vs 6.52/6.48/6.37s unstripped, which is
# one distribution. It is purely a disk-space tradeoff.
#
# What it does cost is the ability to read a crash. -s -w drops .symtab and
# DWARF, and without them the runtime unwinder gives up mid-stack: the
# 2026-09-01 fault printed "traceback stuck" and named no application frame.
# (.gopclntab survives -w, so ordinary panics still symbolize; it is the hard
# faults that do not.)
#
# LUCKFOX_UNSTRIPPED=1 keeps them, for when something needs to be diagnosed.
if [ -n "${LUCKFOX_UNSTRIPPED:-}" ]; then
	GO_LDFLAGS=''
else
	GO_LDFLAGS='-s -w'
fi

# upgrade_tool ships as a universal binary. The x86_64 slice under Rosetta is
# the one Rockchip tests, so prefer it -- but only when Rosetta is actually
# installed. macOS 27 ships without it, and `arch -x86_64` then fails outright
# with "Bad CPU type in executable", which made every script that touches the
# board fatal on 2026-09-16.
#
# The arm64 slice was documented here as segfaulting in pthread_mutex_init.
# That is no longer true on macOS 27: it runs `LD` correctly and exits 0. So
# fall back to it rather than failing, and let whoever hits a real arm64 bug
# install Rosetta (`softwareupdate --install-rosetta`).
UPGRADE_TOOL="$REPO_ROOT/tools/upgrade_tool_v2.44_for_mac/upgrade_tool"
if arch -x86_64 /usr/bin/true 2>/dev/null; then
	UPGRADE_TOOL_ARCH=(arch -x86_64)
else
	UPGRADE_TOOL_ARCH=()
fi

# Firmware images live outside the repo -- they're ~130MB and vendor-supplied.
IMAGE_DIR="${LUCKFOX_IMAGE_DIR:-$HOME/Downloads/Luckfox_Lyra_Flash_250717}"

BOARD_BIN_DIR=/root

die() {
	echo "error: $*" >&2
	exit 1
}

info() { echo "==> $*"; }

need() {
	command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1${2:+ ($2)}"
}

# Run upgrade_tool -- WITHOUT sudo, which is what makes it segfault.
#
# This used to run writes under sudo, on the theory that raw USB needs root. It
# does not: macOS hands USB device access to the logged-in user, and every
# operation (LD, DB, UF) works unprivileged. Under sudo the x86_64 slice dies
# immediately -- EXC_BAD_ACCESS at 0x8, four frames from start, before it opens
# a device -- so `flash.sh` could never have worked on a Mac. The same command
# run as the user flashes normally. Verified 2026-09-15.
#
# The `arch -x86_64` preference above is a separate issue; see the note there.
#
# It writes logs to ~/upgrade_tool/log, which is another reason not to sudo:
# under root they land in /var/root and the user's copies go stale.
run_upgrade_tool() {
	[ -x "$UPGRADE_TOOL" ] || die "upgrade_tool not found at $UPGRADE_TOOL"
	"${UPGRADE_TOOL_ARCH[@]}" "$UPGRADE_TOOL" "$@"
}

# Alias kept so read-only call sites read as read-only; no behavioural
# difference now that writes are unprivileged too.
query_upgrade_tool() { run_upgrade_tool "$@"; }

# Rockchip USB mode: Maskrom (boot ROM, nothing flashed yet or BOOT held),
# Loader (U-Boot's download gadget), or empty when Linux is running.
board_usb_mode() {
	query_upgrade_tool LD 2>/dev/null | sed -n 's/.*Mode=\([A-Za-z]*\).*/\1/p' | head -1
}

adb_online() {
	adb devices 2>/dev/null | grep -q "device$"
}

require_adb() {
	need adb "install Android platform-tools"
	adb_online && return 0
	local mode
	mode="$(board_usb_mode || true)"
	if [ -n "$mode" ]; then
		die "board is in $mode mode, not running Linux -- power-cycle without holding BOOT"
	fi
	die "board not reachable over adb -- check the USB-C OTG port"
}
