#!/bin/bash
# Install WiFi support on a board flashed with a non-W Lyra image.
#
# The stock Luckfox_Lyra_Flash_* image is built for the base Lyra, which has no
# WiFi hardware, so it ships no driver -- even though the Zero W's AIC8800DC is
# present and enumerated (its Bluetooth half works out of the box). This copies
# the driver, firmware, and connect tooling out of the Zero W image instead.
#
#   scripts/setup-wifi.sh                 # extract + install
#   scripts/setup-wifi.sh --extract-only  # unpack the image, don't touch the board
#
# Afterwards, set the credentials ON THE BOARD (they are not stored in this
# repo) and reboot:
#
#   adb shell '/usr/bin/wifi-connect.sh <SSID> <PASSWORD>'
#   adb shell 'sed "s/SSID/<SSID>/; s/PASSWORD/<PASSWORD>/" /etc/wpa_supplicant.conf \
#       > /tmp/w && cp /tmp/w /etc/wpa_supplicant.conf'
#
# The first command connects now; the second makes it survive a reboot, because
# wifi-connect.sh templates into /tmp, which is tmpfs.

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ZERO_W_DIR="${LUCKFOX_ZERO_W_DIR:-$HOME/Downloads/Luckfox_Lyra_Zero_W_Flash_250717}"
WORK="${TMPDIR:-/tmp}/luckfox-zerow-extract"

extract_only=0
[ "${1:-}" = "--extract-only" ] && extract_only=1

need docker "the rootfs is UBIFS; extracting it needs Linux"

[ -f "$ZERO_W_DIR/rootfs.img" ] || die "Zero W rootfs not found at $ZERO_W_DIR/rootfs.img
Download Luckfox_Lyra_Zero_W_Flash_<date>.zip from the Luckfox wiki's Google
Drive (Firmware -> Buildroot) and unzip it, or set LUCKFOX_ZERO_W_DIR."

info "extracting Zero W rootfs (UBIFS, via docker)"
# ubireader_extract_files refuses to write into an existing output directory.
rm -rf "$WORK"
mkdir -p "$WORK"
docker run --rm \
	-v "$ZERO_W_DIR:/img:ro" \
	-v "$WORK:/out" \
	alpine:latest sh -c '
		apk add --no-cache python3 py3-pip >/dev/null 2>&1
		pip3 install --quiet --break-system-packages ubi_reader >/dev/null 2>&1
		ubireader_extract_files -o /out/rootfs /img/rootfs.img >/dev/null 2>&1
	' || die "extraction failed"

# ubi_reader nests its output under a volume-id directory whose name varies per
# image, so locate things by the file we came for rather than by a fixed path.
# Note the modules live under usr/lib/modules -- in the extracted tree /lib is a
# real directory, not the symlink to /usr/lib that it is on a running board.
MODSRC="$(dirname "$(find "$WORK/rootfs" -type f -name aic8800_fdrv.ko | head -1)")"
[ -n "$MODSRC" ] || die "no aic8800_fdrv.ko in the extracted image
Is $ZERO_W_DIR really a Zero W (_W_) image rather than a base Lyra one?"
R="${MODSRC%/lib/modules}"      # strip back to the rootfs root
R="${R%/usr}"
FWSRC="$(dirname "$(find "$WORK/rootfs" -type d -name aic8800DC | head -1)")"
[ -n "$FWSRC" ] || die "no aic8800DC firmware directory in the extracted image"

info "extracted to $R"
if [ "$extract_only" = 1 ]; then
	exit 0
fi

require_adb

# The module must match the running kernel exactly or insmod refuses it.
# `grep -m1` closes the pipe early, so strings takes SIGPIPE -- harmless here,
# but it would trip the `set -e` inherited from common.sh. head -1 instead.
board_vm="$(adb shell 'strings /lib/modules/6.1.99/kernel/net/wireless/cfg80211.ko 2>/dev/null | grep "^vermagic=" | head -1' | tr -d '\r')"
img_vm="$(strings "$MODSRC/aic8800_fdrv.ko" | grep '^vermagic=' | head -1)"
if [ "$board_vm" != "$img_vm" ]; then
	die "vermagic mismatch -- the module will not load
  board: $board_vm
  image: $img_vm"
