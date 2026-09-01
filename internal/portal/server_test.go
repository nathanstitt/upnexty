package portal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
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
		Store: &fakeStore{c: c, configPath: configPath},
		MAC:   "54:01:4a:4c:1b:fd",
	}
}

// An unauthenticated request gets the login form, not a 401 challenge. 200 is
// deliberate: a captive-portal sheet renders a 401 body as an error page rather
// than a document, which is what made Basic auth show up blank there.
func TestUnauthenticatedGetsLoginPage(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with the login page", w.Code)
	}
	if h := w.Header().Get("WWW-Authenticate"); h != "" {
		t.Errorf("WWW-Authenticate = %q, want none -- Basic auth is gone", h)
	}
	body := w.Body.String()
	if !strings.Contains(body, `action="/login"`) {
		t.Error("no login form rendered")
	}
	if !strings.Contains(body, "device_password") {
		t.Error("login form has no password field")
	}
	// The settings must not leak to an unauthenticated caller.
	if strings.Contains(body, "save/calendars") {
		t.Error("settings form served without a session")
	}
}

func TestMACPasswordAuthenticates(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd")) // last 6 hex of the MAC
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// A forged or stale cookie is not a session.
func TestBogusSessionCookieRejected(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "bogus"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `action="/login"`) {
		t.Error("a bogus cookie was not sent to the login page")
	}
	if strings.Contains(w.Body.String(), "save/calendars") {
		t.Error("settings served to a forged cookie")
	}
}

func TestSettingsPageShowsCurrentValues(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
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
	saved, err := config.Load(s.Store.(*fakeStore).configPath)
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	// The MAC-derived default must no longer authenticate.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	// The old session is bound to the previous (empty) password hash, so
	// changing the password invalidates it -- this is what NewSessionToken's
	// HMAC-over-the-hash construction buys.
	if strings.Contains(w2.Body.String(), "save/calendars") {
		t.Error("a session minted under the old password still reaches settings")
	}
	if !strings.Contains(w2.Body.String(), `action="/login"`) {
		t.Error("stale session was not sent to the login page")
	}

	// The new password must authenticate.
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.AddCookie(testSession(t, HashPassword("newpassword"), "54:01:4a:4c:1b:fd"))
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
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if s.Store.Config().Location.Timezone != "America/Chicago" {
		t.Errorf("Timezone = %q, want unchanged America/Chicago", s.Store.Config().Location.Timezone)
	}
}

// TestConcurrentSavesToDifferentSectionsBothSurvive is the handler-level
// counterpart of cmd/dashboard's Store-level regression test: it drives the
// bug through the actual HTTP path (concurrent POSTs to /save/wifi and
// /save/display, as in the reported reproduction) rather than calling
// Store.Update directly, proving the fix holds at the layer users actually
// hit. Looped the same 40 times as the original reproduction, which failed
// every run under the old Config()+SetConfig() save path.
func TestConcurrentSavesToDifferentSectionsBothSurvive(t *testing.T) {
	const iterations = 40

	for i := 0; i < iterations; i++ {
		s := newTestServer(t)

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/save/wifi", strings.NewReader("ssid=NewNetwork&password=hunter2"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
			s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		}()
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/save/display", strings.NewReader("brightness=42"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
			s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		}()

		close(start)
		wg.Wait()

		got := s.Store.Config()
		if got.WiFi.SSID != "NewNetwork" {
			t.Fatalf("iteration %d: WiFi.SSID = %q, want %q (lost update)", i, got.WiFi.SSID, "NewNetwork")
		}
		if got.BrightnessValue() != 42 {
			t.Fatalf("iteration %d: BrightnessValue() = %d, want 42 (lost update)", i, got.BrightnessValue())
		}

		// Must also have reached disk, not just memory -- a reboot would lose
		// whichever save lost the race otherwise.
		saved, err := config.Load(s.Store.(*fakeStore).configPath)
		if err != nil {
			t.Fatalf("iteration %d: config was not written: %v", i, err)
		}
		if saved.WiFi.SSID != "NewNetwork" || saved.BrightnessValue() != 42 {
			t.Fatalf("iteration %d: saved config = %+v, want both changes present", i, saved)
		}
	}
}

// failRunner makes wifi.Client.Connect fail deterministically at the restart
// step, without touching hardware or exec'ing a real command -- Connect gets
// as far as writing the supplicant config successfully and then fails on the
// `S99wlan0 restart` call, which is the failure mode the finding describes
// (association/restart failing after the config write succeeded).
type failRunner struct{ calls chan string }

func (f *failRunner) Run(name string, args ...string) ([]byte, error) {
	if f.calls != nil {
		f.calls <- name
	}
	if name == "/etc/init.d/S99wlan0" {
		return nil, errors.New("simulated restart failure")
	}
	return nil, nil
}

