package googleauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// deviceFlowServer answers the three endpoints a flow touches, with the
// token endpoint driven by a script of successive replies.
type deviceFlowServer struct {
	*httptest.Server
	tokenCalls    int
	tokenReply    []func(w http.ResponseWriter)
	userEmail     string
	accountStatus int
	deviceCode    map[string]any
}

func newDeviceFlowServer(t *testing.T, s *deviceFlowServer) *deviceFlowServer {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		if s.deviceCode == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"device_code": "dev-code", "user_code": "VSBR-DGFB",
				"expires_in": 1800, "interval": 5,
				"verification_url": "https://www.google.com/device",
			})
			return
		}
		writeJSON(w, http.StatusOK, s.deviceCode)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		i := s.tokenCalls
		s.tokenCalls++
		if i < len(s.tokenReply) {
			s.tokenReply[i](w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "acc", "refresh_token": "ref", "expires_in": 3599,
			"scope": CalendarReadonlyScope, "token_type": "Bearer",
		})
	})
	// The account comes from calendarList, whose primary entry's id is the
	// address. See DefaultUserInfoURL for why the identity endpoint is not
	// usable with a calendar-only scope.
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("account lookup Authorization = %q, want a bearer token", got)
		}
		if s.accountStatus != 0 {
			writeJSON(w, s.accountStatus, map[string]any{
				"error": map[string]any{"code": s.accountStatus, "message": "Unauthorized"},
			})
			return
		}
		email := s.userEmail
		if email == "" {
			email = "nas@stitt.org"
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": []map[string]any{
			{"id": "en.usa#holiday@group.v.calendar.google.com", "primary": false},
			{"id": email, "primary": true},
		}})
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func newFlowStore(t *testing.T, srv *deviceFlowServer) *Store {
	t.Helper()
	st, err := New(Config{
		Dir:           t.TempDir(),
		ClientID:      "client-id.apps.googleusercontent.com",
		ClientSecret:  "secret",
		TokenURL:      srv.URL + "/token",
		DeviceCodeURL: srv.URL + "/device/code",
		UserInfoURL:   srv.URL + "/userinfo",
		Now:           func() time.Time { return fixedNow },
		// Fire immediately: a real 5s interval would make these tests take
		// minutes for no added coverage.
		After: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- fixedNow
			return ch
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStartDeviceFlowReturnsTheCode(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{})
	s := newFlowStore(t, srv)

	auth, err := s.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserCode != "VSBR-DGFB" {
		t.Errorf("UserCode = %q", auth.UserCode)
	}
	if auth.VerificationURL != "https://www.google.com/device" {
		t.Errorf("VerificationURL = %q", auth.VerificationURL)
	}
	if auth.DeviceCode != "dev-code" {
		t.Errorf("DeviceCode = %q", auth.DeviceCode)
	}
	if auth.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want Google's 5s", auth.Interval)
	}
	if !auth.Expiry.Equal(fixedNow.Add(30 * time.Minute)) {
		t.Errorf("Expiry = %v, want now+expires_in", auth.Expiry)
	}
}

// Google sends verification_url; RFC 8628 specifies verification_uri. Decoding
// only one of them puts an empty string on the panel.
func TestStartDeviceFlowAcceptsEitherURLSpelling(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		deviceCode: map[string]any{
			"device_code": "d", "user_code": "U-CODE", "expires_in": 1800, "interval": 5,
			"verification_uri": "https://example.test/device", // RFC spelling only
		},
	})
	s := newFlowStore(t, srv)
	auth, err := s.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth.VerificationURL != "https://example.test/device" {
		t.Errorf("VerificationURL = %q, want the verification_uri value", auth.VerificationURL)
	}
}

// A server that omits the interval must not turn the poll into a hot loop.
func TestStartDeviceFlowFloorsTheInterval(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		deviceCode: map[string]any{
			"device_code": "d", "user_code": "U", "expires_in": 1800,
			"verification_url": "https://example.test/device",
		},
	})
	s := newFlowStore(t, srv)
	auth, err := s.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth.Interval < time.Second {
		t.Errorf("Interval = %v; a missing interval must not poll flat out", auth.Interval)
	}
}

