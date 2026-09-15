package googleauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedNow is an arbitrary but sane instant -- after the board's rdate would
// have run, so nothing here accidentally tests the 1970 clock.
var fixedNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

// newStore builds a store over a temp dir pointed at a test server.
func newStore(t *testing.T, tokenURL string) *Store {
	t.Helper()
	s, err := New(Config{
		Dir:          t.TempDir(),
		ClientID:     "client-id.apps.googleusercontent.com",
		ClientSecret: "client-secret",
		TokenURL:     tokenURL,
		Now:          func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// expiredToken is a stored token whose access token needs refreshing.
func expiredToken(account string) StoredToken {
	return StoredToken{
		Account:      account,
		RefreshToken: "refresh-" + account,
		AccessToken:  "stale-access",
		Expiry:       fixedNow.Add(-time.Hour),
		Scope:        "https://www.googleapis.com/auth/calendar.readonly",
		Obtained:     fixedNow.Add(-48 * time.Hour),
	}
}

// tokenServer answers /token with whatever the handler writes. Every request's
// form values are captured so tests can assert what was actually sent.
type tokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	forms []map[string]string
}

func newTokenServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		f := map[string]string{}
		for k := range r.PostForm {
			f[k] = r.PostForm.Get(k)
		}
		ts.mu.Lock()
		ts.forms = append(ts.forms, f)
		ts.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) calls() []map[string]string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]map[string]string, len(ts.forms))
	copy(out, ts.forms)
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	want := expiredToken("nas@stitt.org")
	want.Expiry = fixedNow.Add(time.Hour).UTC()

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("nas@stitt.org")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Account != want.Account || got.RefreshToken != want.RefreshToken ||
		got.AccessToken != want.AccessToken || got.Scope != want.Scope {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
	if !got.Obtained.Equal(want.Obtained) {
		t.Errorf("Obtained = %v, want %v -- must survive so the 7-day testing expiry is predictable", got.Obtained, want.Obtained)
	}

	accounts, err := s.Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0] != "nas@stitt.org" {
		t.Errorf("Accounts() = %v, want [nas@stitt.org]", accounts)
	}
}

// The account is case-folded for the filename, but the address the user typed
// is what the portal has to display, so it must come back unchanged.
func TestLoadIsCaseInsensitiveButPreservesTypedAddress(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	tok := expiredToken("Nas@Stitt.ORG")
	if err := s.Save(tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("nas@stitt.org")
	if err != nil {
		t.Fatalf("Load with different case: %v", err)
	}
	if got.Account != "Nas@Stitt.ORG" {
		t.Errorf("Account = %q, want the address as typed", got.Account)
	}
}

func TestFileName(t *testing.T) {
	tests := []struct {
		name    string
		account string
		want    string
		wantErr bool
	}{
		{"plain email", "nas@stitt.org", "nas@stitt.org.json", false},
		{"case folded", "NAS@Stitt.Org", "nas@stitt.org.json", false},
		{"surrounding space", "  nas@stitt.org \t", "nas@stitt.org.json", false},
		{"plus addressing kept", "nas+board@stitt.org", "nas+board@stitt.org.json", false},
		{"slashes neutralised", "a/b@c.org", "a_b@c.org.json", false},
		{"backslashes neutralised", `a\b@c.org`, "a_b@c.org.json", false},
		{"traversal neutralised", "../../etc/passwd", ".._.._etc_passwd.json", false},
		{"nul byte neutralised", "nas\x00@stitt.org", "nas_@stitt.org.json", false},
		{"newline neutralised", "nas\n@stitt.org", "nas_@stitt.org.json", false},
		{"non-ascii neutralised", "nås@stitt.org", "n_s@stitt.org.json", false},
		{"empty rejected", "", "", true},
		{"whitespace only rejected", "   ", "", true},
		{"dot rejected", ".", "", true},
		{"dotdot rejected", "..", "", true},
		// Separators become '_', so this is already a safe literal name -- the
		// dot-only rejection below is for the accounts that sanitise to nothing
		// but dots, not for anything that merely contained a slash.
		{"dots and slashes are neutralised, not rejected", "/././", "_._._.json", false},
		{"dot with space rejected", " . ", "", true},
		{"many dots rejected", "....", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fileName(tc.account)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("fileName(%q) = %q, want error", tc.account, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("fileName(%q): %v", tc.account, err)
			}
			if got != tc.want {
				t.Errorf("fileName(%q) = %q, want %q", tc.account, got, tc.want)
			}
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("fileName(%q) = %q contains a path separator", tc.account, got)
			}
		})
	}
}