fi
info "vermagic matches: $img_vm"

info "installing modules"
adb shell 'mkdir -p /lib/modules/6.1.99/kernel/drivers/net/wireless'
for m in aic8800_fdrv aic_load_fw aic_btusb; do
	adb push "$MODSRC/$m.ko" \
		"/lib/modules/6.1.99/kernel/drivers/net/wireless/$m.ko" >/dev/null ||
		die "push failed: $m"
	printf '    %s\n' "$m.ko"
done

# There is no depmod on this Buildroot image, so modules.dep is written by hand.
# S03modules_init.sh runs `modprobe $(basename *.ko)` over every module at boot,
# and modprobe refuses anything absent from modules.dep.
info "registering modules in modules.dep"
adb shell 'D=/lib/modules/6.1.99; P=kernel/drivers/net/wireless
	grep -v "aic8800_fdrv\|aic_load_fw\|aic_btusb" $D/modules.dep > /tmp/dep.new
	printf "%s\n" "$P/aic_load_fw.ko:" \
		"$P/aic8800_fdrv.ko: $P/aic_load_fw.ko" \
		"$P/aic_btusb.ko:" >> /tmp/dep.new
	cp /tmp/dep.new $D/modules.dep'

info "installing firmware"
adb shell 'mkdir -p /lib/firmware'
adb push "$FWSRC/aic8800DC" /lib/firmware/ >/dev/null ||
	die "firmware push failed"
printf '    %s blobs\n' "$(ls "$FWSRC/aic8800DC" | wc -l | tr -d ' ')"

# Our image ships an open-network stub; the Zero W one has the WPA2 template
# that wifi-connect.sh substitutes into.
info "installing connect tooling"
adb push "$R/usr/bin/wifi-connect.sh" /usr/bin/wifi-connect.sh >/dev/null
adb shell 'chmod +x /usr/bin/wifi-connect.sh'

# Only lay down the template if the board does not already hold real
# credentials -- re-running this installer must not break a working board.
if adb shell 'grep -q "ssid=\"SSID\"" /etc/wpa_supplicant.conf 2>/dev/null || [ ! -f /etc/wpa_supplicant.conf ]'; then
	adb shell '[ -f /etc/wpa_supplicant.conf ] && cp /etc/wpa_supplicant.conf /root/wpa_supplicant.conf.orig'
	adb push "$R/etc/wpa_supplicant.conf" /etc/wpa_supplicant.conf >/dev/null
	printf '    wpa_supplicant.conf template installed\n'
else
	printf '    wpa_supplicant.conf already configured -- left alone\n'
fi

# Neither image ships CA certificates, so every HTTPS client fails with
# "x509: certificate signed by unknown authority" until this is installed.
info "installing CA certificates (from this host)"
host_ca=""
for p in /etc/ssl/cert.pem /usr/local/etc/openssl@3/cert.pem; do
	[ -f "$p" ] && host_ca="$p" && break
done
[ -n "$host_ca" ] || die "no CA bundle found on this host"
adb shell 'mkdir -p /etc/ssl/certs'
adb push "$host_ca" /etc/ssl/certs/ca-certificates.crt >/dev/null
printf '    %s certs\n' "$(grep -c 'BEGIN CERT' "$host_ca")"

info "installing boot script"
adb push "$REPO_ROOT/board/etc/init.d/S99wlan0" /etc/init.d/S99wlan0 >/dev/null
adb shell 'chmod +x /etc/init.d/S99wlan0'

info "done -- now set the credentials on the board and reboot:"
cat <<'EOF'

    adb shell '/usr/bin/wifi-connect.sh <SSID> <PASSWORD>'
    adb shell 'sed "s/SSID/<SSID>/; s/PASSWORD/<PASSWORD>/" /etc/wpa_supplicant.conf > /tmp/w && cp /tmp/w /etc/wpa_supplicant.conf'
    adb reboot

EOF
