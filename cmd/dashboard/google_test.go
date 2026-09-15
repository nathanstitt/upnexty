package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/googleauth"
)

// A dead credential must not be retried on the calendar loop's 30s cadence:
// that is 2,880 token requests a day against a grant that cannot recover, and
// the quota it burns belongs to a project the user did not sign up to babysit.
func TestAccessTokenBacksOffAfterNeedsReauth(t *testing.T) {
	store, err := googleauth.New(googleauth.Config{
		Dir: t.TempDir(), ClientID: "c", ClientSecret: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := newGoogleLinker(store)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }

	// No token stored: the store answers ErrNoToken without a network call,
	// which is the same "needs the user" class as a revoked grant.
	if _, err := g.AccessToken(context.Background(), "me@example.com"); err == nil {
		t.Fatal("expected an error for an account with no token")
	}
	if _, blocked := g.blocked("me@example.com"); !blocked {
		t.Fatal("the account was not marked for backoff")
	}

	// Inside the window the attempt is refused locally.
	_, err = g.AccessToken(context.Background(), "me@example.com")
	if !errors.Is(err, googleauth.ErrNeedsReauth) {
		t.Errorf("err = %v, want ErrNeedsReauth", err)
	}

	// And it clears once the window passes, so a reconnected account recovers
	// without anyone restarting the service.
	now = now.Add(reauthRetry + time.Minute)
	if _, blocked := g.blocked("me@example.com"); blocked {
		t.Error("still blocked after reauthRetry elapsed")
	}
}

// One account's dead credential must not stall another's.
func TestBackoffIsPerAccount(t *testing.T) {
	store, err := googleauth.New(googleauth.Config{
		Dir: t.TempDir(), ClientID: "c", ClientSecret: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := newGoogleLinker(store)
	g.now = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }

	g.AccessToken(context.Background(), "dead@example.com")
	if _, blocked := g.blocked("dead@example.com"); !blocked {
		t.Fatal("the failing account was not blocked")
	}
	if _, blocked := g.blocked("other@example.com"); blocked {
		t.Error("an unrelated account was blocked by another's failure")
	}
}

// A transient failure says nothing about the credential. Leaving a mark would
// delay recovery by up to an hour for a board that was merely offline.
func TestTransientFailureClearsTheBackoff(t *testing.T) {
	g := &googleLinker{reauth: map[string]time.Time{}, now: time.Now}
	g.record("me@example.com", fmt.Errorf("wrapped: %w", googleauth.ErrNeedsReauth))
	if _, blocked := g.blocked("me@example.com"); !blocked {
		t.Fatal("not blocked after ErrNeedsReauth")
	}
	g.record("me@example.com", fmt.Errorf("wrapped: %w", googleauth.ErrTransient))
	if _, blocked := g.blocked("me@example.com"); blocked {
		t.Error("still blocked after a transient failure; a board that was offline would wait an hour")
	}
}

// Reconnecting through the portal has to clear a block the fetch loop set --
// they share one linker precisely so this works.
func TestDisconnectClearsTheBackoff(t *testing.T) {
	dir := t.TempDir()
	store, err := googleauth.New(googleauth.Config{Dir: dir, ClientID: "c", ClientSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	g := newGoogleLinker(store)
	g.record("me@example.com", googleauth.ErrNeedsReauth)
	if _, blocked := g.blocked("me@example.com"); !blocked {
		t.Fatal("not blocked")
	}
	if err := g.Disconnect("me@example.com"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, blocked := g.blocked("me@example.com"); blocked {
		t.Error("disconnect left the backoff in place")
	}
}

// The board boots at 1970 until rdate runs. A Google fetch before then fails
// on the TLS handshake, so it is skipped rather than attempted and logged.
func TestSkipCalendarGatesGoogleOnTheClock(t *testing.T) {
	google := config.CalendarSource{Name: "G", Kind: config.KindGoogle, CalID: "me@example.com"}
	ical := config.CalendarSource{Name: "I", URL: "https://example.com/a.ics"}

	epoch := time.Date(1970, 1, 1, 0, 0, 7, 0, time.UTC)
	real := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	if !skipCalendar(google, epoch) {
		t.Error("a Google source was attempted before the clock was set")
	}
	if skipCalendar(google, real) {
		t.Error("a Google source was skipped with a good clock")
	}
	// The iCal path does not care: its TLS works at 1970 because the feed is
	// fetched with whatever the system trusts, and it has no expiry arithmetic.
	if skipCalendar(ical, epoch) {
		t.Error("an iCal source was gated on the clock; only Google needs that")
	}
}

func TestSkipCalendarSkipsUnconfiguredSources(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		src  config.CalendarSource
		want bool
	}{
		{"placeholder url", config.CalendarSource{URL: "PASTE_YOUR_URL_HERE"}, true},
		{"empty url", config.CalendarSource{}, true},
		{"real url", config.CalendarSource{URL: "https://example.com/a.ics"}, false},
		{"google without cal_id", config.CalendarSource{Kind: config.KindGoogle}, true},
		{"google with cal_id", config.CalendarSource{Kind: config.KindGoogle, CalID: "x@y.com"}, false},
	} {
		if got := skipCalendar(tc.src, now); got != tc.want {
			t.Errorf("%s: skipCalendar = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A board with no credentials file is the normal case, not a failure.
func TestLoadGoogleLinkerAbsentIsNotAnError(t *testing.T) {
	gl, err := loadGoogleLinker(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("a missing credentials file was an error: %v", err)
	}
	if gl != nil {
		t.Error("a linker was built without credentials")
	}
}

// A file that exists but is unusable IS worth reporting: someone put it there
// on purpose and it does not work.
func TestLoadGoogleLinkerRejectsAnEmptyCredentialsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(p, []byte(`{"client_id":"","client_secret":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGoogleLinker(p); err == nil {
		t.Error("an empty credentials file was accepted")
	}
}

// A typed nil in an interface is not nil. Returning googleLink directly would
// hand fetchGoogle a non-nil TokenSource holding nothing.
func TestGoogleTokenSourceIsUntypedNilWhenAbsent(t *testing.T) {
	saved := googleLink
	t.Cleanup(func() { googleLink = saved })

	googleLink = nil
	if ts := googleTokenSource(); ts != nil {
		t.Error("googleTokenSource returned a non-nil interface for a board with no linker")
	}
}