// The sanitiser must not just produce a safe-looking name -- the resulting path
// has to stay inside the store directory.
func TestPathStaysInsideDir(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	for _, account := range []string{"../../etc/passwd", "a/b@c.org", `..\..\evil`} {
		p, err := s.Path(account)
		if err != nil {
			t.Fatalf("Path(%q): %v", account, err)
		}
		if filepath.Dir(p) != s.cfg.Dir {
			t.Errorf("Path(%q) = %q, escapes %q", account, p, s.cfg.Dir)
		}
	}
}

func TestFilePermissions(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, err := s.Path("nas@stitt.org")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("token file mode = %04o, want 0600", got)
	}
	di, err := os.Stat(s.cfg.Dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("token dir mode = %04o, want 0700", got)
	}
}

// New must tighten a directory that already exists with a looser mode --
// MkdirAll would leave it alone.
func TestNewTightensExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tokens")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := New(Config{Dir: dir, ClientID: "id", ClientSecret: "secret"}); err != nil {
		t.Fatalf("New: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", got)
	}
}

func TestSaveRejectsTokenWithoutRefreshToken(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	tok := expiredToken("nas@stitt.org")
	tok.RefreshToken = ""
	if err := s.Save(tok); err == nil {
		t.Fatal("Save accepted a token with no refresh token")
	}
}

func TestAccessTokenNoStoredToken(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	_, err := s.AccessToken(context.Background(), "nobody@example.com")
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v, want ErrNoToken", err)
	}
	if errors.Is(err, ErrNeedsReauth) || errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, must not also classify as reauth/transient", err)
	}
}

// A valid unexpired token must be served straight from disk -- no request at
// all. The server fails the test if it is touched.
func TestAccessTokenSkipsRefreshWhenFresh(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("token endpoint called for an unexpired token")
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "nope", "expires_in": 3599})
	})
	s := newStore(t, ts.URL)
	tok := expiredToken("nas@stitt.org")
	tok.AccessToken = "still-good"
	tok.Expiry = fixedNow.Add(time.Hour)
	if err := s.Save(tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.AccessToken(context.Background(), "nas@stitt.org")
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "still-good" {
		t.Errorf("AccessToken = %q, want the stored one", got)
	}
}

// Within refreshSkew of expiry the token must be refreshed, not used. This is
// the case that matters on this hardware: a frame takes ~10s, so a token with
// seconds left has expired by the time the request lands.
func TestAccessTokenRefreshesInsideSkewWindow(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "fresh-access", "expires_in": 3599, "token_type": "Bearer",
		})
	})
	s := newStore(t, ts.URL)
	tok := expiredToken("nas@stitt.org")
	tok.AccessToken = "about-to-die"
	tok.Expiry = fixedNow.Add(30 * time.Second) // inside the 60s skew
	if err := s.Save(tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.AccessToken(context.Background(), "nas@stitt.org")
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "fresh-access" {
		t.Errorf("AccessToken = %q, want a refreshed token", got)
	}
}

func TestRefreshSuccess(t *testing.T) {
	// The exact shape Google returned on 2026-09-14.
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":             "new-access",
			"expires_in":               3599,
			"scope":                    "https://www.googleapis.com/auth/calendar.readonly",
			"token_type":               "Bearer",
			"refresh_token":            "rotated-refresh",
			"refresh_token_expires_in": 604799,
		})
	})
	s := newStore(t, ts.URL)
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.EnsureFresh(context.Background(), "nas@stitt.org")
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if got.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want new-access", got.AccessToken)
	}
	if got.RefreshToken != "rotated-refresh" {
		t.Errorf("RefreshToken = %q, want the rotated one", got.RefreshToken)
	}
	wantExpiry := fixedNow.Add(3599 * time.Second).UTC()
	if !got.Expiry.Equal(wantExpiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, wantExpiry)
	}

	// Persisted, not just returned: a restart must not need another refresh.
	reloaded, err := s.Load("nas@stitt.org")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.AccessToken != "new-access" || reloaded.RefreshToken != "rotated-refresh" {
		t.Errorf("refresh not persisted: %+v", reloaded)
	}

	calls := ts.calls()
	if len(calls) != 1 {
		t.Fatalf("token endpoint called %d times, want 1", len(calls))
	}
	want := map[string]string{
		"client_id":     "client-id.apps.googleusercontent.com",
		"client_secret": "client-secret",
		"refresh_token": "refresh-nas@stitt.org",
		"grant_type":    "refresh_token",
	}
	for k, v := range want {
		if calls[0][k] != v {
			t.Errorf("form[%q] = %q, want %q", k, calls[0][k], v)
		}
	}
}

