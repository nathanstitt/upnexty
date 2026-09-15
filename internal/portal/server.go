package portal

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/wifi"
)

// ConfigStore is the slice of cmd/dashboard's Store that the portal needs.
// Declared here rather than imported so this package does not depend on main.
type ConfigStore interface {
	Config() *config.Config
	SetConfig(*config.Config)

	// Update performs an atomic read-modify-write-save: fn receives a fresh
	// copy of the current config, and if it returns nil the copy is
	// persisted and installed as the new current config, all under one
	// lock. save (in handlers.go) uses this instead of Config()+SetConfig()
	// so two concurrent POSTs to different sections cannot each read the
	// same starting config and have the second save silently discard the
	// first's change -- see cmd/dashboard/loop.go's Store.Update for the
	// full reasoning.
	Update(fn func(*config.Config) error) error
}

// CalendarRefetcher is implemented by a store that can re-read its calendar
// feeds on demand. Optional rather than part of ConfigStore: only the real
// store runs a fetch loop, and requiring it would make every test fake
// implement a method it has nothing to do with.
type CalendarRefetcher interface {
	RefetchCalendars()
}

// Server serves the configuration UI.
type Server struct {
	Store ConfigStore
	WiFi  *wifi.Client
	MAC   string

	// EtcHostname and ProcHostname override where the hostname is written.
	// Empty means the real board paths; tests point them at a temp dir, since
	// writing /etc and /proc needs root and would rename the machine running
	// the tests. Same device as wifi.Client.ConfPath.
	EtcHostname  string
	ProcHostname string

	// Now overrides the clock for the generated sample feed. Nil means
	// time.Now; tests set it so the fixture's timestamps are predictable.
	Now func() time.Time

	// connecting single-flights the background Connect goroutine started by
	// handleSaveWiFi: association tears down the AP the request arrived on
	// (see that handler), so Connect intentionally outlives the request and
	// cannot be made synchronous -- but a user resubmitting the form (e.g.
	// after a typo, or just impatiently) before the first attempt finishes
	// would otherwise stack a second `S99wlan0 restart` on top of the first,
	// and the two can kill each other's supplicant. While one is in flight,
	// a new submit updates the saved config (handleSaveWiFi's s.save call
	// still runs) but does not start a second connect attempt; the next
	// attempt to actually associate happens at the following reboot/restart,
	// or the user can resubmit once the in-flight attempt has finished.
	connecting atomic.Bool

	// pendingSSID is the network the in-flight Connect is joining, published so
	// the panel can show the attempt while it happens. Written next to the
	// connecting swap and read by Connecting; atomic.Value rather than a plain
	// string because the render loop reads it from another goroutine.
	pendingSSID atomic.Value // string

	// wifiErrMu guards wifiErr, the most recent background Connect failure.
	// Set by handleSaveWiFi's goroutine, read by page() so the settings UI
	// can show it on the next load -- Connect's error would otherwise be
	// silently dropped, since the HTTP response has already been sent by the
	// time it comes back.
	wifiErrMu sync.Mutex
	wifiErr   string

	// Google is the device-flow client used to connect a Google account. Nil
	// disables the feature: the settings page hides the button rather than
	// offering something that cannot work, which is how a board built without
	// OAuth credentials behaves.
	Google GoogleLinker

	// pairing single-flights the device-flow goroutine, for the same reason
	// connecting does for WiFi: the flow outlives its request (the user has to
	// walk to their phone, and Google's poll interval is 5s over a window of
	// up to 30 minutes), so a second submit would otherwise start a second
	// flow and a second code, and the panel can only show one.
	pairing atomic.Bool

	// pairingCode is the {user_code, verification_url} the panel displays while
	// a flow is in flight, or the zero value when none is. Same shape and
	// rationale as pendingSSID: written beside the pairing swap, read by the
	// render goroutine.
	pairingCode atomic.Value // PairingCode

	// googleErrMu guards googleErr, the most recent device-flow outcome.
	// Same problem as wifiErr: the response is long gone by the time the flow
	// resolves, so the settings page is the only place left to report it.
	googleErrMu sync.Mutex
	googleErr   string
}

// PairingCode is what the user types at Google to authorise the board.
//
// It reaches the user via the PANEL, not the browser that submitted the form.
// That is not a stylistic choice: the code takes seconds to arrive and the
// flow then runs for as long as it takes someone to pick up their phone, so
// the page that started it has already been answered. The panel is the surface
// that is still showing something by then -- exactly the reasoning behind
// Connecting() for WiFi.
type PairingCode struct {
	// UserCode is the short string typed at VerificationURL, e.g. "VSBR-DGFB".
	UserCode string
	// VerificationURL is where to type it, e.g. "https://www.google.com/device".
	VerificationURL string
	// Account is the address being connected, when a re-auth of a known
	// account started the flow. Empty for a first connection, where nobody
	// knows yet which account the user will sign in as.
	Account string
}

