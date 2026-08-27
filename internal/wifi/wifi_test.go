package wifi

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeRunner returns canned output per command, and records what was called.
type fakeRunner struct {
	out  map[string][]byte
	err  map[string]error
	call []string
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	f.call = append(f.call, key)
	if e, ok := f.err[key]; ok {
		return nil, e
	}
	return f.out[key], nil
}

// Captured from the real board.
const statusConnected = `bssid=fc:ec:da:f1:e7:e0
freq=2462
ssid=Argosity
id=0
mode=station
wpa_state=COMPLETED
ip_address=192.168.1.80
key_mgmt=WPA2-PSK
`

const statusScanning = `wpa_state=SCANNING
`

const scanResults = `bssid / frequency / signal level / flags / ssid
86:25:19:98:5a:b4	2462	-69	[WPA2-PSK-CCMP][ESS]	DIRECT-eGM2020 Series
fc:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]	Argosity
fc:ec:da:f1:ea:a0	2437	-77	[WPA2-PSK-CCMP][ESS]	Argosity
02:ec:da:f1:e7:e0	2462	-72	[ESS]	OpenNet
`

func TestStatusConnected(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 status": []byte(statusConnected),
	}}}
	got, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Connected {
		t.Error("Connected = false, want true for wpa_state=COMPLETED")
	}
	if got.SSID != "Argosity" {
		t.Errorf("SSID = %q, want Argosity", got.SSID)
	}
	if got.IP != "192.168.1.80" {
		t.Errorf("IP = %q", got.IP)
	}
}

func TestStatusScanningIsNotConnected(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 status": []byte(statusScanning),
	}}}
	got, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.Connected {
		t.Error("Connected = true for wpa_state=SCANNING")
	}
	if got.SSID != "" {
		t.Errorf("SSID = %q, want empty while scanning", got.SSID)
	}
}

func TestStatusErrorPropagates(t *testing.T) {
	c := &Client{R: &fakeRunner{err: map[string]error{
		"wpa_cli -i wlan0 status": errors.New("no such device"),
	}}}
	if _, err := c.Status(); err == nil {
		t.Fatal("expected an error when wpa_cli fails")
	}
}

func TestScanParsesAndDedupes(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(scanResults),
	}}}
	nets, err := c.Scan()
	if err != nil {
		t.Fatal(err)
	}
	// Argosity appears twice (two APs); the picker should list it once.
	var argosity int
	for _, n := range nets {
		if n.SSID == "Argosity" {
			argosity++
		}
	}
	if argosity != 1 {
		t.Errorf("Argosity listed %d times, want 1 (deduped)", argosity)
	}
	if len(nets) != 3 {
		t.Errorf("got %d networks, want 3 unique SSIDs", len(nets))
	}
}

func TestScanKeepsStrongestOfDuplicates(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(scanResults),
	}}}
	nets, _ := c.Scan()
	for _, n := range nets {
		if n.SSID == "Argosity" && n.Signal != -72 {
			t.Errorf("Argosity signal = %d, want the stronger -72", n.Signal)
		}
	}
}

func TestScanMarksOpenNetworks(t *testing.T) {
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(scanResults),
	}}}
	nets, _ := c.Scan()
	for _, n := range nets {
		switch n.SSID {
		case "OpenNet":
			if n.Secured {
				t.Error("OpenNet has no [WPA...] flag; Secured should be false")
			}
		case "Argosity":
			if !n.Secured {
				t.Error("Argosity is WPA2-PSK; Secured should be true")
			}
		}
	}
}

func TestScanSkipsHiddenSSIDs(t *testing.T) {
	const withHidden = `bssid / frequency / signal level / flags / ssid
02:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]
fc:ec:da:f1:e7:e0	2462	-70	[WPA2-PSK-CCMP][ESS]	Argosity
`
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(withHidden),
	}}}
	nets, _ := c.Scan()
	if len(nets) != 1 || nets[0].SSID != "Argosity" {
		t.Errorf("hidden (blank) SSID should be skipped, got %+v", nets)
	}
}