// The one that silently destroys a credential if it is got wrong: Google
// usually omits refresh_token on a refresh response.
func TestRefreshWithoutNewRefreshTokenKeepsTheOldOne(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "new-access",
			"expires_in":   3599,
			"scope":        "https://www.googleapis.com/auth/calendar.readonly",
			"token_type":   "Bearer",
			// no refresh_token -- the common case
		})
	})
	s := newStore(t, ts.URL)
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.EnsureFresh(context.Background(), "nas@stitt.org")
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if got.RefreshToken != "refresh-nas@stitt.org" {
		t.Fatalf("RefreshToken = %q, want the original preserved", got.RefreshToken)
	}
	reloaded, err := s.Load("nas@stitt.org")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.RefreshToken != "refresh-nas@stitt.org" {
		t.Fatalf("persisted RefreshToken = %q, want the original preserved", reloaded.RefreshToken)
	}
	// Obtained must not be reset either -- it is what predicts the 7-day expiry.
	if !reloaded.Obtained.Equal(fixedNow.Add(-48 * time.Hour)) {
		t.Errorf("Obtained = %v, want it carried forward", reloaded.Obtained)
	}
}

// Classification is the load-bearing behaviour: terminal means "stop retrying
// every 30s", and getting it wrong in either direction is expensive.
func TestRefreshClassification(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     any
		raw      string // used instead of body when non-empty
		wantErr  error
		notErr   error
		wantText string
	}{
		{
			name:     "invalid_grant is terminal",
			status:   http.StatusBadRequest,
			body:     map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."},
			wantErr:  ErrNeedsReauth,
			notErr:   ErrTransient,
			wantText: "Token has been expired or revoked.",
		},
		{
			name:    "invalid_grant with no description is still terminal",
			status:  http.StatusBadRequest,
			body:    map[string]any{"error": "invalid_grant"},
			wantErr: ErrNeedsReauth,
			notErr:  ErrTransient,
		},
		{
			// Seen when the app's testing-status 7-day window closes.
			name:     "invalid_grant on a 401 is terminal too",
			status:   http.StatusUnauthorized,
			body:     map[string]any{"error": "invalid_grant", "error_description": "Bad Request"},
			wantErr:  ErrNeedsReauth,
			notErr:   ErrTransient,
			wantText: "Bad Request",
		},
		{
			name:    "500 is transient",
			status:  http.StatusInternalServerError,
			body:    map[string]any{"error": "internal_failure"},
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			name:    "503 with an html body is transient",
			status:  http.StatusServiceUnavailable,
			raw:     "<html>backend error</html>",
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			name:    "429 rate limit is transient",
			status:  http.StatusTooManyRequests,
			body:    map[string]any{"error": "rateLimitExceeded"},
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			// A mistyped client secret is a config problem, not a dead grant --
			// telling the user to reconnect sends them to fix the wrong thing.
			name:    "invalid_client is transient, not a reauth prompt",
			status:  http.StatusUnauthorized,
			body:    map[string]any{"error": "invalid_client"},
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			// Belongs to the device flow, which shares this endpoint. Must not
			// inherit "give up forever".
			name:    "428 authorization_pending is transient",
			status:  http.StatusPreconditionRequired,
			body:    map[string]any{"error": "authorization_pending"},
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			name:    "200 with a captive-portal html body is transient",
			status:  http.StatusOK,
			raw:     "<html>sign in to the network</html>",
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			name:    "200 with no access token is transient",
			status:  http.StatusOK,
			body:    map[string]any{"expires_in": 3599},
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
		{
			name:    "200 with no expiry is transient",
			status:  http.StatusOK,
			body:    map[string]any{"access_token": "a"},
			wantErr: ErrTransient,
			notErr:  ErrNeedsReauth,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.raw != "" {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.raw)
					return
				}
				writeJSON(w, tc.status, tc.body)
			})
			s := newStore(t, ts.URL)
			if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
				t.Fatalf("Save: %v", err)
			}

			_, err := s.AccessToken(context.Background(), "nas@stitt.org")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, tc.wantErr)
			}
			if tc.notErr != nil && errors.Is(err, tc.notErr) {
				t.Errorf("err = %v, must not classify as %v", err, tc.notErr)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantText)
			}
			// No error may ever carry credential material to the panel.
			for _, secret := range []string{"refresh-nas@stitt.org", "client-secret", "stale-access"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("err = %q leaks %q", err, secret)
				}
			}
			// A failed refresh must leave the stored token alone -- a transient
			// 500 that blanked the refresh token would be unrecoverable.
			reloaded, lerr := s.Load("nas@stitt.org")
			if lerr != nil {
				t.Fatalf("Load after failure: %v", lerr)
			}
			if reloaded.RefreshToken != "refresh-nas@stitt.org" {
				t.Errorf("stored RefreshToken = %q, want it untouched by a failed refresh", reloaded.RefreshToken)
			}
		})
	}
}