// GoogleLinker runs the OAuth device flow and persists the resulting token.
//
// An interface so the portal does not import the token store: it keeps this
// package testable without a temp directory and an httptest token endpoint,
// and it is the seam that lets a board built without credentials pass nil.
type GoogleLinker interface {
	// StartDeviceFlow asks Google for a code. The returned code is shown on
	// the panel.
	StartDeviceFlow(ctx context.Context) (PairingCode, string, error)
	// AwaitToken polls until the user authorises, the code expires, or ctx
	// ends, then stores the token. It returns the account that was connected.
	AwaitToken(ctx context.Context, deviceCode string) (account string, err error)
	// Accounts lists the connected accounts, for the settings page.
	Accounts() ([]string, error)
	// Disconnect deletes an account's token.
	Disconnect(account string) error
}

// PairingCode returns the in-flight device-flow code, or the zero value when
// no flow is running. Read by the render loop each tick, like Connecting.
func (s *Server) PairingCode() PairingCode {
	if !s.pairing.Load() {
		return PairingCode{}
	}
	pc, _ := s.pairingCode.Load().(PairingCode)
	return pc
}

// setGoogleErr records the most recent device-flow outcome. nil clears it, so
// a later successful connection does not leave a stale failure on the page.
func (s *Server) setGoogleErr(err error) {
	s.googleErrMu.Lock()
	defer s.googleErrMu.Unlock()
	if err == nil {
		s.googleErr = ""
		return
	}
	s.googleErr = err.Error()
}

func (s *Server) getGoogleErr() string {
	s.googleErrMu.Lock()
	defer s.googleErrMu.Unlock()
	return s.googleErr
}

// setWiFiErr records the most recent background Connect outcome. err == nil
// clears any previous error, so a later successful connect (whether from a
// retry or the next boot) does not leave a stale failure message on screen
// forever.
func (s *Server) setWiFiErr(err error) {
	s.wifiErrMu.Lock()
	defer s.wifiErrMu.Unlock()
	if err == nil {
		s.wifiErr = ""
		return
	}
	s.wifiErr = err.Error()
}

func (s *Server) getWiFiErr() string {
	s.wifiErrMu.Lock()
	defer s.wifiErrMu.Unlock()
	return s.wifiErr
}

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// The stylesheet is deliberately unauthenticated: it is also used by the
	// 401 page, which by definition has no credentials yet.
	mux.HandleFunc("GET /portal.css", func(w http.ResponseWriter, r *http.Request) {
		b, err := assetFS.ReadFile("assets/portal.css")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(b)
	})

	// Also unauthenticated: the dashboard's own calendar fetcher requests this
	// over loopback and sends no credentials. It serves only a fixture it just
	// generated -- no configuration, no user data.
	mux.HandleFunc("GET /sample.ical", s.handleSampleICal)

	// Captive-portal probe endpoints. iOS/macOS request
	// /hotspot-detect.html, Android /generate_204 and /gen_204, Windows
	// /connecttest.txt and /ncsi.txt. Each expects a specific success response
	// (Apple: a page containing exactly "Success"; Android: a bare 204). Any
	// other answer means "this network intercepts traffic", which is what makes
	// the OS raise its sign-in sheet.
	//
	// Answering them with the settings page is therefore the entire mechanism;
	// see handleCaptiveProbe for why it is served in place rather than
	// redirected.
	for _, p := range []string{
		"/hotspot-detect.html", "/library/test/success.html",
		"/generate_204", "/gen_204",
		"/connecttest.txt", "/ncsi.txt",
		"/canonical.html", "/success.txt",
	} {
		mux.HandleFunc("GET "+p, s.handleCaptiveProbe)
	}

	mux.Handle("GET /", s.auth(http.HandlerFunc(s.handleSettings)))
	mux.Handle("POST /save/wifi", s.auth(http.HandlerFunc(s.handleSaveWiFi)))
	mux.Handle("POST /save/calendars", s.auth(http.HandlerFunc(s.handleSaveCalendars)))
	mux.Handle("POST /save/place", s.auth(http.HandlerFunc(s.handleSavePlace)))
	mux.Handle("POST /save/display", s.auth(http.HandlerFunc(s.handleSaveDisplay)))
	mux.Handle("POST /save/hostname", s.auth(http.HandlerFunc(s.handleSaveHostname)))
	mux.Handle("POST /save/password", s.auth(http.HandlerFunc(s.handleSavePassword)))
	mux.Handle("POST /unmute", s.auth(http.HandlerFunc(s.handleUnmute)))
	mux.Handle("POST /google/connect", s.auth(http.HandlerFunc(s.handleGoogleConnect)))
	mux.Handle("POST /google/disconnect", s.auth(http.HandlerFunc(s.handleGoogleDisconnect)))

	// Login and logout are outside auth by definition.
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	return mux
}

