package portal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
)

// fixedNow is an arbitrary but stable instant; the fixture is relative to it.
var fixedNow = time.Date(2026, 8, 28, 10, 30, 0, 0, time.UTC)

// buildSample runs the generated feed through the real pipeline: parse, then
// model.Build. Asserting on the resulting ViewModel is what proves the fixture
// actually reaches the panel, rather than merely parsing.
func buildSample(t *testing.T) model.ViewModel {
	t.Helper()
	events, err := calendar.Parse([]byte(sampleICal(fixedNow)), "Sample", "#4f9cff",
		fixedNow, 7, "", time.UTC)
	if err != nil {
		t.Fatalf("calendar.Parse: %v", err)
	}
	if len(events) != len(sampleEvents) {
		t.Fatalf("parsed %d events, want %d", len(events), len(sampleEvents))
	}
	return model.Build(fixedNow, &config.Config{}, events, nil, nil, nil)
}

func TestSampleICalServedUnauthenticated(t *testing.T) {
	t.Parallel()
	s := &Server{Now: func() time.Time { return fixedNow }}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sample.ical", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the fetcher sends no credentials)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("Content-Type = %q, want text/calendar", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — the feed is clock-relative", cc)
	}
}

// The whole point of generating the feed is that it lands in the render window.
// Both public horaro test feeds parse fine and render an empty timeline because
// their dates are frozen in the past; this asserts we do not ship that trap.
func TestSampleICalFillsTheTimeline(t *testing.T) {
	t.Parallel()
	vm := buildSample(t)

	timed := len(sampleEvents) - 1 // all but the all-day entry
	// Count events, not cards: Sprint Planning and Vendor Call overlap by
	// design (see sampleEvents), so they share one stacked slot. Every timed
	// event must still reach the row, which is what this asserts.
	var events int
	for _, c := range vm.Agenda.Cards {
		switch {
		case c.IsStack():
			events += len(c.Stacked)
		case !c.IsGap() && !c.IsDaySep():
			events++
		}
	}
	if events != timed {
		t.Errorf("agenda holds %d events, want %d", events, timed)
	}
	if len(vm.AllDay) == 0 {
		t.Error("AllDay is empty; the all-day pill row would not render")
	}
	if vm.NextEvent == nil {
		t.Error("NextEvent is nil; the NEXT panel would be blank")
	}
}

// Every block must have positive width and sit within the track, or the fixture
// is not exercising the layout it exists to exercise.
func TestSampleICalBlocksAreOnScreen(t *testing.T) {
	t.Parallel()
	vm := buildSample(t)

	for _, c := range vm.Agenda.Cards {
		if c.WidthPx <= 0 {
			t.Errorf("card %q has width %v, want > 0", c.Event.Title, c.WidthPx)
		}
	}
}

// The fixture deliberately leaves now in a gap: nothing in progress, something
// just elapsed, and the next event imminent. This is the state the panel spends
// most of its day in, and it exercises the free-time chip and the countdown
// headline, neither of which an in-progress fixture reaches.
func TestSampleICalLeavesNowInAGap(t *testing.T) {
	t.Parallel()
	events := parseSample(t)

	var past, upcoming int
	for _, e := range events {
		if e.AllDay {
			continue
		}
		if !e.Start.After(fixedNow) && e.End.After(fixedNow) {
			t.Errorf("event %q is in progress; the fixture must leave now free", e.Title)
		}
		if !e.End.After(fixedNow) {
			past++
		}
		if e.Start.After(fixedNow) {
			upcoming++
		}
	}

	if past < 1 {
		t.Errorf("past events = %d, want at least 1 for past context", past)
	}
	// One imminent event plus "a few" after it.
	if upcoming < 4 {
		t.Errorf("upcoming events = %d, want at least 4", upcoming)
	}
}

// The next event must sit just outside the imminent threshold, which is what
// makes the left panel lead with FREE FOR rather than a countdown.
func TestSampleICalNextEventLeavesFreeTime(t *testing.T) {
	t.Parallel()
	events := parseSample(t)

	next := time.Duration(-1)
	for _, e := range events {
		if e.AllDay || !e.Start.After(fixedNow) {
			continue
		}
		if d := e.Start.Sub(fixedNow); next < 0 || d < next {
			next = d
		}
	}

	if next < 0 {
		t.Fatal("no upcoming event")
	}
	if next < model.ImminentMinutes*time.Minute {
		t.Errorf("next event in %v, want >= %d min so the panel reads FREE FOR",
			next, model.ImminentMinutes)
	}
}

