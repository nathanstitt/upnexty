package main

import (
	"os"
	"path/filepath"
	"testing"
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

func TestApplyStartupBrightnessSkipsNonPositive(t *testing.T) {
	// BrightnessValue() returns 0 for an explicit 0; nothing to write, and the
	// path need not even exist for this to succeed.
	path := filepath.Join(t.TempDir(), "does-not-matter")
	if err := applyStartupBrightness(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no file to be written for v=0, stat err = %v", err)
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