func TestSaveWiFiLogsAndSurfacesConnectFailure(t *testing.T) {
	s := newTestServer(t)
	calls := make(chan string, 4)
	s.WiFi = &wifi.Client{
		R:        &failRunner{calls: calls},
		ConfPath: t.TempDir() + "/wpa_supplicant.conf",
	}

	req := httptest.NewRequest("POST", "/save/wifi", strings.NewReader("ssid=BadNetwork&password=wrongpass"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	// 200 with the pending page (was a 303 to "/"): the response must still
	// come back before Connect finishes, which is what this asserts -- only the
	// page it returns changed.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- Connect must not block the response", w.Code)
	}

	// Wait for the background goroutine's restart call rather than sleeping:
	// the channel receive blocks exactly until Connect reaches that step.
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Connect's background goroutine never ran")
	}

	// The error surfaces asynchronously; poll getWiFiErr without a fixed
	// sleep so the test isn't a timing gamble under load.
	deadline := time.After(2 * time.Second)
	for {
		if s.getWiFiErr() != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("wifi connect failure was never recorded")
		case <-time.After(time.Millisecond):
		}
	}

	got := s.getWiFiErr()
	if !strings.Contains(got, "BadNetwork") {
		t.Errorf("recorded error = %q, want it to name the SSID", got)
	}

	// It must also reach the settings page so a human looking at the portal
	// (not just logs) can see the connect failed.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if !strings.Contains(w2.Body.String(), "BadNetwork") {
		t.Error("settings page does not show the connect failure")
	}
}

// TestSaveWiFiSingleFlightsConcurrentConnects covers the "repeated submits
// stack restarts" issue: two /save/wifi submits close together must not both
// get a background Connect goroutine running at once, since concurrent
// `S99wlan0 restart` invocations can kill each other's supplicant. The second
// submit's config save must still succeed either way -- only the redundant
// connect attempt is the thing being dropped.
func TestSaveWiFiSingleFlightsConcurrentConnects(t *testing.T) {
	s := newTestServer(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	blocking := &blockingRunner{release: release, started: started}
	s.WiFi = &wifi.Client{R: blocking, ConfPath: t.TempDir() + "/wpa_supplicant.conf"}

	post := func(ssid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/save/wifi", strings.NewReader("ssid="+ssid+"&password=pw"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}

	w1 := post("First")
	if w1.Code != http.StatusOK {
		t.Fatalf("first save status = %d, want 200 (the pending page)", w1.Code)
	}
	// Wait for the first Connect to actually be the in-flight one before
	// firing the second, so this deterministically exercises the guard
	// instead of racing to see which goroutine's CompareAndSwap wins.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first connect never started")
	}

	w2 := post("Second")
	if w2.Code != http.StatusOK {
		t.Fatalf("second save status = %d, want 200 (config save must still succeed)", w2.Code)
	}

	// The second submit's config save must have gone through even though its
	// connect attempt was skipped.
	if got := s.Store.Config().WiFi.SSID; got != "Second" {
		t.Errorf("WiFi.SSID = %q, want %q -- second save must not be blocked by the single-flight guard", got, "Second")
	}

	close(release) // let the first (only) Connect finish

	select {
	case <-started:
		t.Error("a second Connect ran concurrently with the first; single-flight guard did not hold")
	case <-time.After(100 * time.Millisecond):
		// Expected: no second signal on `started`.
	}
}

// blockingRunner lets a test hold one Connect call open (via release) while
// observing exactly when it starts (via started), so concurrency can be
// driven deterministically instead of guessed at with sleeps.
type blockingRunner struct {
	release chan struct{}
	started chan struct{}
}

func (b *blockingRunner) Run(name string, args ...string) ([]byte, error) {
	if name == "/etc/init.d/S99wlan0" {
		b.started <- struct{}{}
		<-b.release
	}
	return nil, nil
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

// testSession mints a cookie for a server with the given password hash, so
// tests exercise the same session path a logged-in browser uses.
func testSession(t *testing.T, hash, mac string) *http.Cookie {
	t.Helper()
	return &http.Cookie{Name: SessionCookie, Value: NewSessionToken(hash, mac)}
}

// refetchStore is a fakeStore that also records a refetch request, so the save
// handler's call can be observed.
type refetchStore struct {
	fakeStore
	refetched int
}

func (r *refetchStore) RefetchCalendars() { r.refetched++ }

// Saving feeds asks the store to re-read them.
//
// Without this the panel keeps showing events from the URL that was just
// replaced until the next fetch interval -- up to ten minutes by default --
// which is indistinguishable from the save having failed.
func TestSaveCalendarsTriggersARefetch(t *testing.T) {
	c := &config.Config{}
	c.Location.Timezone = "America/Chicago"
	c.Calendars = []config.CalendarSource{{Name: "Work", Color: "#4f9cff", URL: "https://example.com/old.ics"}}
	store := &refetchStore{fakeStore: fakeStore{c: c, configPath: t.TempDir() + "/config.json"}}
	s := &Server{Store: store, MAC: "54:01:4a:4c:1b:fd"}

	req := httptest.NewRequest("POST", "/save/calendars",
		strings.NewReader("name=Work&url=https%3A%2F%2Fexample.com%2Fnew.ics"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if got := s.Store.Config().Calendars; len(got) != 1 || got[0].URL != "https://example.com/new.ics" {
		t.Fatalf("saved calendars = %+v, want the new URL", got)
	}
	if store.refetched != 1 {
		t.Errorf("RefetchCalendars called %d times, want 1: the panel would keep "+
			"showing the old feed until the next interval", store.refetched)
	}
}

// A store that cannot refetch still saves. The interface is optional so test
// fakes and any future store need not implement it.
func TestSaveCalendarsWithoutARefetcher(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/save/calendars",
		strings.NewReader("name=Work&url=https%3A%2F%2Fexample.com%2Fnew.ics"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}
