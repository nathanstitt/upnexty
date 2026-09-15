package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nathanstitt/upnexty/internal/calendar"
	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/googleauth"
	"github.com/nathanstitt/upnexty/internal/portal"
)

// googleLinker adapts googleauth.Store to portal.GoogleLinker.
//
// The adapter exists so neither side depends on the other: the portal knows
// nothing about token files, and googleauth knows nothing about HTTP handlers.
// It also owns the retry policy, which is the one piece of the token lifecycle
// that belongs to the fetch loop rather than to storage -- see AccessToken.
type googleLinker struct {
	store *googleauth.Store

	// mu guards reauth, the per-account backoff below.
	mu sync.Mutex

	// reauth records when an account was last found to need re-authorisation.
	//
	// googleauth reports ErrNeedsReauth cheaply and without a network call, but
	// the CALLER still has to not hammer Google: the calendar loop retries a
	// failed fetch every retryDelay (30s), which against a revoked credential
	// is 2,880 token requests a day that cannot possibly succeed. This throttles
	// the attempt itself, so a dead grant is retried hourly rather than
	// constantly, while a transient failure keeps the fast cadence.
	reauth map[string]time.Time

	now func() time.Time
}

// reauthRetry is how often a credential known to be dead is retried anyway.
//
// Not never: the user reconnects the account from the settings page, which
// writes a new token to the same file, and the fetch loop has no way to be told
// that happened. An hourly probe means a reconnected account recovers on its
// own within the hour even if nothing else prompts a refetch. The portal also
// calls RefetchCalendars on a successful connect, so the common path is
// immediate and this is the backstop.
const reauthRetry = time.Hour

func newGoogleLinker(store *googleauth.Store) *googleLinker {
	return &googleLinker{
		store:  store,
		reauth: map[string]time.Time{},
		now:    time.Now,
	}
}

// AccessToken implements calendar.TokenSource.
//
// This is the retry gate: an account already known to need re-authorisation is
// refused locally, without a request, until reauthRetry has passed.
func (g *googleLinker) AccessToken(ctx context.Context, account string) (string, error) {
	if wait, blocked := g.blocked(account); blocked {
		return "", fmt.Errorf("%w (retrying in %s)", googleauth.ErrNeedsReauth, wait.Round(time.Minute))
	}
	tok, err := g.store.AccessToken(ctx, account)
	g.record(account, err)
	return tok, err
}

// blocked reports whether this account is inside its re-auth backoff.
func (g *googleLinker) blocked(account string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.reauth[account]
	if !ok {
		return 0, false
	}
	elapsed := g.now().Sub(at)
	if elapsed >= reauthRetry {
		return 0, false
	}
	return reauthRetry - elapsed, true
}

// record updates the backoff from a fetch outcome. Any success or transient
// failure clears it: a transient error says nothing about the credential, and
// leaving the mark set would delay recovery by up to an hour for a board that
// was merely offline.
func (g *googleLinker) record(account string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case errors.Is(err, googleauth.ErrNeedsReauth), errors.Is(err, googleauth.ErrNoToken):
		if _, seen := g.reauth[account]; !seen {
			g.reauth[account] = g.now()
		}
	default:
		delete(g.reauth, account)
	}
}

// StartDeviceFlow implements portal.GoogleLinker.
func (g *googleLinker) StartDeviceFlow(ctx context.Context) (portal.PairingCode, any, error) {
	auth, err := g.store.StartDeviceFlow(ctx)
	if err != nil {
		return portal.PairingCode{}, nil, err
	}
	return portal.PairingCode{
		UserCode:        auth.UserCode,
		VerificationURL: auth.VerificationURL,
	}, auth, nil
}

// AwaitToken implements portal.GoogleLinker.
func (g *googleLinker) AwaitToken(ctx context.Context, handle any) (string, error) {
	auth, ok := handle.(googleauth.DeviceAuth)
	if !ok {
		return "", fmt.Errorf("google: internal error: unexpected pairing handle %T", handle)
	}
	tok, err := g.store.PollDeviceFlow(ctx, auth)
	if err != nil {
		return "", err
	}
	// A freshly authorised account starts with a clean slate: whatever state
	// the previous credential was in is irrelevant now.
	g.mu.Lock()
	delete(g.reauth, tok.Account)
	g.mu.Unlock()
	return tok.Account, nil
}