// Connecting reports the network an in-flight association attempt is joining,
// or "" when none is running. The dashboard polls this each render so the panel
// can show the attempt: the browser that submitted the form loses its
// connection when the AP comes down, which makes the panel the only surface
// still able to report what is happening.
func (s *Server) Connecting() string {
	if !s.connecting.Load() {
		return ""
	}
	ssid, _ := s.pendingSSID.Load().(string)
	return ssid
}

// PortalURL is the portal's address, printed on the panel for the case where
// the sign-in sheet does not appear on its own. No port suffix: the portal
// serves :80, which is also where a connectivity probe looks.
const PortalURL = "http://" + wifi.APAddr

// handleCaptiveProbe answers an OS connectivity check with the portal itself,
// which is what raises the "sign in to network" sheet.
//
// Serving a page rather than redirecting is deliberate. The portal is on :80,
// the same port the probe hits, so there is nowhere to redirect to -- and an
// earlier version that did redirect (to :8080) made the sheet render the
// destination's response as a bare error instead of following it usefully.
// Answering in place keeps the sheet on one origin with real HTML in it.
//
// It runs through auth like any other page, so an unauthenticated probe gets
// the login form -- which is exactly the right thing for the sheet to show.
// The DNS wildcard alone does none of this: dnsmasq points every name at the
// board, but something still has to answer the request.
func (s *Server) handleCaptiveProbe(w http.ResponseWriter, r *http.Request) {
	s.auth(http.HandlerFunc(s.handleSettings)).ServeHTTP(w, r)
}

// auth gates a handler behind a session cookie, showing the login page when
// there is not a valid one.
//
// Cookie rather than Basic auth because the portal's primary client is a
// captive-portal sheet, and a sheet does not render a WWW-Authenticate
// challenge: the 401 body is displayed instead, which reads as a blank or
// broken page with no way to enter anything. An ordinary HTML form works in the
// sheet, in a desktop browser, and in curl (--data + --cookie-jar) alike, so
// there is one mechanism rather than two.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := s.Store.Config().Portal.PasswordHash

		if c, err := r.Cookie(SessionCookie); err == nil && ValidSession(c.Value, hash, s.MAC) {
			next.ServeHTTP(w, r)
			return
		}

		// No session. A POST carrying the right password is a login combined
		// with the action: set the cookie and let the request through, so
		// submitting the settings form from a fresh sheet works in one step
		// rather than bouncing through a separate login screen.
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err == nil {
				if CheckPassword(r.PostFormValue("device_password"), hash, s.MAC) {
					s.setSession(w, hash)
					next.ServeHTTP(w, r)
					return
				}
			}
			s.loginPage(w, "That device password is not correct.", r.URL.Path)
			return
		}
		s.loginPage(w, "", r.URL.Path)
	})
}

// setSession installs the session cookie.
func (s *Server) setSession(w http.ResponseWriter, hash string) {
	http.SetCookie(w, &http.Cookie{
		Name:  SessionCookie,
		Value: NewSessionToken(hash, s.MAC),
		Path:  "/",
		// No Secure: the portal is plain HTTP (it has no certificate and, in AP
		// mode, no resolvable name to get one for). Setting Secure would make
		// the cookie be dropped entirely.
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
}

// clearSession expires the session cookie.
func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// handleLogin authenticates and returns to the page the user was aiming at.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	hash := s.Store.Config().Portal.PasswordHash
	if err := r.ParseForm(); err != nil {
		s.loginPage(w, "That form could not be read. Try again.", "/")
		return
	}
	next := r.PostFormValue("next")
	if !strings.HasPrefix(next, "/") {
		next = "/" // never redirect off-site on a value from the request
	}
	if !CheckPassword(r.PostFormValue("device_password"), hash, s.MAC) {
		s.loginPage(w, "That device password is not correct.", next)
		return
	}
	s.setSession(w, hash)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// handleLogout drops the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