// The end-to-end assertion: the fixture must actually drive the FREE FOR
// headline. The threshold and the offset are set independently, and a change to
// either could silently put the panel back into a countdown.
func TestSampleICalRendersFreeForHeadline(t *testing.T) {
	t.Parallel()
	vm := buildSample(t)

	if vm.NowBlock.Mode != model.ModeFree {
		t.Errorf("NowBlock.Mode = %v, want ModeFree", vm.NowBlock.Mode)
	}
	if vm.NowBlock.Lead != "FREE FOR" {
		t.Errorf("Lead = %q, want %q", vm.NowBlock.Lead, "FREE FOR")
	}
	if !vm.NowBlock.BigIsDuration {
		t.Error("BigIsDuration = false, want true so the headline gets the green free treatment")
	}
	if vm.NowBlock.NextTitle != "Design Review" {
		t.Errorf("NextTitle = %q, want %q", vm.NowBlock.NextTitle, "Design Review")
	}
}

// The window does not grow for a late event, so anything ending past +220 min is
// silently dropped -- the failure mode this fixture exists to avoid.
func TestSampleICalFitsInsideTheRenderWindow(t *testing.T) {
	t.Parallel()
	limit := fixedNow.Add(220 * time.Minute)

	for _, e := range parseSample(t) {
		if e.AllDay {
			continue
		}
		if e.End.After(limit) {
			t.Errorf("event %q ends at %v, past the +220m window edge", e.Title, e.End)
		}
	}
}

func parseSample(t *testing.T) []calendar.Event {
	t.Helper()
	events, err := calendar.Parse([]byte(sampleICal(fixedNow)), "Sample", "#4f9cff",
		fixedNow, 7, "", time.UTC)
	if err != nil {
		t.Fatalf("calendar.Parse: %v", err)
	}
	return events
}

// A phone's connectivity probe must be answered with the settings page itself.
// Answering IS what raises the "sign in to network" sheet -- the DNS wildcard in
// S99wlan0 points the name at the board but cannot answer a request.
//
// It must be the page, not a redirect and not a 401: the sheet renders both as
// what looks like an empty page (verified on an iPhone against a redirect to
// :8080, which showed only the destination's error text).
func TestCaptiveProbesServeThePortal(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}
	h := s.Handler()

	for _, p := range []string{
		"/hotspot-detect.html", "/generate_204", "/gen_204",
		"/connecttest.txt", "/ncsi.txt",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		// The probe must get the settings page itself: 200 with real HTML.
		// A redirect makes the iOS sheet render the destination as an error,
		// and a 401 renders as bare text -- both look like an empty page.
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 with the page inline", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q, want text/html", p, ct)
		}
		// With a session it is the settings page; without, the login form.
		// Either way it must be a real document with something to submit --
		// a bare error body is what made the sheet look blank.
		if b := rec.Body.String(); !strings.Contains(b, "<form") {
			t.Errorf("%s: body has no <form>; the sheet would show a blank/plain page", p)
		}
	}
}

// The probe endpoints must not sit behind the admin password: a phone checking
// connectivity has no credentials, and a 401 is not a redirect, so the sheet
// would never appear.
func TestCaptiveProbesAreUnauthenticated(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hotspot-detect.html", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("probe endpoint requires auth; the sign-in sheet will never appear")
	}
}

// The URL printed on the panel must not carry a port suffix: the portal serves
// :80, and a printed ":8080" would send a user somewhere nothing listens.
func TestPortalURLHasNoPortSuffix(t *testing.T) {
	t.Parallel()
	if strings.Contains(strings.TrimPrefix(PortalURL, "http://"), ":") {
		t.Errorf("PortalURL = %q, want no port suffix now the portal is on :80", PortalURL)
	}
}

// Saving WiFi must answer with the pending page, not a redirect back to the
// form. Association tears down the AP the browser is on, so this is the last
// thing that browser will successfully load -- a redirect to "/" reads as "the
// submit did nothing" and gives the user no reason to look at the panel.
func TestSaveWiFiShowsPendingPage(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	form := strings.NewReader("ssid=HomeNet&password=hunter2")
	req := httptest.NewRequest(http.MethodPost, "/save/wifi", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the pending page", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Update pending") {
		t.Error("body is missing the 'Update pending' heading")
	}
	if !strings.Contains(body, "HomeNet") {
		t.Error("pending page does not name the network being joined")
	}
	// The warning that the page will stop loading is the whole point: without
	// it a dropped connection looks like a failure.
	if !strings.Contains(body, "stop responding") {
		t.Error("pending page does not warn that the connection will drop")
	}
}

