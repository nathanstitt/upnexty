package portal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

type fakeStore struct{ c *config.Config }

func (f *fakeStore) Config() *config.Config     { return f.c }
func (f *fakeStore) SetConfig(c *config.Config) { f.c = c }

func newTestServer(t *testing.T) *Server {
	t.Helper()
	c := &config.Config{}
	c.Location.Timezone = "America/Chicago"
	b200 := 200
	c.Display.Brightness = &b200
	c.Calendars = []config.CalendarSource{{Name: "Work", Color: "#4f9cff", URL: "https://example.com/w.ics"}}
	return &Server{
		Store:      &fakeStore{c: c},
		MAC:        "54:01:4a:4c:1b:fd",
		ConfigPath: t.TempDir() + "/config.json",
	}
}

func TestUnauthenticatedIsChallenged(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge: %q", w.Header().Get("WWW-Authenticate"))
	}
}

func TestMACPasswordAuthenticates(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "4c1bfd") // last 6 hex of the MAC
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestWrongPasswordRejected(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "nope")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestSettingsPageShowsCurrentValues(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{"America/Chicago", "Work", "https://example.com/w.ics"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
}

func TestSettingsPageEscapesCalendarNames(t *testing.T) {
	s := newTestServer(t)
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{{Name: `<script>alert(1)</script>`}},
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "<script>alert(1)</script>") {
		t.Error("calendar name was not escaped")
	}
}

func TestSaveDisplayUpdatesConfigAndPersists(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/display",
		strings.NewReader("brightness=90&clock_24h=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 (post-redirect-get)", w.Code)
	}
	got := s.Store.Config()
	if got.BrightnessValue() != 90 {
		t.Errorf("Brightness = %d, want 90", got.BrightnessValue())
	}
	if !got.Units.Clock24h {
		t.Error("Clock24h = false, want true")
	}
	// It must reach disk, not just memory -- a reboot would lose it otherwise.
	saved, err := config.Load(s.ConfigPath)
	if err != nil {
		t.Fatalf("config was not written: %v", err)
	}
	if saved.BrightnessValue() != 90 {
		t.Errorf("saved Brightness = %d, want 90", saved.BrightnessValue())
	}
}

func TestSaveRejectsInvalidBrightness(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/display",
		strings.NewReader("brightness=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if s.Store.Config().BrightnessValue() != 200 {
		t.Error("invalid input must not change the stored config")
	}
}

func TestCSSIsServed(t *testing.T) {
	s := newTestServer(t)
	// The stylesheet must not require auth, or an unauthenticated 401 page
	// renders unstyled.
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/portal.css", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "#0b0d12") {
		t.Error("stylesheet does not contain the ground colour")
	}
}
