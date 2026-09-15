package googleauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The OAuth 2.0 device authorization grant (RFC 8628).
//
// This board has no browser and no keyboard: the panel is its only output and
// the settings page is reached from a phone. Device flow is the grant designed
// for exactly that -- the board asks Google for a short code, shows it on the
// panel, and polls while the user types it on another device.
//
// Verified against Google on 2026-09-14 with a "TV and Limited Input device"
// client: the scope calendar.readonly is accepted, and the responses below are
// the real shapes rather than transcriptions from the RFC.

// DefaultDeviceCodeURL is Google's device authorization endpoint. Injectable
// for the same reason as DefaultTokenURL: the pending and expiry paths cannot
// be exercised against the real endpoint without waiting out a 30-minute
// window by hand.
const DefaultDeviceCodeURL = "https://oauth2.googleapis.com/device/code"

// CalendarReadonlyScope is all this application ever asks for. Read-only is not
// caution for its own sake: a panel that displays a calendar has no reason to
// hold a credential that can alter one, and the consent screen says so.
const CalendarReadonlyScope = "https://www.googleapis.com/auth/calendar.readonly"

// ErrAuthorizationExpired reports a device code that timed out before the user
// finished. Distinct from ErrNeedsReauth: nothing is broken and no credential
// was revoked -- the user simply has to press the button again.
var ErrAuthorizationExpired = errors.New("the sign-in code expired before it was used")

// deviceCodeResponse is the device authorization endpoint's reply. Real shape:
//
//	{"device_code":"AH-1Ng3...","user_code":"VSBR-DGFB","expires_in":1800,
//	 "interval":5,"verification_url":"https://www.google.com/device"}
//
// Note verification_url, not the RFC's verification_uri: Google sends the
// former. Both are decoded so a future change to the documented spelling does
// not silently produce an empty URL on the panel.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	VerificationURL string `json:"verification_url"`
	VerificationURI string `json:"verification_uri"`
}

func (d deviceCodeResponse) url() string {
	if d.VerificationURL != "" {
		return d.VerificationURL
	}
	return d.VerificationURI
}

// DeviceAuth is a started device flow, waiting for the user.
type DeviceAuth struct {
	// UserCode is what the user types, e.g. "VSBR-DGFB".
	UserCode string
	// VerificationURL is where they type it.
	VerificationURL string
	// DeviceCode is the board's own handle on the flow. Not shown to anyone:
	// it is the bearer of the eventual token and must be treated as a secret.
	DeviceCode string
	// Interval is Google's requested poll period.
	Interval time.Duration
	// Expiry is when the code stops working.
	Expiry time.Time
}

// StartDeviceFlow asks Google for a code to show on the panel.
func (s *Store) StartDeviceFlow(ctx context.Context) (DeviceAuth, error) {
	form := url.Values{
		"client_id": {s.cfg.ClientID},
		"scope":     {CalendarReadonlyScope},
	}
	endpoint := s.cfg.DeviceCodeURL
	if endpoint == "" {
		endpoint = DefaultDeviceCodeURL
	}

	body, status, err := s.postForm(ctx, endpoint, form)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("%w: requesting a sign-in code: %v", ErrTransient, err)
	}
	if status != http.StatusOK {
		// A failure here is nearly always the app's own configuration -- a
		// client id of the wrong type, or the scope not enabled on the project
		// -- so it is reported as transient rather than as "reconnect your
		// account": there is no account yet, and no amount of user action at
		// google.com/device would help.
		return DeviceAuth{}, fmt.Errorf("%w: sign-in code refused: %s", ErrTransient, oauthErrorMessage(body, status))
	}

	var dc deviceCodeResponse
	if err := json.Unmarshal(body, &dc); err != nil {
		return DeviceAuth{}, fmt.Errorf("%w: decoding the sign-in code: %v", ErrTransient, err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return DeviceAuth{}, fmt.Errorf("%w: sign-in response carried no code", ErrTransient)
	}

	// Google's own interval, with a floor. A server that omits it or sends 0
	// would otherwise turn the poll below into a hot loop against an endpoint
	// that rate-limits.
	interval := time.Duration(dc.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	expires := time.Duration(dc.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 30 * time.Minute
	}

	return DeviceAuth{
		UserCode:        dc.UserCode,
		VerificationURL: dc.url(),
		DeviceCode:      dc.DeviceCode,
		Interval:        interval,
		Expiry:          s.cfg.Now().Add(expires),
	}, nil
}

// PollDeviceFlow waits for the user to authorise, then stores the token and
// returns the account it belongs to.
//
// Blocks for as long as the code is valid -- up to 30 minutes -- so callers run
// it in a goroutine and bound it with ctx.
func (s *Store) PollDeviceFlow(ctx context.Context, auth DeviceAuth) (StoredToken, error) {
	interval := auth.Interval
	if interval < time.Second {
		interval = 5 * time.Second
	}

	form := url.Values{
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"device_code":   {auth.DeviceCode},
		"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	endpoint := s.cfg.TokenURL
	if endpoint == "" {
		endpoint = DefaultTokenURL
	}

	for {
		body, status, err := s.postForm(ctx, endpoint, form)
		if err != nil {
			// The board may be mid-reassociation, or the user may have just
			// switched it onto the network they are authorising from. Keep
			// polling: the code's own expiry is the deadline, not the first
			// failed request.
			if ctx.Err() != nil {
				return StoredToken{}, fmt.Errorf("%w: sign-in cancelled", ErrTransient)
			}
		} else {
			switch code := oauthErrorCode(body, status); code {
			case "":
				// Success.
				tok, err := s.storeTokenResponse(body)
				if err != nil {
					return StoredToken{}, err
				}
				return tok, nil

			case "authorization_pending":
				// The expected answer until the user finishes. Not an error.

			case "slow_down":
				// Google asks for a longer gap; RFC 8628 says add 5 seconds.
				interval += 5 * time.Second

			case "expired_token":
				return StoredToken{}, ErrAuthorizationExpired

			case "access_denied":
				// The user pressed "cancel" at the consent screen. A specific
				// answer beats a timeout: nothing is broken and they chose it.
				return StoredToken{}, fmt.Errorf("%w: sign-in was declined", ErrNeedsReauth)

			default:
				return StoredToken{}, fmt.Errorf("%w: sign-in failed: %s",
					ErrTransient, oauthErrorMessage(body, status))
			}
		}

		select {
		case <-ctx.Done():
			return StoredToken{}, fmt.Errorf("%w: sign-in cancelled", ErrTransient)
		case <-s.after(interval):
		}

		if !s.cfg.Now().Before(auth.Expiry) {
			// Belt and braces: normally Google answers expired_token first,
			// but a board whose clock jumped (rdate lands after boot) or a
			// server that never says so must not leave this polling forever.
			return StoredToken{}, ErrAuthorizationExpired
		}
	}
}

// oauthErrorCode returns the OAuth `error` field, or "" when the response is a
// success. A 200 carrying no error field is the success case.
func oauthErrorCode(body []byte, status int) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error != "" {
		return e.Error
	}
	if status == http.StatusOK {
		return ""
	}
	return "unexpected_status"
}

// oauthErrorMessage renders a failure for a human, preferring Google's
// description. Never includes the raw body: it can echo request parameters.
func oauthErrorMessage(body []byte, status int) string {
	var e struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &e)
	switch {
	case e.Desc != "" && e.Error != "":
		return e.Error + " (" + e.Desc + ")"
	case e.Error != "":
		return e.Error
	default:
		return http.StatusText(status)
	}
}

