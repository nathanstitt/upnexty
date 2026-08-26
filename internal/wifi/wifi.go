package wifi

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Iface is the wireless interface. The board has exactly one.
const Iface = "wlan0"

// Status is the current association state.
type Status struct {
	State     string // raw wpa_state, e.g. COMPLETED, SCANNING
	SSID      string
	IP        string
	Connected bool
}

// Network is one SSID seen in a scan.
type Network struct {
	SSID    string
	Signal  int // dBm, closer to zero is stronger
	Secured bool
}

// Client talks to the board's wifi tooling.
type Client struct {
	R Runner
	// ConfPath is the persistent supplicant config. Defaults to
	// /etc/wpa_supplicant.conf when empty.
	ConfPath string
}

func (c *Client) confPath() string {
	if c.ConfPath == "" {
		return "/etc/wpa_supplicant.conf"
	}
	return c.ConfPath
}

// Status reports the current association.
func (c *Client) Status() (Status, error) {
	out, err := c.R.Run("wpa_cli", "-i", Iface, "status")
	if err != nil {
		return Status{}, fmt.Errorf("wpa_cli status: %w", err)
	}
	var s Status
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "wpa_state":
			s.State = v
		case "ssid":
			s.SSID = v
		case "ip_address":
			s.IP = v
		}
	}
	s.Connected = s.State == "COMPLETED"
	// wpa_cli reports a stale ssid while re-scanning; only claim one when
	// actually associated, so the UI never shows a network we are not on.
	if !s.Connected {
		s.SSID = ""
	}
	return s, nil
}

// Scan returns the visible networks, strongest first, one entry per SSID.
//
// This parses `scan_results` rather than using `iw`, which is not on the image.
// It does not trigger a fresh scan -- wpa_supplicant scans on its own while
// unassociated, which is exactly when the picker is used.
func (c *Client) Scan() ([]Network, error) {
	out, err := c.R.Run("wpa_cli", "-i", Iface, "scan_results")
	if err != nil {
		return nil, fmt.Errorf("wpa_cli scan_results: %w", err)
	}

	best := map[string]Network{}
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // header or blank
		}
		// bssid \t freq \t signal \t flags \t ssid
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		ssid := strings.TrimSpace(f[4])
		if ssid == "" {
			continue // hidden network: nothing to show or type
		}
		sig, err := strconv.Atoi(strings.TrimSpace(f[2]))
		if err != nil {
			continue
		}
		n := Network{
			SSID:    ssid,
			Signal:  sig,
			Secured: strings.Contains(f[3], "WPA") || strings.Contains(f[3], "WEP"),
		}
		// Same SSID on several APs: keep the strongest.
		if prev, seen := best[ssid]; !seen || n.Signal > prev.Signal {
			best[ssid] = n
		}
	}

	nets := make([]Network, 0, len(best))
	for _, n := range best {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Signal != nets[j].Signal {
			return nets[i].Signal > nets[j].Signal
		}
		// Ties are common, and map iteration is randomized -- without a
		// secondary key the picker's order would shuffle between page loads.
		return nets[i].SSID < nets[j].SSID
	})
	return nets, nil
}

// wpaEscape quotes a value for wpa_supplicant.conf. Without this an SSID or
// password containing a quote would terminate the value early and corrupt the
// file -- leaving a board that cannot associate and cannot be reached to fix.
func wpaEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// Connect writes the supplicant config and restarts the supplicant.
//
// It writes the persistent /etc/wpa_supplicant.conf rather than the /tmp copy
// the vendor's wifi-connect.sh uses -- /tmp is tmpfs, so credentials written
// there are lost on reboot.
func (c *Client) Connect(ssid, password string) error {
	if strings.TrimSpace(ssid) == "" {
		return fmt.Errorf("ssid is required")
	}

	// An open network needs key_mgmt=NONE and no psk line at all --
	// wpa_supplicant rejects WPA-PSK with an empty passphrase (it requires
	// 8-63 chars), so emitting psk="" would produce a block that never
	// associates and fails in a way that looks like a generic connect error.
	var netBlock string
	if password == "" {
		netBlock = fmt.Sprintf("network={\n\tssid=\"%s\"\n\tkey_mgmt=NONE\n}\n", wpaEscape(ssid))
	} else {
		netBlock = fmt.Sprintf("network={\n\tssid=\"%s\"\n\tpsk=\"%s\"\n\tkey_mgmt=WPA-PSK\n}\n",
			wpaEscape(ssid), wpaEscape(password))
	}

	conf := "ctrl_interface=/var/run/wpa_supplicant\nap_scan=1\nupdate_config=1\n\n" + netBlock

	// Written via a temp file + rename for the same reason config.Save is:
	// a torn write here leaves a board that cannot get back on the network.
	path := c.confPath()
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write supplicant config: %w", err)
	}
	if _, err := f.WriteString(conf); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write supplicant config: %w", err)
	}
	// fsync before rename: rename is atomic, but without the sync the rename
	// can land before the contents on power loss -- and this file is what the
	// board needs to get back on the network.
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync supplicant config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close supplicant config: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install supplicant config: %w", err)
	}

	// S99wlan0 restart re-runs the whole bring-up: supplicant, DHCP, and the
	// clock sync. Simpler and more reliable than driving wpa_cli reconfigure
	// and udhcpc separately.
	if out, err := c.R.Run("/etc/init.d/S99wlan0", "restart"); err != nil {
		return fmt.Errorf("restart networking: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
