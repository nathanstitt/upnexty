package portal

import "testing"

func TestDefaultPasswordFromMAC(t *testing.T) {
	got := DefaultPassword("54:01:4a:4c:1b:fd")
	if got != "4c1bfd" {
		t.Errorf("DefaultPassword = %q, want 4c1bfd", got)
	}
}

func TestDefaultPasswordNormalizes(t *testing.T) {
	// Uppercase, and a dash-separated form some tools emit.
	for _, mac := range []string{"54:01:4A:4C:1B:FD", "54-01-4a-4c-1b-fd"} {
		if got := DefaultPassword(mac); got != "4c1bfd" {
			t.Errorf("DefaultPassword(%q) = %q, want 4c1bfd", mac, got)
		}
	}
}

func TestDefaultPasswordEmptyMAC(t *testing.T) {
	// No MAC (driver not loaded) must not panic or return a guessable constant.
	if got := DefaultPassword(""); got != "" {
		t.Errorf("DefaultPassword(\"\") = %q, want empty", got)
	}
}

func TestCheckPasswordUsesMACWhenNoHashSet(t *testing.T) {
	mac := "54:01:4a:4c:1b:fd"
	if !CheckPassword("4c1bfd", "", mac) {
		t.Error("MAC default should be accepted when no hash is set")
	}
	if CheckPassword("wrong", "", mac) {
		t.Error("wrong password accepted against the MAC default")
	}
}

func TestCheckPasswordUsesHashWhenSet(t *testing.T) {
	mac := "54:01:4a:4c:1b:fd"
	h := HashPassword("chosen-by-user")

	if !CheckPassword("chosen-by-user", h, mac) {
		t.Error("the configured password should be accepted")
	}
	// Once a password is set, the MAC default must stop working.
	if CheckPassword("4c1bfd", h, mac) {
		t.Error("MAC default still accepted after a password was set")
	}
}

func TestHashIsNotPlaintext(t *testing.T) {
	h := HashPassword("hunter2")
	if h == "hunter2" || h == "" {
		t.Errorf("hash = %q, must be neither the plaintext nor empty", h)
	}
	if HashPassword("hunter2") != h {
		t.Error("hashing must be deterministic")
	}
}

func TestEmptyPasswordNeverAuthenticates(t *testing.T) {
	mac := "54:01:4a:4c:1b:fd"
	if CheckPassword("", "", mac) {
		t.Error("empty password accepted against the MAC default")
	}
	if CheckPassword("", HashPassword("x"), mac) {
		t.Error("empty password accepted against a set hash")
	}
	// And with no MAC available either -- nothing should authenticate.
	if CheckPassword("", "", "") {
		t.Error("empty password accepted with no MAC and no hash")
	}
}
