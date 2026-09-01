#!/bin/bash
# Cross-compile Go commands for the board.
#
#   scripts/build.sh                      # build the default set
#   scripts/build.sh ./cmd/mytool         # build one package
#   scripts/build.sh -C ~/code/other ./cmd/x
#
# Output goes to build/ in this repo. The board is armv7 with a Buildroot
# rootfs, so binaries are static (CGO_ENABLED=0) and stripped.

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

need go

MODULE_DIR="$REPO_ROOT"
if [ "${1:-}" = "-C" ]; then
	MODULE_DIR="$2"
	shift 2
fi

# "dashboard" is accepted for symmetry with the docs; it is also the default,
# since it is the only command this repo ships.
#
# The no-arg case used to build ./cmd/html2fb and ./cmd/fbtouch out of the
# renderer's repo. Those commands do not exist in omnidoc and are not needed:
# the dashboard rasterizes in-process through internal/fb. Stale binaries may
# still be sitting in build/ from before the rename.
if [ "${1:-}" = "dashboard" ]; then
	shift
fi
# Only default the package when building THIS repo -- with -C the caller named
# another module, and ./cmd/dashboard would not exist there.
if [ $# -eq 0 ]; then
	[ "$MODULE_DIR" = "$REPO_ROOT" ] || die "no packages to build in $MODULE_DIR"
	set -- ./cmd/dashboard
fi

OUT="$REPO_ROOT/build"
mkdir -p "$OUT"

for pkg in "$@"; do
	name="$(basename "$pkg")"
	info "building $name for $GOOS/$GOARCH (v$GOARM)"
	(cd "$MODULE_DIR" && go build -ldflags="$GO_LDFLAGS" -o "$OUT/$name" "$pkg")
	printf '    %s  %s\n' "$(du -h "$OUT/$name" | cut -f1)" "$OUT/$name"
done

# A host-arch binary here would fail on the board with a confusing exec error,
# so confirm the ELF header actually says 32-bit ARM.
for pkg in "$@"; do
	name="$(basename "$pkg")"
	file "$OUT/$name" | grep -q "ELF 32-bit LSB.*ARM" ||
		die "$name is not a 32-bit ARM binary -- check GOARCH"
done
