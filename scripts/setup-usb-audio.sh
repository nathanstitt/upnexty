#!/bin/bash
# Install the USB audio class driver on a board and play a test tone.
#
# Companion to scripts/build-usb-audio.sh, which produces the four modules in
# build/modules/. This copies them into the board's module tree, writes their
# modules.dep entries by hand (this Buildroot image has no depmod), loads them,
# and checks that a plugged-in USB speaker shows up as an ALSA card.
#
#   scripts/setup-usb-audio.sh root@192.168.1.86      # over ssh
#   scripts/setup-usb-audio.sh                        # over adb
#
# S03modules_init.sh modprobes every .ko under /lib/modules/$(uname -r)/kernel
# at boot, so once installed and listed in modules.dep the driver loads on its
# own, the same way the WiFi driver does.

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

TARGET="${1:-}"
MODS="$REPO_ROOT/build/modules"
KVER=6.1.99
D="/lib/modules/$KVER"

for m in snd-hwdep snd-rawmidi snd-usbmidi-lib snd-usb-audio; do
	[ -f "$MODS/$m.ko" ] || die "$MODS/$m.ko missing -- run scripts/build-usb-audio.sh first"
done

# Two transports, one interface. adb is the USB cable; ssh is WiFi, which is
# where a board on wall power lives.
if [ -n "$TARGET" ]; then
	need ssh
	SSH=(ssh -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$TARGET")
	run() { "${SSH[@]}" "$@"; }
	push() { "${SSH[@]}" "cat > '$2'" <"$1"; }
else
	require_adb
	run() { adb shell "$@"; }
	push() { adb push "$1" "$2" >/dev/null; }
fi

board_vm="$(run "grep -a -o 'vermagic=[^[:cntrl:]]*' $D/kernel/net/wireless/cfg80211.ko 2>/dev/null | head -1" | tr -d '\r')"
mod_vm="vermagic=$(grep -a -o 'vermagic=[^[:cntrl:]]*' "$MODS/snd-usb-audio.ko" | head -1 | cut -d= -f2-)"
if [ -n "$board_vm" ] && [ "$board_vm" != "$mod_vm" ]; then
	die "vermagic mismatch -- the modules will not load
  board: $board_vm
  built: $mod_vm"
fi
info "vermagic matches: ${mod_vm#vermagic=}"

# Every symbol the modules import must exist in the running kernel, minus the
# ones the four modules provide to each other. modpost could not check this
# at build time (no vmlinux), and a missing one fails modprobe with "unknown
# symbol", which is easy to misread as a bad build.
if [ -f "$MODS/undefined-symbols.txt" ]; then
	info "checking imported symbols against the board's kernel"
	kallsyms="$(run "awk '{print \$3}' /proc/kallsyms" | tr -d '\r' | sort -u)"
	missing="$(comm -23 <(sort -u "$MODS/undefined-symbols.txt") <(sort -u "$MODS/defined-symbols.txt" 2>/dev/null) | comm -23 - <(echo "$kallsyms"))"
	# Without CONFIG_KALLSYMS_ALL the kernel lists only text symbols, so
	# imported variables (jiffies, param_ops_*, system_wq) look absent
	# here while being perfectly exported. Report rather than refuse:
	# modprobe below is the check that counts, and it names any symbol it
	# cannot resolve.
	if [ -n "$missing" ]; then
		echo "    not in kallsyms (data symbols are hidden without KALLSYMS_ALL; modprobe decides):"
		echo "$missing" | sed 's/^/      /'
	else
		printf '    %s symbols, all present\n' "$(wc -l <"$MODS/undefined-symbols.txt" | tr -d ' ')"
	fi
fi

info "installing modules"
run "mkdir -p $D/kernel/sound/core $D/kernel/sound/usb"
for m in snd-hwdep snd-rawmidi; do
	push "$MODS/$m.ko" "$D/kernel/sound/core/$m.ko"
done
for m in snd-usbmidi-lib snd-usb-audio; do
	push "$MODS/$m.ko" "$D/kernel/sound/usb/$m.ko"
done
# Verify every transfer: a corrupted module is refused by the kernel with an
# error that reads like a build problem.
for p in core/snd-hwdep core/snd-rawmidi usb/snd-usbmidi-lib usb/snd-usb-audio; do
	local_md5="$(md5 -q "$MODS/$(basename "$p").ko" 2>/dev/null || md5sum "$MODS/$(basename "$p").ko" | cut -d' ' -f1)"
	board_md5="$(run "md5sum $D/kernel/sound/$p.ko" | tr -d '\r' | cut -d' ' -f1)"
	[ "$local_md5" = "$board_md5" ] || die "corrupt transfer: $p.ko ($board_md5 on the board, $local_md5 here)"
	printf '    %s.ko\n' "$p"
done

# There is no depmod on this image, so modules.dep is written by hand.
# Dependencies in load order: hwdep and rawmidi need only the built-in core;
# usbmidi-lib needs rawmidi; usb-audio needs all three.
info "registering modules in modules.dep"
run "grep -v 'snd-hwdep\|snd-rawmidi\|snd-usbmidi-lib\|snd-usb-audio' $D/modules.dep > /tmp/dep.new
	printf '%s\n' \
		'kernel/sound/core/snd-hwdep.ko:' \
		'kernel/sound/core/snd-rawmidi.ko:' \
		'kernel/sound/usb/snd-usbmidi-lib.ko: kernel/sound/core/snd-rawmidi.ko' \
		'kernel/sound/usb/snd-usb-audio.ko: kernel/sound/usb/snd-usbmidi-lib.ko kernel/sound/core/snd-rawmidi.ko kernel/sound/core/snd-hwdep.ko' \
		>> /tmp/dep.new
	cp /tmp/dep.new $D/modules.dep"

info "loading"
run "modprobe snd-usb-audio" || die "modprobe failed -- dmesg on the board has the reason"
run "lsmod | grep -E '^snd_(usb_audio|hwdep|rawmidi|usbmidi_lib)' | awk '{print \"    \" \$1}'"

info "sound cards"
cards="$(run "cat /proc/asound/cards" | tr -d '\r')"
echo "$cards" | sed 's/^/    /'
if echo "$cards" | grep -q "no soundcards"; then
	echo "no card yet: plug the speaker into the USB host port (the one that is not power/adb) and check dmesg"
	exit 0
fi

# The image has aplay but no speaker-test and no sample sounds, so the test
# tone is generated here: a short rising bloop, 16-bit mono at 44.1kHz.
info "playing a test bloop"
bloop="$(mktemp)"
python3 - "$bloop" <<'PY'
import math, struct, sys, wave
rate, secs = 44100, 0.35
frames = bytearray()
for i in range(int(rate * secs)):
    t = i / rate
    f = 440 + 660 * t / secs                 # sweep 440 -> 1100 Hz
    env = min(1.0, t / 0.02) * max(0.0, 1 - t / secs)
    frames += struct.pack('<h', int(12000 * env * math.sin(2 * math.pi * f * t)))
w = wave.open(sys.argv[1], 'wb'); w.setnchannels(1); w.setsampwidth(2); w.setframerate(rate)
w.writeframes(bytes(frames)); w.close()
PY
push "$bloop" /root/bloop.wav
rm -f "$bloop"
run "aplay -q -D default /root/bloop.wav" && echo "    played /root/bloop.wav (kept on the board)"
