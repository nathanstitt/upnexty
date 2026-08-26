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

# Board-side init scripts live outside /root, so they are pushed separately
# rather than through the loop below (which targets BOARD_BIN_DIR).
install_init_scripts() {
	shopt -s nullglob
	local s
	for s in "$REPO_ROOT"/board/etc/init.d/*; do
		adb push "$s" "/etc/init.d/$(basename "$s")" >/dev/null ||
			die "push failed: $s"
		adb shell "chmod +x '/etc/init.d/$(basename "$s")'"
		printf '    %s -> /etc/init.d/\n' "$(basename "$s")"
	done
	shopt -u nullglob
}

for f in "${files[@]}"; do
	[ -f "$f" ] || die "not a file: $f"
	dest="$BOARD_BIN_DIR/$(basename "$f")"
	adb push "$f" "$dest" >/dev/null || die "push failed: $f"
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
