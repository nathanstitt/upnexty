package portal

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
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

// Server serves the configuration UI.
type Server struct {
	Store      ConfigStore
	WiFi       *wifi.Client
	MAC        string
	ConfigPath string

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

	// wifiErrMu guards wifiErr, the most recent background Connect failure.
	// Set by handleSaveWiFi's goroutine, read by page() so the settings UI
	// can show it on the next load -- Connect's error would otherwise be
	// silently dropped, since the HTTP response has already been sent by the
	// time it comes back.
	wifiErrMu sync.Mutex
	wifiErr   string
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

	mux.Handle("GET /", s.auth(http.HandlerFunc(s.handleSettings)))
	mux.Handle("POST /save/wifi", s.auth(http.HandlerFunc(s.handleSaveWiFi)))
	mux.Handle("POST /save/calendars", s.auth(http.HandlerFunc(s.handleSaveCalendars)))
	mux.Handle("POST /save/place", s.auth(http.HandlerFunc(s.handleSavePlace)))
	mux.Handle("POST /save/display", s.auth(http.HandlerFunc(s.handleSaveDisplay)))
	mux.Handle("POST /save/password", s.auth(http.HandlerFunc(s.handleSavePassword)))
	return mux
}

// auth gates a handler behind the admin password.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, ok := r.BasicAuth()
		if !ok || !CheckPassword(pw, s.Store.Config().Portal.PasswordHash, s.MAC) {
			w.Header().Set("WWW-Authenticate", `Basic realm="UpNext setup"`)
			http.Error(w, "Enter the device password to continue.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