// Accounts implements portal.GoogleLinker.
func (g *googleLinker) Accounts() ([]string, error) { return g.store.Accounts() }

// Disconnect implements portal.GoogleLinker.
func (g *googleLinker) Disconnect(account string) error {
	g.mu.Lock()
	delete(g.reauth, account)
	g.mu.Unlock()
	// Delete, not a "disconnect" of our own: the token file IS the connection,
	// so removing it is the whole operation.
	return g.store.Delete(account)
}

// googleClientFile is where the OAuth application's own credentials live.
//
// Separate from the per-account tokens because it identifies the APP, not a
// user: every account on the board shares it. A file rather than compiled in so
// a board can be built from a public checkout without embedding anything, and
// so rotating the credential does not need a rebuild.
const googleClientFile = "/root/google-client.json"

// googleClient is the on-disk shape, matching the field names Google's own
// downloaded credentials file uses so it can be copied across with minimal
// editing.
type googleClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// loadGoogleLinker builds the linker, or returns nil when the board has no
// OAuth credentials.
//
// A missing file is NOT an error: an iCal-only board is the normal case and
// must not log a failure every boot. The portal hides the connect button when
// this is nil, so the feature is simply absent rather than broken.
func loadGoogleLinker(path string) (*googleLinker, error) {
	if path == "" {
		path = googleClientFile
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("google client credentials: %w", err)
	}
	var gc googleClient
	if err := json.Unmarshal(raw, &gc); err != nil {
		return nil, fmt.Errorf("google client credentials: %w", err)
	}
	if gc.ClientID == "" || gc.ClientSecret == "" {
		return nil, fmt.Errorf("google client credentials: %s has no client_id/client_secret", path)
	}
	store, err := googleauth.New(googleauth.Config{
		Dir:          googleauth.DefaultDir,
		ClientID:     gc.ClientID,
		ClientSecret: gc.ClientSecret,
	})
	if err != nil {
		return nil, err
	}
	return newGoogleLinker(store), nil
}

// clockIsSane reports whether the board's clock has been set.
//
// The board has no RTC and boots at 1970 until S99wlan0 runs rdate. Both OAuth
// expiry arithmetic and the TLS handshake to Google depend on the clock, so a
// Google fetch attempted before then fails -- harmlessly, since googleauth
// classifies it transient and the next cycle retries, but noisily enough to
// fill the error list on the panel for no reason.
//
// The gate lives here rather than in googleauth because it is a property of
// this hardware's boot sequence, not of OAuth.
func clockIsSane(now time.Time) bool { return now.Year() > 2020 }

// skipCalendar reports whether a source should not be fetched this cycle.
//
// Two reasons, and they are not symmetrical. A placeholder iCal URL is a source
// that was never configured -- it is skipped forever, silently, because the
// sample config ships with one. The clock gate is temporary: a Google source is
// skipped only until rdate runs, and the very next cycle picks it up.
//
// Neither records an error. A skipped source contributes no feedResult, so its
// cache is dropped rather than served stale -- which is right for a source that
// does not exist, and harmless for the seconds before the clock is set, when
// there is nothing cached anyway.
func skipCalendar(src config.CalendarSource, now time.Time) bool {
	switch src.SourceKind() {
	case config.KindGoogle:
		return src.CalID == "" || !clockIsSane(now)
	default:
		return src.URL == "" || (len(src.URL) > 6 && src.URL[:6] == "PASTE_")
	}
}

// googleTokenSource returns the shared linker as a calendar.TokenSource, or a
// nil interface when the board has none.
//
// The explicit nil matters: returning a typed nil pointer would give
// fetchGoogle a non-nil interface holding nothing, and it would fail on a
// method call instead of its own clear "no token source configured" check.
func googleTokenSource() calendar.TokenSource {
	if googleLink == nil {
		return nil
	}
	return googleLink
}
