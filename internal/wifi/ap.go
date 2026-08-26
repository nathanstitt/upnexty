package wifi

import (
	"fmt"
	"os"
	"strings"
)

// APAddr is the board's address in AP mode, and the portal's address there.
const APAddr = "192.168.4.1"

// APName derives the access point name from the WiFi MAC's last two octets, so
// a returning user sees the same network name every time.
func APName(mac string) string {
	clean := strings.ToLower(mac)
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	if len(clean) < 4 {
		// No MAC available. "setup" is still joinable and still unambiguous
		// on a network that has exactly one of these boards on it.
		return "upnext-setup"
	}
	return "upnext-" + clean[len(clean)-4:]
}

func (c *Client) hostapdConf() string {
	if c.HostapdConf == "" {
		return "/tmp/hostapd.conf"
	}
	return c.HostapdConf
}

func (c *Client) dnsmasqConf() string {
	if c.DnsmasqConf == "" {
		return "/tmp/dnsmasq-ap.conf"
	}
	return c.DnsmasqConf
}

// restoreSTA undoes a partial AP bring-up. Called when StartAP fails after the
// supplicant has already been killed: without it the board is left with no
// supplicant and no AP, unreachable over WiFi with no automatic way back.
func (c *Client) restoreSTA() {
	_, _ = c.R.Run("killall", "hostapd")
	_, _ = c.R.Run("killall", "dnsmasq")
	_, _ = c.R.Run("ip", "addr", "del", APAddr+"/24", "dev", Iface)
	_, _ = c.R.Run("/etc/init.d/S99wlan0", "restart")
}

// StartAP brings up the setup access point: an open network named upnext-<hex>
// serving DHCP and answering every DNS query with the portal's address.
//
// The AP is deliberately open. A password on the setup network would need to be
// communicated somehow, and the thing it would protect is a form that already
// requires the admin password. Joining it grants nothing but the portal login.
func (c *Client) StartAP(mac string) error {
	// Stop the supplicant first: it and hostapd cannot both own wlan0.
	_, _ = c.R.Run("killall", "wpa_supplicant")

	// hostapd and dnsmasq may already be running from an earlier StartAP --
	// killall exits non-zero when nothing matches, which is the normal path.
	_, _ = c.R.Run("killall", "hostapd")
	_, _ = c.R.Run("killall", "dnsmasq")

	hostapd := fmt.Sprintf(`interface=%s
driver=nl80211
ssid=%s
hw_mode=g
channel=6
auth_algs=1
wmm_enabled=0
`, Iface, APName(mac))
	if err := os.WriteFile(c.hostapdConf(), []byte(hostapd), 0o644); err != nil {
		return fmt.Errorf("write hostapd config: %w", err)
	}

	// address=/#/ answers EVERY name with the portal address. That is what
	// makes a phone show its "sign in to network" sheet, and with no iptables
	// on this image it is the whole redirect mechanism.
	// A second dnsmasq here is safe alongside the board's S80dnsmasq init script
	// because that script exits immediately (its /etc/dnsmasq.conf is empty), and
	// this one's bind-interfaces confines it to wlan0.
	dnsmasq := fmt.Sprintf(`interface=%s
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,12h
address=/#/%s
no-resolv
`, Iface, APAddr)
	if err := os.WriteFile(c.dnsmasqConf(), []byte(dnsmasq), 0o644); err != nil {
		return fmt.Errorf("write dnsmasq config: %w", err)
	}

	if out, err := c.R.Run("ip", "addr", "add", APAddr+"/24", "dev", Iface); err != nil {
		// Already assigned is fine; anything else is not.
		if !strings.Contains(string(out), "File exists") {
			c.restoreSTA()
			return fmt.Errorf("assign %s: %w (%s)", APAddr, err, strings.TrimSpace(string(out)))
		}
	}
	if out, err := c.R.Run("ip", "link", "set", Iface, "up"); err != nil {
		c.restoreSTA()
		return fmt.Errorf("bring up %s: %w (%s)", Iface, err, strings.TrimSpace(string(out)))
	}
	if out, err := c.R.Run("hostapd", "-B", c.hostapdConf()); err != nil {
		c.restoreSTA()
		return fmt.Errorf("start hostapd: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := c.R.Run("dnsmasq", "-C", c.dnsmasqConf()); err != nil {
		c.restoreSTA()
		return fmt.Errorf("start dnsmasq: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopAP tears the access point down and returns the board to STA mode. Stopping
// one that is not running is not an error -- that is the normal path on a board
// that booted straight into STA.
func (c *Client) StopAP() error {
	_, _ = c.R.Run("killall", "hostapd")
	_, _ = c.R.Run("killall", "dnsmasq")
	_, _ = c.R.Run("ip", "addr", "del", APAddr+"/24", "dev", Iface)
	_, _ = c.R.Run("/etc/init.d/S99wlan0", "restart")
	return nil
}
