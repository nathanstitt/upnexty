# Does the AIC8800DC support AP mode?

**Yes.** Confirmed 2026-08-26 on the running board, without disturbing the
interface.

This mattered because the captive portal's whole AP-fallback design rests on it,
and the AIC8800DC is a USB part with a vendor out-of-tree driver — nothing about
it is guaranteed. There is no `iw` on this image, which is the usual way to ask.

## The answer, in one command

```
# wpa_cli -i wlan0 get_capability modes
AP
```

`get_capability modes` asks nl80211 which interface types the driver supports
and prints them. It is a query — it does not reconfigure the radio, so it is
safe to run on a live board.

Also useful, from the same source:

```
# wpa_cli -i wlan0 get_capability channels
Mode[G] Channels: 1 2 3 4 5 6 7 8 9 10 11 12 13 14
Mode[B] Channels: 1 2 3 4 5 6 7 8 9 10 11 12 13 14
```

2.4 GHz only, channels 1–14. Channel 6 (the plan's choice) is valid.

## Corroborating evidence from the driver binary

`strings` on `/lib/modules/6.1.99/kernel/drivers/net/wireless/aic8800_fdrv.ko`
shows the cfg80211 callbacks hostapd requires:

| Callback | Occurrences |
|---|---|
| `start_ap` | 3 |
| `stop_ap` | 1 |
| `change_beacon` | 1 |
| `del_station` | 1 |
| `change_station` | 1 |

Plus a complete APM (Access Point Manager) command set — `APM_START_REQ`/`CFM`,
`APM_STOP_REQ`, `APM_SET_BEACON_IE_REQ`, `APM_START_CAC_REQ` (radar channel
availability check) — and runtime log strings including:

```
AP started: ch=%d, bcmc_idx=%d channel=%d bw=%d
AP Stopped
AP not stopped when disabling interface
```

Those are not stubs. The driver has a working AP implementation with station
tracking and channel-switch handling.

## How NOT to check this

An earlier attempt ran `hostapd` directly against `wlan0` to see whether it came
up. It tore down the association — and since adb rides the USB gadget stack that
also depends on the WiFi path, the board dropped off **both** routes and needed a
physical power cycle.

If a live hostapd test is ever needed, background it with an unconditional
restore so the board recovers on its own:

```bash
adb shell 'nohup sh -c "hostapd -B /tmp/ap.conf; sleep 20; killall hostapd; /etc/init.d/S99wlan0 restart" >/tmp/probe.log 2>&1 &'
```

But for the question "does this driver do AP mode at all", `get_capability
modes` answers it in one line and touches nothing.

## What this unblocks

The captive portal's AP fallback (plan Tasks 7, 11, 13) is viable as designed:
`hostapd` on channel 6, 2.4 GHz, with `dnsmasq` providing DHCP and the wildcard
DNS that makes phones show their "sign in to network" sheet.

Still unproven, and only testable by actually raising the AP: whether the driver
sustains a stable AP with clients attached, and whether it can hold AP and STA
simultaneously (the plan does not need concurrency — AP is a fallback for when
STA has failed).
