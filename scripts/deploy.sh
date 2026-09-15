#!/bin/bash
# Push built binaries and helper scripts to the board over adb.
#
#   scripts/deploy.sh                  # everything in build/ plus scripts
#   scripts/deploy.sh build/html2fb    # just these files
#
# adb works over the USB-C OTG port -- no network or serial console needed.

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

require_adb

files=("$@")
if [ ${#files[@]} -eq 0 ]; then
	shopt -s nullglob
	files=("$REPO_ROOT"/build/* "$REPO_ROOT"/scripts/set-dsi-panel.sh)
	shopt -u nullglob
	[ ${#files[@]} -gt 0 ] || die "nothing to deploy -- run scripts/build.sh first"
fi


# push_verified copies a file and checks it survived the trip.
#
# A silently CORRUPTED transfer is the failure this exists for, not a failed
# one. On 2026-09-15 a dashboard binary arrived at the right size with a
# different checksum; the flipped bytes landed in the Go runtime's startup
# path, so it died in runtime.osinit before main, the panel went back to
# showing boot logs, and the board looked like it had failed to boot. adb
# reported success throughout.
#
# The board has md5sum (busybox); the host has md5 on macOS and md5sum on
# Linux. Comparing the two is one extra round trip per file against an hour of
# misdiagnosis.
host_md5() {
	if command -v md5 >/dev/null 2>&1; then
		md5 -q "$1"
	else
		md5sum "$1" | cut -d' ' -f1
	fi
}

push_verified() {
	local src="$1" dest="$2"
	adb push "$src" "$dest" >/dev/null || die "push failed: $src"
	local want got
	want="$(host_md5 "$src")"
	got="$(adb shell "md5sum '$dest' 2>/dev/null" | tr -d '\r' | cut -d' ' -f1)"
	if [ -z "$got" ]; then
		# No md5sum on the board is not fatal -- it would make every deploy
		# fail on an image that simply lacks the tool -- but it must be said
		# out loud rather than silently skipping the check.
		printf '    (warning: no md5sum on the board; %s not verified)\n' "$(basename "$src")"
		return 0
	fi
	[ "$want" = "$got" ] ||
		die "corrupt transfer: $(basename "$src") is $got on the board, expected $want -- re-run the deploy"
}

# Board-side init scripts live outside /root, so they are pushed separately
# rather than through the loop below (which targets BOARD_BIN_DIR).
install_init_scripts() {
	shopt -s nullglob
	local s
	for s in "$REPO_ROOT"/board/etc/init.d/*; do
		push_verified "$s" "/etc/init.d/$(basename "$s")"
		adb shell "chmod +x '/etc/init.d/$(basename "$s")'"
		printf '    %s -> /etc/init.d/\n' "$(basename "$s")"
	done
	# Board-side helper scripts that belong in /root alongside the binaries.
	# These are shell, not build output, so they are not in build/ and would
	# otherwise be lost on a reflash -- which is how the health sampler (the
	# only record of a lockup) would go missing exactly when it is needed.
	for s in "$REPO_ROOT"/board/root/*; do
		push_verified "$s" "$BOARD_BIN_DIR/$(basename "$s")"
		adb shell "chmod +x '$BOARD_BIN_DIR/$(basename "$s")'"
		printf '    %s -> %s/\n' "$(basename "$s")" "$BOARD_BIN_DIR"
	done
	shopt -u nullglob
}

for f in "${files[@]}"; do
	[ -f "$f" ] || die "not a file: $f"
	dest="$BOARD_BIN_DIR/$(basename "$f")"
	push_verified "$f" "$dest"
	adb shell "chmod +x '$dest'"
	printf '    %s -> %s\n' "$(basename "$f")" "$dest"
done

info "deployed ${#files[@]} file(s) to $BOARD_BIN_DIR"

# Only refresh the init scripts on a full deploy; a targeted `deploy.sh <file>`
# is usually an iteration loop that should not restart services.
if [ $# -eq 0 ]; then
	info "installing init scripts"
	install_init_scripts
	info "restart a service with: adb shell /etc/init.d/<name> restart"
fi