// A network-level failure (nothing listening) is transient: the board is in
// exactly this state for the first seconds of every boot.
func TestRefreshTransportErrorIsTransient(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {})
	url := ts.URL
	ts.Close() // nothing listening now

	s := newStore(t, url)
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err := s.AccessToken(context.Background(), "nas@stitt.org")
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("err = %v, want ErrTransient", err)
	}
	if errors.Is(err, ErrNeedsReauth) {
		t.Errorf("err = %v, a dead network must not read as a dead grant", err)
	}
	if strings.Contains(err.Error(), "refresh-nas@stitt.org") {
		t.Errorf("err = %q leaks the refresh token", err)
	}
}

func TestCorruptTokenFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{"truncated json", `{"account":"nas@stitt.org","refresh_to`, ErrNeedsReauth},
		{"not json at all", "\x00\xff\x00 garbage", ErrNeedsReauth},
		{"empty file", "", ErrNeedsReauth},
		{"valid json, no refresh token", `{"account":"nas@stitt.org","access_token":"a"}`, ErrNeedsReauth},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t, "http://unused.invalid/token")
			p, err := s.Path("nas@stitt.org")
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			_, err = s.AccessToken(context.Background(), "nas@stitt.org")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if errors.Is(err, ErrTransient) {
				t.Errorf("err = %v, a corrupt file must not be retried forever", err)
			}
			if strings.Contains(err.Error(), tc.content) && tc.content != "" {
				t.Errorf("err = %q includes the file contents, which may be a partial credential", err)
			}

			// It must not break the listing for healthy accounts either.
			if err := s.Save(expiredToken("other@example.com")); err != nil {
				t.Fatalf("Save: %v", err)
			}
			accounts, err := s.Accounts()
			if err != nil {
				t.Fatalf("Accounts: %v", err)
			}
			if len(accounts) != 1 || accounts[0] != "other@example.com" {
				t.Errorf("Accounts() = %v, want only the healthy account", accounts)
			}
		})
	}
}

