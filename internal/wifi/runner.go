// Package wifi wraps the board's wpa_cli/hostapd/dnsmasq tooling.
//
// Every command goes through Runner so tests never touch the hardware. That is
// a hard requirement here rather than a nicety: a test that shelled out for
// real could tear down wlan0, and adb rides the same USB/WiFi stack -- taking
// out the interface can strand the board entirely.
package wifi

import "os/exec"

// Runner executes a command and returns its combined output.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands for real. Used on the board.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
