#!/bin/bash
# Build the USB audio class driver for the board's kernel, in Docker.
#
# The Luckfox images ship no snd-usb-audio, and the kernel does not have it
# built in, so a USB speaker enumerates and then sits with no driver bound.
# ALSA core and PCM are built in; what is missing is four modules:
#
#   sound/core/snd-hwdep.ko
#   sound/core/snd-rawmidi.ko
#   sound/usb/snd-usbmidi-lib.ko
#   sound/usb/snd-usb-audio.ko
#
# They are built against the same source the board runs -- Rockchip BSP
# 6.1.99, branch rk-6.1-rkr5 of armbian/linux-rockchip -- with the RK3506
# defconfig, so that the vermagic string matches and the modules load. The
# kernel has no CONFIG_MODVERSIONS, so vermagic is the whole check. It is
# verified here before anything is copied out.
#
#   scripts/build-usb-audio.sh              # modules land in build/modules/
#   LUCKFOX_KERNEL_SRC=/path/to/6.1.99 scripts/build-usb-audio.sh
#
# The kernel tree defaults to a worktree of the vendor checkout; create one
# with:
#   git -C ~/code/vendor/linux-rockchip fetch origin rk-6.1-rkr5
#   git -C ~/code/vendor/linux-rockchip worktree add --detach \
#       ~/code/vendor/linux-6.1.99-rkr5 FETCH_HEAD
#
# Install on the board afterwards with scripts/setup-usb-audio.sh.

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

need docker "the kernel build needs Linux"

KSRC="${LUCKFOX_KERNEL_SRC:-$HOME/code/vendor/linux-6.1.99-rkr5}"
OUT="$REPO_ROOT/build/modules"
IMAGE=luckfox-kbuild:6.1
# The board's vermagic, byte for byte (grep -a vermagic= any module on it).
WANT_VERMAGIC="6.1.99 SMP preempt mod_unload ARMv7 thumb2 p2v8"

[ -f "$KSRC/Makefile" ] || die "kernel source not found at $KSRC (set LUCKFOX_KERNEL_SRC)"
ver="$(awk '/^VERSION|^PATCHLEVEL|^SUBLEVEL/{printf $3"."}' "$KSRC/Makefile")"
[ "$ver" = "6.1.99." ] || die "kernel at $KSRC is ${ver%.}, board runs 6.1.99"

mkdir -p "$OUT"

info "building image $IMAGE"
docker build -q -t "$IMAGE" "$REPO_ROOT/docker/usb-audio" >/dev/null

info "configuring and building modules (this takes a few minutes the first time)"
# The object tree lives in a named volume so a rerun is incremental and the
# source worktree stays clean. The source is mounted read-only.
docker run --rm \
	-v "$KSRC:/src:ro" \
	-v luckfox-kbuild-obj:/obj \
	-v "$OUT:/out" \
	-e WANT_VERMAGIC="$WANT_VERMAGIC" \
	"$IMAGE" bash -eu -o pipefail -c '
	cd /src
	M="make -j$(nproc) O=/obj"
	# rk3506_defconfig plus the fragments the Luckfox build uses. USB stays
	# =m as in the defconfig: the board loads usbcore and dwc2 as modules.
	if [ ! -f /obj/.config ]; then
		$M rk3506_defconfig rk3506-display.config rk3506-wifibt.config >/dev/null
	fi
	/src/scripts/config --file /obj/.config \
		-e SND_USB -m SND_USB_AUDIO -m SND_HWDEP -m SND_RAWMIDI \
		-d SND_USB_UA101 -d SND_USB_USX2Y -d SND_USB_CAIAQ -d SND_USB_6FIRE \
		-d SND_USB_HIFACE -d SND_BCD2000 -d SND_USB_POD -d SND_USB_PODHD \
		-d SND_USB_TONEPORT -d SND_USB_VARIAX \
		-d LOCALVERSION_AUTO --set-str LOCALVERSION "" \
		-d STACKPROTECTOR -d STACKPROTECTOR_STRONG -d STACKPROTECTOR_PER_TASK
	# No stack protector: the kernel on the board exports no __stack_chk_guard,
	# so a module built with it fails to load on that one symbol. (The
	# defconfig turns it on; the Luckfox build evidently does not.)
	$M olddefconfig >/dev/null
	for k in SND_USB_AUDIO SND_HWDEP SND_RAWMIDI; do
		grep -q "^CONFIG_$k=m" /obj/.config || { echo "CONFIG_$k did not take"; grep "CONFIG_$k" /obj/.config || true; exit 1; }
	done
	echo "kernelrelease: $($M -s kernelrelease)"
	$M modules_prepare >/dev/null
	# modpost cannot see the kernel'"'"'s own exports without a vmlinux build, so
	# it warns about every symbol these modules import from it. Those resolve
	# at load time; the list is written out so the installer can check each
	# one against the running kernel'"'"'s kallsyms instead.
	$M M=sound/core modules 2>&1 | grep -v "modpost" || true
	$M M=sound/usb modules 2>&1 | grep -v "modpost" || true
	rm -f /out/*.ko /out/undefined-symbols.txt /out/defined-symbols.txt
	for f in sound/core/snd-hwdep.ko sound/core/snd-rawmidi.ko \
	         sound/usb/snd-usbmidi-lib.ko sound/usb/snd-usb-audio.ko; do
		[ -f /obj/$f ] || { echo "missing /obj/$f"; exit 1; }
		# The string the kernel embeds ends in a space; compare trimmed.
		vm="$(modinfo -F vermagic /obj/$f | sed "s/[[:space:]]*$//")"
		[ "$vm" = "$WANT_VERMAGIC" ] || { echo "vermagic mismatch on $f: [$vm] want [$WANT_VERMAGIC]"; exit 1; }
		arm-linux-gnueabihf-strip --strip-debug -o /out/$(basename $f) /obj/$f
		arm-linux-gnueabihf-nm -u /obj/$f | awk "{print \$2}" >> /out/undefined-symbols.txt
		arm-linux-gnueabihf-nm --defined-only --extern-only /obj/$f | awk "{print \$3}" >> /out/defined-symbols.txt
		printf "    %-22s %7s bytes  depends=%s\n" $(basename $f) $(stat -c %s /out/$(basename $f)) "$(modinfo -F depends /obj/$f)"
	done
	sort -u -o /out/undefined-symbols.txt /out/undefined-symbols.txt
	sort -u -o /out/defined-symbols.txt /out/defined-symbols.txt
	echo "    $(wc -l < /out/undefined-symbols.txt) distinct imported symbols listed in undefined-symbols.txt"
'

info "modules in $OUT (vermagic verified: $WANT_VERMAGIC)"
echo "next: scripts/setup-usb-audio.sh root@<board>"