// The isolation the plan calls the point of this step: one account's revoked
// grant must not touch the other's token or stop it refreshing.
func TestAccountsAreIsolated(t *testing.T) {
	const (
		dead  = "revoked@example.com"
		alive = "working@example.com"
	)
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.PostFormValue("refresh_token") {
		case "refresh-" + dead:
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid_grant", "error_description": "Token has been expired or revoked.",
			})
		case "refresh-" + alive:
			writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "good-access", "expires_in": 3599, "token_type": "Bearer",
			})
		default:
			t.Errorf("unexpected refresh token presented")
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	s := newStore(t, ts.URL)
	for _, a := range []string{dead, alive} {
		if err := s.Save(expiredToken(a)); err != nil {
			t.Fatalf("Save %s: %v", a, err)
		}
	}

	// Order deliberately puts the failure first: the healthy account must work
	// after the dead one has already failed.
	if _, err := s.AccessToken(context.Background(), dead); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("dead account err = %v, want ErrNeedsReauth", err)
	}
	got, err := s.AccessToken(context.Background(), alive)
	if err != nil {
		t.Fatalf("live account: %v", err)
	}
	if got != "good-access" {
		t.Errorf("live AccessToken = %q, want good-access", got)
	}

	// And the reverse: the live account's refresh must not have disturbed the
	// dead one's file, which the portal still needs to list and disconnect.
	deadTok, err := s.Load(dead)
	if err != nil {
		t.Fatalf("Load dead: %v", err)
	}
	if deadTok.RefreshToken != "refresh-"+dead {
		t.Errorf("dead RefreshToken = %q, want it untouched", deadTok.RefreshToken)
	}
	if deadTok.AccessToken != "stale-access" {
		t.Errorf("dead AccessToken = %q, want it untouched by the failed refresh", deadTok.AccessToken)
	}

	// The dead account stays dead across repeat calls -- the caller, not this
	// package, decides the retry interval, so the classification must be stable.
	for i := 0; i < 3; i++ {
		if _, err := s.AccessToken(context.Background(), dead); !errors.Is(err, ErrNeedsReauth) {
			t.Fatalf("call %d: err = %v, want a stable ErrNeedsReauth", i, err)
		}
	}
	liveTok, err := s.Load(alive)
	if err != nil {
		t.Fatalf("Load live: %v", err)
	}
	if liveTok.AccessToken != "good-access" {
		t.Errorf("live AccessToken on disk = %q, want good-access", liveTok.AccessToken)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t, "http://unused.invalid/token")
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete("nas@stitt.org"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("nas@stitt.org"); !errors.Is(err, ErrNoToken) {
		t.Errorf("Load after Delete: err = %v, want ErrNoToken", err)
	}
	// Idempotent: the portal can be submitted twice.
	if err := s.Delete("nas@stitt.org"); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

// Two goroutines wanting the same account's token must produce one refresh, not
// two -- two sources sharing an account is the documented normal setup.
func TestConcurrentRefreshOfSameAccountHitsEndpointOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // widen the race window
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "shared-access", "expires_in": 3599, "token_type": "Bearer",
		})
	})
	s := newStore(t, ts.URL)
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	toks := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			toks[i], errs[i] = s.AccessToken(context.Background(), "nas@stitt.org")
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if toks[i] != "shared-access" {
			t.Errorf("goroutine %d got %q", i, toks[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("token endpoint called %d times, want 1 -- the second caller should reuse the first's result", calls)
	}
}

// Concurrent work across accounts must not corrupt either file. Run with -race.
func TestConcurrentAcrossAccounts(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-for-" + r.PostFormValue("refresh_token"),
			"expires_in":   3599,
			"token_type":   "Bearer",
		})
	})
	s := newStore(t, ts.URL)
	accounts := []string{"a@example.com", "b@example.com", "c@example.com"}
	for _, a := range accounts {
		if err := s.Save(expiredToken(a)); err != nil {
			t.Fatalf("Save %s: %v", a, err)
		}
	}

	var wg sync.WaitGroup
	for range 4 {
		for _, a := range accounts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := s.AccessToken(context.Background(), a)
				if err != nil {
					t.Errorf("%s: %v", a, err)
					return
				}
				if want := "access-for-refresh-" + a; got != want {
					t.Errorf("%s: AccessToken = %q, want %q", a, got, want)
				}
			}()
		}
	}
	wg.Wait()
}

// A cancelled context is transient: the dashboard cancels a fetch when a tap
// re-renders, and that must not be read as a dead grant.
func TestCancelledContextIsTransient(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "a", "expires_in": 3599})
	})
	s := newStore(t, ts.URL)
	if err := s.Save(expiredToken("nas@stitt.org")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.AccessToken(ctx, "nas@stitt.org")
	if !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
	if errors.Is(err, ErrNeedsReauth) {
		t.Errorf("err = %v, a cancelled render must not read as a dead grant", err)
	}
}

func TestNewRequiresDir(t *testing.T) {
	if _, err := New(Config{ClientID: "id"}); err == nil {
		t.Fatal("New with no Dir succeeded")
	}
}