// The panel needs the in-flight SSID while the attempt runs, since the browser
// that submitted the form cannot be reached once the AP comes down.
func TestConnectingReportsInFlightSSID(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	if got := s.Connecting(); got != "" {
		t.Errorf("Connecting = %q on a fresh server, want empty", got)
	}
	s.connecting.Store(true)
	s.pendingSSID.Store("HomeNet")
	if got := s.Connecting(); got != "HomeNet" {
		t.Errorf("Connecting = %q, want %q", got, "HomeNet")
	}
	s.connecting.Store(false)
	if got := s.Connecting(); got != "" {
		t.Errorf("Connecting = %q once the attempt finished, want empty", got)
	}
}

// The flow a phone actually performs: open the portal through the captive
// sheet (no credentials), then submit the form. This is the case that was
// broken -- the page rendered fine but every POST returned 401, because a
// captive sheet cannot answer a Basic-auth challenge. Every earlier test set
// Basic auth explicitly and so never exercised it.
func TestSaveWiFiFromCaptiveSheetWithoutBasicAuth(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	form := strings.NewReader("ssid=HomeNet&password=hunter2&device_password=4c1bfd")
	req := httptest.NewRequest(http.MethodPost, "/save/wifi", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Deliberately NO SetBasicAuth: that is the whole point.
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatal("form submit from the captive sheet was rejected; the sheet cannot answer Basic auth")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the pending page", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Update pending") {
		t.Error("did not reach the pending page")
	}
}

// A wrong device password must still be refused, and say so on the page rather
// than returning a bare 401 body the sheet renders as a blank screen.
func TestSaveWiFiRejectsWrongDevicePassword(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	form := strings.NewReader("ssid=HomeNet&device_password=wrong")
	req := httptest.NewRequest(http.MethodPost, "/save/wifi", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	// 200 with the login page, not a 401: the sheet renders a 401 body as an
	// error rather than a document.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the login page", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not correct") {
		t.Error("no explanation shown; the user cannot tell what went wrong")
	}
	if !strings.Contains(rec.Body.String(), `action="/login"`) {
		t.Error("rejection page has no login form; the user cannot retry")
	}
}

// Logging in sets a session cookie and returns to the requested page.
func TestLoginSetsSessionAndReturnsToNext(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	form := strings.NewReader("device_password=4c1bfd&next=/")
	req := httptest.NewRequest(http.MethodPost, "/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("no session cookie set")
	}
	if !found.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if found.Secure {
		t.Error("Secure set on a plain-HTTP portal; the cookie would be dropped")
	}
	if !ValidSession(found.Value, "", "54:01:4a:4c:1b:fd") {
		t.Error("cookie value is not a valid session")
	}
}

// next must not be usable to bounce a user off-site.
func TestLoginRejectsOffsiteNext(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	form := strings.NewReader("device_password=4c1bfd&next=https://evil.example.com/x")
	req := httptest.NewRequest(http.MethodPost, "/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q -- off-site next must be discarded", loc, "/")
	}
}

// The captive-sheet flow: no cookie, submit the settings form with the password
// inline. It must authenticate, set a session, and perform the action in one
// step rather than bouncing to a login screen and losing the submission.
func TestSaveWiFiFromSheetWithInlinePasswordSetsSession(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}

	form := strings.NewReader("ssid=HomeNet&password=hunter2&device_password=4c1bfd")
	req := httptest.NewRequest(http.MethodPost, "/save/wifi", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Update pending") {
		t.Fatalf("status = %d; did not reach the pending page", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return
		}
	}
	t.Error("no session cookie set; the next request would ask for the password again")
}

// Logging out drops the session.
func TestLogoutClearsSession(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/logout", nil))

	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.MaxAge >= 0 {
			t.Errorf("logout did not expire the cookie: MaxAge = %d", c.MaxAge)
		}
	}
}

// A probe with no session must show the login form -- that is what the sheet
// displays, and it is the whole point of serving the probe through auth.
func TestCaptiveProbeShowsLoginWhenNoSession(t *testing.T) {
	t.Parallel()
	s := &Server{Store: &fakeStore{c: &config.Config{}}, MAC: "54:01:4a:4c:1b:fd"}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hotspot-detect.html", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `action="/login"`) {
		t.Error("probe did not show the login form")
	}
}
