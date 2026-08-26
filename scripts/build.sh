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

# doctaculous is the HTML/document renderer these tools are built around.
DOCTACULOUS="${DOCTACULOUS_DIR:-$HOME/code/doctaculous}"
# "dashboard" builds this repo's service; no args builds the doctaculous tools.
if [ "${1:-}" = "dashboard" ]; then
	shift
	set -- ./cmd/dashboard "$@"
elif [ "$MODULE_DIR" = "$REPO_ROOT" ] && [ $# -eq 0 ]; then
	[ -d "$DOCTACULOUS" ] || die "doctaculous not found at $DOCTACULOUS
set DOCTACULOUS_DIR, or pass packages to build"
	MODULE_DIR="$DOCTACULOUS"
	set -- ./cmd/html2fb ./cmd/fbtouch
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
