// Package googleauth stores Google OAuth tokens per account and refreshes
// them against Google's token endpoint.
//
// Tokens are kept one file per account rather than inside config.json for two
// reasons. The portal serves config-derived state to a browser, and a refresh
// token is a bearer credential that must never reach a page; and a refresh
// failure is per account -- one dead grant must not stop another account's
// calendars from updating. Separate files make both properties structural
// rather than something every caller has to remember.
//
// Scope note: this package does storage, refresh and failure classification
// only. Obtaining the first token (the RFC 8628 device flow) is a separate
// concern; StoredToken is shaped so that code can write one here and this
// package will keep it refreshed from then on.
package googleauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultTokenURL is Google's OAuth 2.0 token endpoint. Injectable through
// Config.TokenURL so tests can point at an httptest.Server -- there is no way
// to exercise the refresh paths, and especially the invalid_grant path, against
// the real endpoint without deliberately burning a credential.
const DefaultTokenURL = "https://oauth2.googleapis.com/token"

// DefaultDir is where tokens live on the board. /root is the persistent UBI
// volume; /tmp and /var are tmpfs and would lose the grant on every reboot.
const DefaultDir = "/root/google-tokens"

// refreshSkew is how far before expiry a token is treated as already expired.
//
// A frame takes ~10s on this hardware and the calendar fetch sits inside it, so
// a token with 5 seconds left will have expired by the time the request lands.
// A minute is Google's own suggested margin and is far shorter than the ~1h
// token lifetime, so this costs at most one extra refresh per hour.
const refreshSkew = 60 * time.Second

// Sentinel errors. Callers classify with errors.Is rather than by string, and
// the distinction drives retry policy: ErrNeedsReauth must not be retried on
// the 30s calendar cadence, because the credential cannot recover on its own
// and 2,880 requests a day against a dead grant is how a small project meets
// Google's quota limits.
var (
	// ErrNeedsReauth reports a credential that will never work again without
	// the user re-authorising. Google signals this as invalid_grant, which
	// covers a revoked grant, a password change, and -- the one that bites on
	// a wall-mounted panel -- the 7-day refresh-token expiry that applies
	// while the OAuth app is in "testing" status.
	ErrNeedsReauth = errors.New("google account needs to be reconnected")

	// ErrTransient reports a failure that may well succeed on the next try:
	// no network, a DNS failure, a 5xx, a timeout. The board boots without a
	// network and associates seconds later, so this is the expected state at
	// startup rather than an exceptional one.
	ErrTransient = errors.New("temporary google authorization failure")

	// ErrNoToken reports that no token is stored for an account. Distinct from
	// ErrNeedsReauth only in what the UI should say: "connect this account"
	// versus "reconnect it". Both need the same user action.
	ErrNoToken = errors.New("no stored google token for account")
)

// StoredToken is what lives in <dir>/<account>.json.
//
// Expiry is stored as an absolute instant rather than the expires_in seconds
// Google returns, because a duration is only meaningful relative to when the
// response arrived and nothing persists that. The board has no RTC and boots at
// 1970 until S99wlan0 runs rdate, so a token saved before the clock is set
// carries a nonsense Expiry -- which is safe in the direction that matters: an
// Expiry in the past causes a refresh, and a refresh against a live network is
// exactly what recovers.
type StoredToken struct {
	// Account is the email address this token authorises. Stored inside the
	// file as well as encoded in its name so a token that gets copied to the
	// wrong filename is detectable rather than silently mis-attributed.
	Account string `json:"account"`

	// RefreshToken is the long-lived credential. This is the value that must
	// never be logged, rendered, or included in an error.
	RefreshToken string `json:"refresh_token"`

	// AccessToken is the ~1h bearer token. Persisted rather than held only in
	// memory so a dashboard restart does not spend a refresh call; the board
	// restarts the service far more often than tokens expire.
	AccessToken string `json:"access_token"`

	// Expiry is when AccessToken stops working, absolute and in UTC.
	Expiry time.Time `json:"expiry"`

	// Scope is what Google says was granted, which is not necessarily what was
	// asked for. Recorded for diagnosis: a calendar that 403s on every request
	// with a perfectly valid token is a scope problem, and without this there
	// is nothing on the board to look at.
	Scope string `json:"scope,omitempty"`

	// Obtained is when this token was first authorised -- carried forward
	// across refreshes, not reset by them. While the OAuth app is in "testing"
	// status Google expires the refresh token 7 days after authorisation, so
	// this is what lets a caller say "this will die on Tuesday" instead of
	// discovering it at the moment it dies.
	Obtained time.Time `json:"obtained,omitempty"`
}

