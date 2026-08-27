package portal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

type fakeStore struct {
	mu         sync.Mutex
	c          *config.Config
	configPath string
}

func (f *fakeStore) Config() *config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c
}

func (f *fakeStore) SetConfig(c *config.Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.c = c
}

// Update mirrors cmd/dashboard's Store.Update: hold the lock across
// copy-mutate-save-install so tests exercising concurrent saves (e.g.
// TestConcurrentSavesToDifferentSectionsBothSurvive) see the same
// no-lost-update guarantee production gets.
func (f *fakeStore) Update(fn func(*config.Config) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := *f.c
	if err := fn(&next); err != nil {
		return err
	}
	if f.configPath != "" {
		if err := next.Save(f.configPath); err != nil {
			return err
		}
	}
	f.c = &next
	return nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	c := &config.Config{}
	c.Location.Timezone = "America/Chicago"
	b200 := 200
	c.Display.Brightness = &b200
	c.Calendars = []config.CalendarSource{{Name: "Work", Color: "#4f9cff", URL: "https://example.com/w.ics"}}
	configPath := t.TempDir() + "/config.json"
	return &Server{
		Store:      &fakeStore{c: c, configPath: configPath},
		MAC:        "54:01:4a:4c:1b:fd",
		ConfigPath: configPath,
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

func TestSaveCalendarsPreservesColorsOnDelete(t *testing.T) {
	s := newTestServer(t)
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "A", Color: "#4f9cff", URL: "https://example.com/a.ics"},
			{Name: "B", Color: "#8b97ab", URL: "https://example.com/b.ics"},
			{Name: "C", Color: "#e8ecf3", URL: "https://example.com/c.ics"},
		},
	})

	// Delete A by clearing its URL, leaving B and C untouched.
	form := "name=&url=&name=B&url=" + "https://example.com/b.ics" +
		"&name=C&url=" + "https://example.com/c.ics"
	req := httptest.NewRequest("POST", "/save/calendars", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	got := s.Store.Config().Calendars
	if len(got) != 2 {
		t.Fatalf("Calendars = %d entries, want 2: %+v", len(got), got)
	}
	if got[0].URL != "https://example.com/b.ics" || got[0].Color != "#8b97ab" {
		t.Errorf("B = %+v, want Color #8b97ab unchanged", got[0])
	}
	if got[1].URL != "https://example.com/c.ics" || got[1].Color != "#e8ecf3" {
		t.Errorf("C = %+v, want Color #e8ecf3 unchanged", got[1])
	}
}

func TestSaveCalendarsNewFeedDoesNotDuplicateColor(t *testing.T) {
	s := newTestServer(t)
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "A", Color: "#4f9cff", URL: "https://example.com/a.ics"},
			{Name: "B", Color: "#8b97ab", URL: "https://example.com/b.ics"},
		},
	})

	// Delete A by clearing its row, and add a new feed D.
	form := "name=&url=" +
		"&name=B&url=" + "https://example.com/b.ics" +
		"&name=D&url=" + "https://example.com/d.ics"
	req := httptest.NewRequest("POST", "/save/calendars", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	got := s.Store.Config().Calendars
	if len(got) != 2 {
		t.Fatalf("Calendars = %d entries, want 2: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.Color] {
			t.Errorf("color %q used by more than one feed: %+v", c.Color, got)
		}
		seen[c.Color] = true
	}
}

func TestSavePasswordDisablesMACDefault(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/password", strings.NewReader("password=newpassword"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	// The MAC-derived default must no longer authenticate.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.SetBasicAuth("admin", "4c1bfd")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("status with MAC default after password set = %d, want 401", w2.Code)
	}

	// The new password must authenticate.
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.SetBasicAuth("admin", "newpassword")
	w3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Errorf("status with new password = %d, want 200", w3.Code)
	}
}

func TestSavePlaceRejectsInvalidTimezone(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/place",
		strings.NewReader("latitude=10&longitude=20&timezone=Not/AZone"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "4c1bfd")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if s.Store.Config().Location.Timezone != "America/Chicago" {
		t.Errorf("Timezone = %q, want unchanged America/Chicago", s.Store.Config().Location.Timezone)
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
