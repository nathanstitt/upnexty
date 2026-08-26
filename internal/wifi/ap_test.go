package wifi

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAPNameFromMAC(t *testing.T) {
	if got := APName("54:01:4a:4c:1b:fd"); got != "upnext-1bfd" {
		t.Errorf("APName = %q, want upnext-1bfd", got)
	}
}

func TestAPNameStableAcrossFormats(t *testing.T) {
	for _, mac := range []string{"54:01:4A:4C:1B:FD", "54-01-4a-4c-1b-fd"} {
		if got := APName(mac); got != "upnext-1bfd" {
			t.Errorf("APName(%q) = %q, want upnext-1bfd", mac, got)
		}
	}
}

func TestAPNameFallsBackWithoutMAC(t *testing.T) {
	// Must still produce a joinable name rather than "upnext-".
	got := APName("")
	if got == "upnext-" || got == "" {
		t.Errorf("APName(\"\") = %q, want a usable fallback", got)
	}
}

func TestStartAPWritesConfigsAndStartsBoth(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, HostapdConf: dir + "/hostapd.conf", DnsmasqConf: dir + "/dnsmasq-ap.conf"}

	if err := c.StartAP("54:01:4a:4c:1b:fd"); err != nil {
		t.Fatal(err)
	}

	hb, err := os.ReadFile(c.HostapdConf)
	if err != nil {
		t.Fatal(err)
	}
	h := string(hb)
	if !strings.Contains(h, "ssid=upnext-1bfd") {
		t.Errorf("hostapd.conf missing ssid:\n%s", h)
	}
	if !strings.Contains(h, "interface=wlan0") {
		t.Errorf("hostapd.conf missing interface:\n%s", h)
	}

	db, err := os.ReadFile(c.DnsmasqConf)
	if err != nil {
		t.Fatal(err)
	}
	d := string(db)
	// The wildcard DNS entry IS the captive-portal redirect; there is no
	// iptables on this image to do it any other way.
	if !strings.Contains(d, "address=/#/192.168.4.1") {
		t.Errorf("dnsmasq config missing the wildcard redirect:\n%s", d)
	}
	if !strings.Contains(d, "dhcp-range=") {
		t.Errorf("dnsmasq config missing dhcp-range; clients get no address:\n%s", d)
	}

	var startedHostapd, startedDnsmasq, addressed bool
	for _, call := range f.call {
		switch {
		case strings.Contains(call, "hostapd"):
			startedHostapd = true
		case strings.Contains(call, "dnsmasq"):
			startedDnsmasq = true
		case strings.Contains(call, "192.168.4.1"):
			addressed = true
		}
	}
	if !startedHostapd || !startedDnsmasq {
		t.Errorf("both daemons must start; calls were %v", f.call)
	}
	if !addressed {
		t.Errorf("wlan0 must get 192.168.4.1; calls were %v", f.call)
	}
}

func TestStopAPKillsBoth(t *testing.T) {
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f}
	if err := c.StopAP(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.call, " | ")
	if !strings.Contains(joined, "hostapd") || !strings.Contains(joined, "dnsmasq") {
		t.Errorf("StopAP must stop both daemons; calls were %v", f.call)
	}
}

func TestStopAPIgnoresNotRunning(t *testing.T) {
	// killall exits non-zero when nothing matches. Stopping an AP that is not
	// running is not an error -- it is the normal path on every boot.
	f := &fakeRunner{err: map[string]error{
		"killall hostapd": errors.New("exit status 1"),
		"killall dnsmasq": errors.New("exit status 1"),
	}}
	c := &Client{R: f}
	if err := c.StopAP(); err != nil {
		t.Errorf("StopAP should tolerate daemons that are not running: %v", err)
	}
}