// The whole point of the flow: poll through the pending answers, then store a
// token named for the account Google says it belongs to.
func TestPollDeviceFlowStoresTheTokenAfterPending(t *testing.T) {
	pending := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusPreconditionRequired, map[string]any{"error": "authorization_pending"})
	}
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){pending, pending},
		userEmail:  "nas@stitt.org",
	})
	s := newFlowStore(t, srv)

	auth, err := s.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.PollDeviceFlow(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Account != "nas@stitt.org" {
		t.Errorf("Account = %q, want the address userinfo reported", tok.Account)
	}
	if tok.RefreshToken != "ref" {
		t.Errorf("RefreshToken not carried through")
	}
	if srv.tokenCalls != 3 {
		t.Errorf("token endpoint called %d times, want 3 (two pending then success)", srv.tokenCalls)
	}

	// And it must be on disk under that account, ready for the next fetch.
	got, err := s.Load("nas@stitt.org")
	if err != nil {
		t.Fatalf("token was not persisted: %v", err)
	}
	if got.AccessToken != "acc" {
		t.Errorf("stored AccessToken = %q", got.AccessToken)
	}
	path := filepath.Join(s.cfg.Dir, "nas@stitt.org.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// slow_down means "you are polling too fast" -- it must widen the interval and
// keep going, not abort the flow.
func TestPollDeviceFlowHandlesSlowDown(t *testing.T) {
	var intervals []time.Duration
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){
			// Two slow_downs, so the interval is observed widening twice before
			// the flow succeeds -- one would only prove it did not crash.
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "slow_down"})
			},
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "slow_down"})
			},
		},
	})
	st, err := New(Config{
		Dir: t.TempDir(), ClientID: "c", ClientSecret: "s",
		TokenURL: srv.URL + "/token", DeviceCodeURL: srv.URL + "/device/code",
		UserInfoURL: srv.URL + "/userinfo",
		Now:         func() time.Time { return fixedNow },
		After: func(d time.Duration) <-chan time.Time {
			intervals = append(intervals, d)
			ch := make(chan time.Time, 1)
			ch <- fixedNow
			return ch
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	auth, err := st.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PollDeviceFlow(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	if len(intervals) < 2 {
		t.Fatalf("polled %d times, want at least 2", len(intervals))
	}
	if intervals[1] <= intervals[0] {
		t.Errorf("interval after slow_down = %v, was %v -- it must widen",
			intervals[1], intervals[0])
	}
}

// An expired code is its own outcome: nothing is broken and no credential was
// revoked, so it must not read as "reconnect your account".
func TestPollDeviceFlowReportsExpiry(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expired_token"})
			},
		},
	})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())
	_, err := s.PollDeviceFlow(context.Background(), auth)
	if !errors.Is(err, ErrAuthorizationExpired) {
		t.Errorf("err = %v, want ErrAuthorizationExpired", err)
	}
	if errors.Is(err, ErrNeedsReauth) {
		t.Error("an expired code must not be reported as a dead credential")
	}
}

// Declining at the consent screen is a decision, not a fault.
func TestPollDeviceFlowReportsDenial(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "access_denied"})
			},
		},
	})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())
	if _, err := s.PollDeviceFlow(context.Background(), auth); !errors.Is(err, ErrNeedsReauth) {
		t.Errorf("err = %v, want ErrNeedsReauth", err)
	}
}

// A grant with no refresh token is unusable on a panel: the access token dies
// in an hour and nobody is standing at the board to redo the flow.
func TestPollDeviceFlowRejectsAGrantWithoutARefreshToken(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusOK, map[string]any{
					"access_token": "acc", "expires_in": 3599, "token_type": "Bearer",
				})
			},
		},
	})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())
	_, err := s.PollDeviceFlow(context.Background(), auth)
	if err == nil {
		t.Fatal("a grant with no refresh token was accepted")
	}
	if !strings.Contains(err.Error(), "refresh token") {
		t.Errorf("err = %v, want it to name the missing refresh token", err)
	}
}

// Cancelling must end the poll rather than run to the code's 30-minute expiry.
func TestPollDeviceFlowStopsOnContextCancel(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusPreconditionRequired, map[string]any{"error": "authorization_pending"})
			},
		},
	})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := s.PollDeviceFlow(ctx, auth); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled poll returned success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled poll did not return")
	}
}

// No token value may appear in any error: these strings reach the settings page.
func TestDeviceFlowErrorsCarryNoSecrets(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{
		tokenReply: []func(http.ResponseWriter){
			func(w http.ResponseWriter) {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "invalid_client", "error_description": "bad client",
				})
			},
		},
	})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())
	_, err := s.PollDeviceFlow(context.Background(), auth)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"secret", "dev-code", "ref", "acc"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

// A token that cannot be attributed to an account must fail loudly, not be
// stored under an empty name.
//
// This is the failure the first real authorisation hit: the flow succeeded,
// Google issued a token, and the account lookup 401'd -- so the token was
// discarded after a successful sign-in, with the user told only "temporary
// google authorization failure". The lookup endpoint has since changed to one
// the granted scope can actually reach.
//
// Note what this test does NOT cover: the fake server answers whatever path it
// is given, so pointing DefaultUserInfoURL back at the identity endpoint still
// passes here. Only a live token proves which endpoint the granted scope can
// reach, and that check was done by hand -- userinfo 401s while calendarList
// returns 200 for the same token.
func TestPollDeviceFlowFailsWhenTheAccountCannotBeIdentified(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{accountStatus: http.StatusUnauthorized})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())

	_, err := s.PollDeviceFlow(context.Background(), auth)
	if err == nil {
		t.Fatal("a token with no identifiable account was accepted")
	}
	if !strings.Contains(err.Error(), "account") {
		t.Errorf("err = %v, want it to name the account lookup", err)
	}
	// And nothing may be left on disk under a blank name.
	if accounts, _ := s.Accounts(); len(accounts) != 0 {
		t.Errorf("accounts = %v, want none stored", accounts)
	}
}

// The account is the primary calendar's id; a non-primary entry must not be
// mistaken for it. A shared holiday calendar appearing first in the list is
// the normal case, not a contrived one.
func TestAccountLookupUsesThePrimaryCalendar(t *testing.T) {
	srv := newDeviceFlowServer(t, &deviceFlowServer{userEmail: "nas@stitt.org"})
	s := newFlowStore(t, srv)
	auth, _ := s.StartDeviceFlow(context.Background())
	tok, err := s.PollDeviceFlow(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Account != "nas@stitt.org" {
		t.Errorf("Account = %q, want the primary calendar's id", tok.Account)
	}
}
