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

for f in "${files[@]}"; do
	[ -f "$f" ] || die "not a file: $f"
	dest="$BOARD_BIN_DIR/$(basename "$f")"
	adb push "$f" "$dest" >/dev/null || die "push failed: $f"
	adb shell "chmod +x '$dest'"
	printf '    %s -> %s\n' "$(basename "$f")" "$dest"
done

info "deployed ${#files[@]} file(s) to $BOARD_BIN_DIR"