// Valid reports whether the access token can be used right now, with the skew
// margin applied. A token with no expiry recorded is treated as expired: the
// only way to produce one is a hand-written or partially-written file, and
// refreshing an already-good token costs one request while trusting a dead one
// costs a failed fetch.
func (t StoredToken) Valid(now time.Time) bool {
	return t.AccessToken != "" && !t.Expiry.IsZero() && now.Add(refreshSkew).Before(t.Expiry)
}

// Config identifies the OAuth application and locates the store.
//
// ClientID and ClientSecret identify the *app*, not the user, and are shared by
// every account -- which is why they are passed in rather than read from a
// per-account file. On the board they come from /root/google-client.json or are
// compiled in; this package deliberately does not care which, so nothing here
// has to be changed when that decision is made.
type Config struct {
	Dir          string
	ClientID     string
	ClientSecret string

	// TokenURL overrides DefaultTokenURL. Tests set it; production leaves it
	// empty.
	TokenURL string

	// HTTPClient overrides the default. The default carries a 30s timeout
	// because http.DefaultClient has none at all: a stalled TLS handshake on a
	// flaky AP would otherwise hang a refresh, and the refresh is called from
	// the render path.
	HTTPClient *http.Client

	// Now overrides time.Now, for tests that need to sit either side of an
	// expiry boundary without sleeping.
	Now func() time.Time
}

// Store is the per-account token store. Safe for concurrent use: the dashboard
// fetches calendars from several goroutines and two sources can share one
// account, so two simultaneous refreshes of the same credential is the normal
// case, not an edge case.
type Store struct {
	cfg Config

	// mu guards accounts. Held only while looking up or creating an account's
	// entry -- never across a network call, or one account's slow refresh would
	// block every other account's.
	mu       sync.Mutex
	accounts map[string]*account
}

// account holds the per-account serialisation. Its mutex is what makes two
// concurrent refreshes of the same credential safe *and* cheap: the second
// caller blocks, and by the time it re-reads the file the first has already
// written a fresh token, so it returns that instead of spending a second
// refresh call on the same grant.
//
// A single store-wide mutex was the obvious alternative and is wrong here: it
// would serialise unrelated accounts behind each other's network calls, which
// is precisely the isolation this design exists to provide.
type account struct {
	mu sync.Mutex
}

// New creates a store. The directory is created 0700 if missing -- tokens are
// bearer credentials and the mode is the only thing standing between them and
// any other process on the board.
func New(cfg Config) (*Store, error) {
	if cfg.Dir == "" {
		return nil, errors.New("googleauth: Dir is required")
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = DefaultTokenURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("googleauth: create token dir: %w", err)
	}
	// MkdirAll is a no-op on an existing directory, mode included, so an
	// upgrade from a looser mode would keep it. Tighten explicitly.
	if err := os.Chmod(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("googleauth: set token dir permissions: %w", err)
	}
	return &Store{cfg: cfg, accounts: map[string]*account{}}, nil
}

