package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanstitt/upnexty/internal/wifi"
)

func TestApplyStartupBrightnessWritesValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brightness")
	if err := applyStartupBrightness(path, 120); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "120" {
		t.Errorf("brightness file = %q, want 120", b)
	}
}

func TestApplyStartupBrightnessWritesExplicitZero(t *testing.T) {
	// An explicit 0 means "deliberately off" (see config.Config.Display.
	// Brightness's *int) and must survive a reboot exactly like any other
	// value -- it must be written, not skipped.
	path := filepath.Join(t.TempDir(), "brightness")
	if err := applyStartupBrightness(path, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "0" {
		t.Errorf("brightness file = %q, want 0", b)
	}
}

func TestApplyStartupBrightnessClampsOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brightness")
	if err := applyStartupBrightness(path, 999); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "255" {
		t.Errorf("brightness file = %q, want clamped 255", b)
	}

	if err := applyStartupBrightness(path, -5); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "0" {
		t.Errorf("brightness file = %q, want clamped 0", b)
	}
}

func TestApplyStartupBrightnessPropagatesWriteError(t *testing.T) {
	// A directory that does not exist: os.WriteFile must fail rather than the
	// helper swallowing the error -- callers decide whether to log and
	// continue, but they need the error to log.
	path := filepath.Join(t.TempDir(), "missing-dir", "brightness")
	if err := applyStartupBrightness(path, 120); err == nil {
		t.Error("expected an error writing to a nonexistent directory")
	}
}

// TestSetupHintTrackerHidesWhenConnected covers the case this fix round
// exists for: a saved SSID is not evidence of a working connection (the
// portal sets it the instant credentials are saved, before association is
// attempted -- see internal/portal/handlers.go's handleSaveWiFi), so the
// tracker must gate on live Status().Connected instead. A genuinely
// connected poll hides the hint immediately, with no hysteresis delay.
func TestSetupHintTrackerHidesWhenConnected(t *testing.T) {
	tr := &setupHintTracker{}
	got := tr.Update(wifi.Status{Connected: true, SSID: "Argosity"}, nil, "aa:bb:cc:dd:ee:ff")
	if got != nil {
		t.Errorf("Update = %+v, want nil while connected", got)
	}
}

// TestSetupHintTrackerShowsAfterSustainedDisconnect covers the wrong-
// password / AP-fallback recovery case: the board is not connected on two
// consecutive polls (setupHintMissThreshold), so the hint must show with the
// live MAC-derived AP name and password.
func TestSetupHintTrackerShowsAfterSustainedDisconnect(t *testing.T) {
	tr := &setupHintTracker{}
	mac := "aa:bb:cc:dd:1b:fd"
	for i := 0; i < setupHintMissThreshold-1; i++ {
		if got := tr.Update(wifi.Status{Connected: false}, nil, mac); got != nil {
			t.Fatalf("Update poll %d = %+v, want nil before the miss threshold", i, got)
		}
	}
	got := tr.Update(wifi.Status{Connected: false}, nil, mac)
	if got == nil {
		t.Fatal("Update = nil, want the hint after sustained disconnect")
	}
	if got.APName != wifi.APName(mac) || got.Password == "" {
		t.Errorf("Update = %+v, want APName/Password derived from %q", got, mac)
	}
}

// TestSetupHintTrackerShowsImmediatelyOnStatusError covers "fail toward
// showing the hint": a Status() error (wpa_cli missing, supplicant not
// running) is not evidence the board is fine, so it must show the hint on
// the very first poll, bypassing the miss-threshold hysteresis entirely --
// that hysteresis exists only to smooth over ordinary re-scan blips on a
// board that Status() can actually observe.
func TestSetupHintTrackerShowsImmediatelyOnStatusError(t *testing.T) {
	tr := &setupHintTracker{}
	got := tr.Update(wifi.Status{}, errors.New("wpa_cli: no such device"), "aa:bb:cc:dd:1b:fd")
	if got == nil {
		t.Fatal("Update = nil, want the hint on the first Status() error")
	}
}

// TestSetupHintTrackerDoesNotFlapOnASingleMiss is the "no flap" requirement:
// a healthy board's ordinary wpa_supplicant re-scan reports a transient
// non-COMPLETED state for one poll without ever actually losing the
// connection. A single miss must not surface the hint -- only
// setupHintMissThreshold consecutive misses may.
func TestSetupHintTrackerDoesNotFlapOnASingleMiss(t *testing.T) {
	tr := &setupHintTracker{}
	if got := tr.Update(wifi.Status{Connected: false}, nil, "aa:bb:cc:dd:1b:fd"); got != nil {
		t.Errorf("Update = %+v, want nil on the first miss (threshold is %d)", got, setupHintMissThreshold)
	}
}

// TestSetupHintTrackerRecoveryResetsMissCounter confirms a connected poll
// between two misses resets the counter rather than the tracker
// accumulating misses across separate blips -- otherwise two blips a week
// apart could combine to spuriously trip the threshold.
func TestSetupHintTrackerRecoveryResetsMissCounter(t *testing.T) {
	tr := &setupHintTracker{}
	mac := "aa:bb:cc:dd:1b:fd"
	if got := tr.Update(wifi.Status{Connected: false}, nil, mac); got != nil {
		t.Fatalf("Update (miss 1) = %+v, want nil", got)
	}
	if got := tr.Update(wifi.Status{Connected: true}, nil, mac); got != nil {
		t.Fatalf("Update (connected) = %+v, want nil", got)
	}
	// A second isolated miss must not immediately show the hint: the earlier
	// miss must have been forgotten on the connected poll in between.
	if got := tr.Update(wifi.Status{Connected: false}, nil, mac); got != nil {
		t.Errorf("Update (miss after reset) = %+v, want nil -- counter should have reset on the connected poll", got)
	}
}