func TestScanErrorPropagates(t *testing.T) {
	c := &Client{R: &fakeRunner{err: map[string]error{
		"wpa_cli -i wlan0 scan_results": errors.New("no such device"),
	}}}
	if _, err := c.Scan(); err == nil {
		t.Fatal("expected an error when wpa_cli fails")
	}
}

func TestScanMalformedLinesTooFewFields(t *testing.T) {
	const malformed = `bssid / frequency / signal level / flags / ssid
86:25:19:98:5a:b4	2462	-69
fc:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]	Argosity
`
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(malformed),
	}}}
	nets, err := c.Scan()
	if err != nil {
		t.Fatal(err)
	}
	// Line with too few fields is skipped silently; only valid Argosity should remain.
	if len(nets) != 1 || nets[0].SSID != "Argosity" {
		t.Errorf("malformed line should be skipped, got %+v", nets)
	}
}

func TestScanNonNumericSignal(t *testing.T) {
	const badSignal = `bssid / frequency / signal level / flags / ssid
86:25:19:98:5a:b4	2462	invalid	[WPA2-PSK-CCMP][ESS]	BadSignal
fc:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]	Argosity
`
	c := &Client{R: &fakeRunner{out: map[string][]byte{
		"wpa_cli -i wlan0 scan_results": []byte(badSignal),
	}}}
	nets, err := c.Scan()
	if err != nil {
		t.Fatal(err)
	}
	// Line with non-numeric signal is skipped silently; only valid Argosity should remain.
	if len(nets) != 1 || nets[0].SSID != "Argosity" {
		t.Errorf("non-numeric signal should be skipped, got %+v", nets)
	}
}

func TestScanDeterministicOrderingOnTiedSignals(t *testing.T) {
	// Run the same scan multiple times and verify identical ordering.
	// Before the fix, equal signals would produce random ordering due to map
	// iteration randomization. After the fix, ties should always sort by SSID.
	const withTies = `bssid / frequency / signal level / flags / ssid
fc:ec:da:f1:e7:e0	2462	-72	[WPA2-PSK-CCMP][ESS]	Zebra
02:ec:da:f1:e7:e0	2462	-72	[ESS]	Alpha
86:25:19:98:5a:b4	2462	-69	[WPA2-PSK-CCMP][ESS]	Beta
`
	var lastOrder []string
	for i := 0; i < 10; i++ {
		c := &Client{R: &fakeRunner{out: map[string][]byte{
			"wpa_cli -i wlan0 scan_results": []byte(withTies),
		}}}
		nets, err := c.Scan()
		if err != nil {
			t.Fatal(err)
		}
		order := make([]string, len(nets))
		for j, n := range nets {
			order[j] = n.SSID
		}
		if lastOrder != nil && !equal(order, lastOrder) {
			t.Errorf("iteration %d: order %v differs from first %v", i, order, lastOrder)
		}
		lastOrder = order
	}
	// Verify the deterministic order is by signal (Beta first), then alphabetically (Alpha, Zebra).
	if lastOrder[0] != "Beta" || lastOrder[1] != "Alpha" || lastOrder[2] != "Zebra" {
		t.Errorf("expected [Beta Alpha Zebra], got %v", lastOrder)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestConnectWritesConfigAndRestarts(t *testing.T) {
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, ConfPath: t.TempDir() + "/wpa_supplicant.conf"}

	if err := c.Connect("HomeNet", "s3cret"); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(c.ConfPath)
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	if !strings.Contains(conf, `ssid="HomeNet"`) {
		t.Errorf("config missing ssid:\n%s", conf)
	}
	if !strings.Contains(conf, `psk="s3cret"`) {
		t.Errorf("config missing psk:\n%s", conf)
	}
	if !strings.Contains(conf, "ctrl_interface=") {
		t.Errorf("config missing ctrl_interface, wpa_cli will not work:\n%s", conf)
	}

	// It must restart with the exact command /etc/init.d/S99wlan0 restart.
	var restarted bool
	for _, call := range f.call {
		if call == "/etc/init.d/S99wlan0 restart" {
			restarted = true
		}
	}
	if !restarted {
		t.Errorf("expected exact call '/etc/init.d/S99wlan0 restart', got calls: %v", f.call)
	}
}

func TestConnectRejectsEmptySSID(t *testing.T) {
	c := &Client{R: &fakeRunner{}, ConfPath: t.TempDir() + "/w.conf"}
	if err := c.Connect("", "pw"); err == nil {
		t.Error("expected an error for an empty SSID")
	}
}

func TestConnectEscapesQuotes(t *testing.T) {
	// A quote in the SSID would otherwise break out of the quoted value and
	// corrupt the config file.
	c := &Client{R: &fakeRunner{}, ConfPath: t.TempDir() + "/w.conf"}
	if err := c.Connect(`My"Net`, `pa"ss`); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.ConfPath)
	conf := string(b)
	if strings.Contains(conf, `ssid="My"Net"`) {
		t.Errorf("unescaped quote corrupts the config:\n%s", conf)
	}
	if !strings.Contains(conf, `\"`) {
		t.Errorf("expected escaped quotes:\n%s", conf)
	}
}