// fileName maps an account to its file name within the store directory.
//
// The account is an email address chosen by whoever configures the board, and
// it is about to become part of a path -- so it is sanitised rather than
// trusted. "../../etc/passwd" must not escape the directory, and an account
// containing a separator must not create one. The rule is an allowlist: letters,
// digits and a few safe punctuation characters survive; everything else,
// separators included, becomes '_'.
//
// Case is folded because email local parts are case-insensitive in every
// practical sense and the board may run on a case-insensitive filesystem during
// host-side development. Without folding, "Nas@Stitt.org" and "nas@stitt.org"
// are two accounts on the board and one on a Mac -- a difference that would
// only show up as a test passing locally and the board asking to reconnect.
//
// Hashing the account instead was considered and rejected: the directory is
// something a person reads over adb when a calendar stops updating, and
// "a3f9c1....json" tells them nothing about which account is broken.
func fileName(account string) (string, error) {
	a := strings.TrimSpace(strings.ToLower(account))
	if a == "" {
		return "", errors.New("googleauth: empty account")
	}
	var b strings.Builder
	for _, r := range a {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '@' || r == '.' || r == '-' || r == '_' || r == '+':
			b.WriteRune(r)
		default:
			// Covers '/', '\\', NUL, control characters, and anything
			// non-ASCII. Non-ASCII is mapped rather than preserved because
			// filesystems disagree about Unicode normalisation and the board's
			// UBIFS is not the same as a Mac's APFS.
			b.WriteByte('_')
		}
	}
	name := b.String()
	// The allowlist cannot produce a path separator, so the only remaining
	// traversal shapes are the dot-only names -- "." and ".." themselves, and
	// anything that sanitises down to them.
	if strings.Trim(name, ".") == "" {
		return "", fmt.Errorf("googleauth: account %q has no usable characters", account)
	}
	return name + ".json", nil
}

// Path returns where an account's token is stored. Exported so the portal's
// disconnect path and any operator poking around over adb are looking at the
// same name this package writes.
func (s *Store) Path(account string) (string, error) {
	name, err := fileName(account)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.cfg.Dir, name), nil
}

func (s *Store) account(key string) *account {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accounts[key]
	if a == nil {
		a = &account{}
		s.accounts[key] = a
	}
	return a
}

// Save writes a token, replacing any existing one for the account.
//
// The write is atomic (temp file plus rename) for the same reason config.Save
// is: this file is the only copy of a credential that took a phone, a browser
// and a typed code to obtain, and a power cut mid-write on NAND would otherwise
// leave a truncated file that reads as "reconnect this account".
func (s *Store) Save(tok StoredToken) error {
	path, err := s.Path(tok.Account)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		// Without a refresh token the file is a ~1h credential that can never
		// be renewed, which is indistinguishable from a broken account an hour
		// later. Refuse it at the point where the mistake is still traceable.
		return errors.New("googleauth: refusing to save a token with no refresh token")
	}
	a := s.account(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	return writeTokenFile(path, tok)
}

func writeTokenFile(path string, tok StoredToken) error {
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("googleauth: encode token: %w", err)
	}
	b = append(b, '\n')

	// Same directory as the target, so the rename stays on one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("googleauth: create temp token: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("googleauth: write temp token: %w", err)
	}
	// Rename is atomic but does not order the data against it; without the
	// sync a power loss can land the rename and not the contents.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("googleauth: sync temp token: %w", err)
	}
	// os.CreateTemp already uses 0600 and rename preserves it, but state it
	// explicitly so the guarantee does not depend on that default.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("googleauth: set token permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("googleauth: close temp token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("googleauth: install token: %w", err)
	}
	return nil
}

// Load reads an account's stored token without refreshing it. Callers wanting a
// usable access token want AccessToken instead; this exists for the portal's
// "which accounts are connected" listing and for tests.
func (s *Store) Load(account string) (StoredToken, error) {
	path, err := s.Path(account)
	if err != nil {
		return StoredToken{}, err
	}
	a := s.account(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	return readTokenFile(path)
}

func readTokenFile(path string) (StoredToken, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoredToken{}, ErrNoToken
		}
		// A read error that is not "missing" -- a permissions problem, or NAND
		// giving up on the block -- is transient as far as this package can
		// tell. It is not evidence the grant is dead, and classifying it as
		// terminal would tell the user to reconnect an account that is fine.
		return StoredToken{}, fmt.Errorf("%w: read token: %v", ErrTransient, err)
	}
	var tok StoredToken
	if err := json.Unmarshal(b, &tok); err != nil {
		// Corrupt on disk. Reported as needing re-auth rather than as transient
		// because retrying reads the same bytes forever: there is no recovery
		// that does not involve the user. The error text deliberately carries
		// no file contents -- a partially written token file contains a partial
		// credential.
		return StoredToken{}, fmt.Errorf("%w: stored token is unreadable", ErrNeedsReauth)
	}
	if tok.RefreshToken == "" {
		// Well-formed JSON with nothing usable in it. Same conclusion.
		return StoredToken{}, fmt.Errorf("%w: stored token has no refresh token", ErrNeedsReauth)
	}
	return tok, nil
}

