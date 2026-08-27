package portal

import (
	"net/http"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
)

// ConfigStore is the slice of cmd/dashboard's Store that the portal needs.
// Declared here rather than imported so this package does not depend on main.
type ConfigStore interface {
	Config() *config.Config
	SetConfig(*config.Config)
}

// Server serves the configuration UI.
type Server struct {
	Store      ConfigStore
	WiFi       *wifi.Client
	MAC        string
	ConfigPath string
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