// postForm sends a form and returns the body and status. The body is capped:
// these endpoints answer in hundreds of bytes, and an unbounded read here
// would be a way to exhaust a 512MB board from the network.
func (s *Store) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// httpClient is the configured client. New always sets one, so this is a
// convenience rather than a nil guard -- but a Store built by a test that
// bypassed New would otherwise panic here rather than at construction.
func (s *Store) httpClient() *http.Client {
	if s.cfg.HTTPClient != nil {
		return s.cfg.HTTPClient
	}
	return http.DefaultClient
}

// after is time.After, overridable so a poll test does not spend real seconds
// waiting out Google's 5s interval.
func (s *Store) after(d time.Duration) <-chan time.Time {
	if s.cfg.After != nil {
		return s.cfg.After(d)
	}
	return time.After(d)
}

// storeTokenResponse turns a successful device-flow token response into a
// stored token, naming the account it belongs to.
//
// The account is looked up from Google rather than parsed out of the id_token:
// this flow does not request the openid scope, so there is no id_token to
// parse, and adding the scope purely to learn an email address would widen the
// consent screen for no benefit. userinfo is one request, made once per
// authorisation.
func (s *Store) storeTokenResponse(body []byte) (StoredToken, error) {
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return StoredToken{}, fmt.Errorf("%w: decoding the sign-in token: %v", ErrTransient, err)
	}
	if tr.AccessToken == "" || tr.RefreshToken == "" {
		// A device-flow grant without a refresh token is unusable on a panel:
		// the access token dies in an hour and there would be no way back
		// without the user standing at the board again.
		return StoredToken{}, fmt.Errorf("%w: sign-in returned no refresh token", ErrTransient)
	}

	now := s.cfg.Now()
	tok := StoredToken{
		RefreshToken: tr.RefreshToken,
		AccessToken:  tr.AccessToken,
		Scope:        tr.Scope,
		Expiry:       now.Add(time.Duration(tr.ExpiresIn) * time.Second).UTC(),
		Obtained:     now.UTC(),
	}

	account, err := s.lookupAccount(context.Background(), tr.AccessToken)
	if err != nil {
		return StoredToken{}, err
	}
	tok.Account = account

	if err := s.Save(tok); err != nil {
		return StoredToken{}, err
	}
	return tok, nil
}

// DefaultUserInfoURL identifies the account a token belongs to. calendarList
// would also work and needs no extra scope either, but userinfo answers in a
// few hundred bytes and says the address directly.
const DefaultUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

// lookupAccount asks Google which account an access token belongs to.
//
// Needed because the board stores tokens per account and the user never types
// their address: they pick it at Google's consent screen, so the board only
// learns it by asking.
func (s *Store) lookupAccount(ctx context.Context, accessToken string) (string, error) {
	endpoint := s.cfg.UserInfoURL
	if endpoint == "" {
		endpoint = DefaultUserInfoURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTransient, err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: identifying the account: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: identifying the account: %v", ErrTransient, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: identifying the account: %s",
			ErrTransient, oauthErrorMessage(body, resp.StatusCode))
	}
	var ui struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &ui); err != nil || ui.Email == "" {
		return "", fmt.Errorf("%w: Google did not say which account was connected", ErrTransient)
	}
	return ui.Email, nil
}