// Delete removes an account's token. Missing is success: disconnect must be
// idempotent, because the portal can be submitted twice and the second attempt
// failing would report a problem that does not exist.
func (s *Store) Delete(account string) error {
	path, err := s.Path(account)
	if err != nil {
		return err
	}
	a := s.account(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("googleauth: delete token: %w", err)
	}
	return nil
}

// Accounts lists the accounts with a stored token, for the portal's
// connected-accounts list.
//
// The names come from inside the files rather than from their names: the
// filename is sanitised and lower-cased, so it cannot be turned back into the
// address the user typed. A file that fails to parse is skipped rather than
// failing the listing -- one corrupt token must not make the settings page
// unable to show the accounts that are fine.
func (s *Store) Accounts() ([]string, error) {
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("googleauth: list tokens: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		tok, err := readTokenFile(filepath.Join(s.cfg.Dir, e.Name()))
		if err != nil || tok.Account == "" {
			continue
		}
		out = append(out, tok.Account)
	}
	return out, nil
}

// AccessToken returns a usable bearer token for an account, refreshing first if
// the stored one is expired or within refreshSkew of expiring.
//
// Every error is wrapped with exactly one of ErrNoToken, ErrNeedsReauth or
// ErrTransient, so a caller can decide its retry policy with errors.Is and
// never has to parse a message. No returned error contains a token value: these
// are expected to reach the panel and the settings page.
func (s *Store) AccessToken(ctx context.Context, account string) (string, error) {
	tok, err := s.EnsureFresh(ctx, account)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// EnsureFresh is AccessToken with the whole record, for callers that also want
// the scope or the expiry -- the portal shows when an account was connected,
// which is what predicts the 7-day testing-status expiry.
func (s *Store) EnsureFresh(ctx context.Context, account string) (StoredToken, error) {
	path, err := s.Path(account)
	if err != nil {
		return StoredToken{}, err
	}
	a := s.account(path)

	// Held across the refresh call, deliberately. Two goroutines fetching two
	// calendars that share an account would otherwise both refresh; Google
	// honours that, but it doubles the request count for nothing and the two
	// writes race over the same file. Waiting means the second caller re-reads
	// below and finds the first caller's fresh token.
	a.mu.Lock()
	defer a.mu.Unlock()

	tok, err := readTokenFile(path)
	if err != nil {
		return StoredToken{}, err
	}
	now := s.cfg.Now()
	if tok.Valid(now) {
		return tok, nil
	}

	refreshed, err := s.refresh(ctx, tok)
	if err != nil {
		return StoredToken{}, err
	}
	if err := writeTokenFile(path, refreshed); err != nil {
		// The refresh succeeded and only the persistence failed. Return the
		// token anyway: the caller gets a working hour, and the next refresh
		// retries the write. Failing here would turn a full NAND into a dead
		// calendar.
		return refreshed, nil
	}
	return refreshed, nil
}

// tokenResponse is Google's token endpoint reply, both success and error.
//
// Decoded from one struct because the endpoint answers an error with a 400 and
// a JSON body in the same shape family, and pairing the status with the body is
// what makes the classification possible -- a 400 alone does not distinguish
// "this grant is dead" from "this request was malformed".
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`

	// RefreshToken is frequently ABSENT on a refresh response -- Google only
	// sends a new one when it rotates the grant. Overwriting the stored value
	// with this field unconditionally would blank the refresh token on almost
	// every refresh and destroy the credential silently, with the failure
	// landing an hour later when the access token expires.
	RefreshToken string `json:"refresh_token"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// refresh exchanges a refresh token for a new access token.
//
// The returned token always carries the caller's account, refresh token and
// Obtained time forward; only the access token, expiry and scope come from the
// response, plus a refresh token when one is actually present.
func (s *Store) refresh(ctx context.Context, tok StoredToken) (StoredToken, error) {
	form := url.Values{
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"refresh_token": {tok.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return StoredToken{}, fmt.Errorf("%w: build refresh request", ErrTransient)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		// A transport error is never evidence about the grant. The board is
		// routinely in this state: it boots with no network and associates
		// seconds later, and a marginal AP drops requests all day. The error
		// text is not wrapped with %v because a URL-carrying transport error
		// can include the request body in some shapes, and the request body is
		// the refresh token.
		return StoredToken{}, fmt.Errorf("%w: cannot reach google token endpoint", ErrTransient)
	}
	defer resp.Body.Close()

	// The endpoint's replies are small; cap anyway so a proxy or a captive
	// portal answering with a large HTML page cannot be read into memory on a
	// 512MB board.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return StoredToken{}, fmt.Errorf("%w: reading token response", ErrTransient)
	}

	var tr tokenResponse
	// A decode failure is not fatal on its own: what matters next is the status
	// code, and a captive portal's HTML 200 must not be read as a success.
	decodeErr := json.Unmarshal(body, &tr)

	if err := classify(resp.StatusCode, tr, decodeErr); err != nil {
		return StoredToken{}, err
	}

	out := tok
	out.AccessToken = tr.AccessToken
	// Stored as an absolute instant, computed from the local clock. If the
	// clock is wrong the expiry is wrong in the same direction, which is the
	// best available behaviour: too-early expiry costs a redundant refresh,
	// and the board sets its clock from rdate before the dashboard's first
	// successful fetch anyway.
	out.Expiry = s.cfg.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	if tr.Scope != "" {
		out.Scope = tr.Scope
	}
	// The whole point of the field comment above: keep the old refresh token
	// unless Google actually rotated it.
	if tr.RefreshToken != "" {
		out.RefreshToken = tr.RefreshToken
	}
	return out, nil
}

// classify turns an HTTP status plus a decoded body into one of the sentinels,
// or nil when the response is a usable success.
//
// Terminal means "no amount of retrying fixes this" and is signalled by exactly
// one thing: Google's invalid_grant. That covers a revoked grant, a password
// change, and the 7-day refresh-token expiry that applies while the OAuth app
// is in testing status. Everything else is transient, including the 4xx codes
// that look like programming errors -- invalid_client and invalid_request do
// mean something is wrong, but telling the user to re-authorise an account
// because the client secret was mistyped sends them to fix the wrong thing,
// and the retry cost is bounded by the calendar's own interval.
//
// 428 authorization_pending is the device flow's "user hasn't finished yet" and
// cannot occur on a refresh; it is classified transient rather than terminal so
// that a device-flow poller built on this endpoint later cannot accidentally
// inherit "give up forever" from a code path meant for revoked grants.
func classify(status int, tr tokenResponse, decodeErr error) error {
	if tr.Error == "invalid_grant" {
		// Google's error_description is a short English phrase ("Token has been
		// expired or revoked.") with no credential material in it, so it is safe
		// to surface. It is the only detail that distinguishes the several ways
		// a grant dies, and the panel has room for it.
		if tr.ErrorDescription != "" {
			return fmt.Errorf("%w: %s", ErrNeedsReauth, tr.ErrorDescription)
		}
		return ErrNeedsReauth
	}
	if status != http.StatusOK {
		if tr.Error != "" {
			return fmt.Errorf("%w: token endpoint returned %s (HTTP %d)", ErrTransient, tr.Error, status)
		}
		return fmt.Errorf("%w: token endpoint returned HTTP %d", ErrTransient, status)
	}
	if decodeErr != nil {
		// A 200 whose body is not the JSON we expect. In practice this is a
		// captive portal or a transparent proxy, both of which resolve
		// themselves, so it is transient.
		return fmt.Errorf("%w: token endpoint returned an unreadable response", ErrTransient)
	}
	if tr.AccessToken == "" {
		// A 200 with no token. Not seen in practice, but treating it as success
		// would store an empty access token and fail every subsequent request
		// with a 401 that looks like a scope problem.
		return fmt.Errorf("%w: token endpoint returned no access token", ErrTransient)
	}
	if tr.ExpiresIn <= 0 {
		// Likewise: a zero expiry stores a token that Valid rejects
		// immediately, producing a refresh on every single fetch.
		return fmt.Errorf("%w: token endpoint returned no expiry", ErrTransient)
	}
	return nil
}