func TestConnectOpenNetworkNoPassword(t *testing.T) {
	// Open networks must have key_mgmt=NONE and no psk line.
	// An empty psk with WPA-PSK is invalid and causes association to fail.
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, ConfPath: t.TempDir() + "/wpa_supplicant.conf"}

	if err := c.Connect("OpenNet", ""); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(c.ConfPath)
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	if !strings.Contains(conf, "key_mgmt=NONE") {
		t.Errorf("open network missing key_mgmt=NONE:\n%s", conf)
	}
	if strings.Contains(conf, "psk=") {
		t.Errorf("open network should not have psk line:\n%s", conf)
	}
	if strings.Contains(conf, "WPA-PSK") {
		t.Errorf("open network should not have WPA-PSK:\n%s", conf)
	}
}

func TestConnectSecuredNetworkHasWPAPSK(t *testing.T) {
	// Secured networks must have key_mgmt=WPA-PSK and a psk line.
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, ConfPath: t.TempDir() + "/wpa_supplicant.conf"}

	if err := c.Connect("SecuredNet", "password123"); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(c.ConfPath)
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	if !strings.Contains(conf, "key_mgmt=WPA-PSK") {
		t.Errorf("secured network missing key_mgmt=WPA-PSK:\n%s", conf)
	}
	if !strings.Contains(conf, `psk="password123"`) {
		t.Errorf("secured network missing psk line:\n%s", conf)
	}
}

func TestMACReadsSysfs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/address", []byte("54:01:4a:4c:1b:fd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{R: &fakeRunner{}, SysfsNet: dir}
	if got := c.MAC(); got != "54:01:4a:4c:1b:fd" {
		t.Errorf("MAC = %q, want the trimmed address", got)
	}
}

func TestMACMissingIsEmpty(t *testing.T) {
	// Driver not loaded: must return empty rather than panic, so the portal
	// falls back to a usable AP name and refuses the MAC default password.
	c := &Client{R: &fakeRunner{}, SysfsNet: t.TempDir()}
	if got := c.MAC(); got != "" {
		t.Errorf("MAC = %q, want empty", got)
	}
}

func TestConnectRestartsWithExactCommand(t *testing.T) {
	// Assert the exact restart invocation, not just a substring match.
	f := &fakeRunner{out: map[string][]byte{}}
	c := &Client{R: f, ConfPath: t.TempDir() + "/wpa_supplicant.conf"}

	if err := c.Connect("TestNet", "testpass"); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, call := range f.call {
		if call == "/etc/init.d/S99wlan0 restart" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected exact call '/etc/init.d/S99wlan0 restart', got calls: %v", f.call)
	}
}
