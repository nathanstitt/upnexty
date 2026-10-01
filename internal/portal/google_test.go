package portal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
)

// fakeLinker is a GoogleLinker whose every step is controllable, so a test can
// hold the flow open and inspect the state the panel would see mid-flight.
type fakeLinker struct {
	mu sync.Mutex

	startErr   error
	code       PairingCode
	deviceCode string

	// release gates AwaitToken so a test can observe the in-flight state
	// before letting the flow finish. Nil means return immediately.
	release  chan struct{}
	account  string
	awaitErr error

	starts       int
	accounts     []string
	calendars    []GoogleCalendar
	calendarsErr error
	listed       []string
	disconnected []string
	disconnErr   error
}

func (f *fakeLinker) StartDeviceFlow(context.Context) (PairingCode, any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.startErr != nil {
		return PairingCode{}, nil, f.startErr
	}
	return f.code, f.deviceCode, nil
}

func (f *fakeLinker) AwaitToken(ctx context.Context, handle any) (string, error) {
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.account, f.awaitErr
}

func (f *fakeLinker) Accounts() ([]string, error) { return f.accounts, nil }

func (f *fakeLinker) Calendars(_ context.Context, account string) ([]GoogleCalendar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, account)
	if f.calendarsErr != nil {
		return nil, f.calendarsErr
	}
	return f.calendars, nil
}

func (f *fakeLinker) Disconnect(account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disconnErr != nil {
		return f.disconnErr
	}
	f.disconnected = append(f.disconnected, account)
	return nil
}

func (f *fakeLinker) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func postAuthed(t *testing.T, s *Server, path string, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

// The code has to reach the PANEL. The browser that started the flow is
// answered before the user has even picked up their phone, so PairingCode()
// -- which the render loop polls -- is the only route to them.
func TestGoogleConnectPublishesCodeForThePanel(t *testing.T) {
	s := newTestServer(t)
	link := &fakeLinker{
		code:       PairingCode{UserCode: "VSBR-DGFB", VerificationURL: "https://www.google.com/device"},
		deviceCode: "dev-123",
		release:    make(chan struct{}),
		account:    "me@example.com",
	}
	s.Google = link

	if got := s.PairingCode(); got.UserCode != "" {
		t.Fatalf("a code was published before any flow started: %+v", got)
	}

	w := postAuthed(t, s, "/google/connect", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect; the flow must not block the response", w.Code)
	}

	// Mid-flight: this is what the render loop reads each tick.
	got := s.PairingCode()
	if got.UserCode != "VSBR-DGFB" {
		t.Errorf("PairingCode().UserCode = %q, want the code Google issued", got.UserCode)
	}
	if got.VerificationURL != "https://www.google.com/device" {
		t.Errorf("VerificationURL = %q", got.VerificationURL)
	}

	close(link.release)

	// And it must clear once the flow ends, or the panel shows a dead code
	// forever.
	waitFor(t, func() bool { return s.PairingCode().UserCode == "" },
		"code was still published after the flow finished")
}

// A second submit while a flow is running must not mint a second code: the
// panel has one place to show one, and the user pressing the button again
// because nothing has appeared yet is the expected way to get here.
func TestGoogleConnectSingleFlights(t *testing.T) {
	s := newTestServer(t)
	link := &fakeLinker{
		code:       PairingCode{UserCode: "AAAA-BBBB", VerificationURL: "https://www.google.com/device"},
		deviceCode: "dev-1",
		release:    make(chan struct{}),
	}
	s.Google = link

	postAuthed(t, s, "/google/connect", "")
	postAuthed(t, s, "/google/connect", "")
	postAuthed(t, s, "/google/connect", "")

	if n := link.startCount(); n != 1 {
		t.Errorf("started %d flows, want 1 -- a second code would replace the one on the panel", n)
	}
	close(link.release)
}

// A failure to even get a code is reported on the page the user is looking at,
// because that request is still open. Contrast with a failure during the poll,
// which can only surface on the next page load.
func TestGoogleConnectReportsStartFailureInline(t *testing.T) {
	s := newTestServer(t)
	s.Google = &fakeLinker{startErr: errors.New("no route to host")}

	w := postAuthed(t, s, "/google/connect", "")
	if w.Code == http.StatusSeeOther {
		t.Fatal("a failed start redirected; the user would never learn it failed")
	}
	if !strings.Contains(w.Body.String(), "no route to host") {
		t.Errorf("body does not carry the reason:\n%s", w.Body.String())
	}
	// And the flag must be released, or the feature is wedged until reboot.
	if s.pairing.Load() {
		t.Error("pairing flag left set after a failed start; no later attempt could run")
	}
}

// Disconnecting an account must also drop the calendars it fed. Leaving them
// points every future fetch at a token that no longer exists, so the row goes
// stale behind an error the user has already dealt with.
func TestGoogleDisconnectRemovesThatAccountsCalendars(t *testing.T) {
	s := newTestServer(t)
	link := &fakeLinker{}
	s.Google = link
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "Legacy", URL: "https://example.com/a.ics"},
			{Name: "Mine", Kind: config.KindGoogle, CalID: "me@x.com", Account: "me@x.com"},
			{Name: "Work", Kind: config.KindGoogle, CalID: "w@x.com", Account: "me@x.com"},
			{Name: "Other", Kind: config.KindGoogle, CalID: "o@y.com", Account: "other@y.com"},
		},
	})

	w := postAuthed(t, s, "/google/disconnect", "account=me%40x.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect", w.Code)
	}
	if len(link.disconnected) != 1 || link.disconnected[0] != "me@x.com" {
		t.Errorf("disconnected = %v, want [me@x.com]", link.disconnected)
	}

	var names []string
	for _, src := range s.Store.Config().Calendars {
		names = append(names, src.Name)
	}
	want := []string{"Legacy", "Other"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("calendars after disconnect = %v, want %v -- the iCal feed and the other account's calendar must survive", names, want)
	}
}

// With no linker configured the routes must refuse rather than panic: that is
// a board built without OAuth credentials, which is a supported state.
func TestGoogleRoutesWithoutALinker(t *testing.T) {
	s := newTestServer(t)
	s.Google = nil
	for _, path := range []string{"/google/connect", "/google/disconnect"} {
		w := postAuthed(t, s, path, "account=x@y.com")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error(msg)
}

// Saving the calendars form must not delete Google sources.
//
// The form carries name/url pairs and the handler rebuilds the list from the
// url values, so a source with no URL -- which every Google calendar is --
// would be dropped by saving any unrelated change on that form. Found while
// building the settings UI; it would have presented as calendars vanishing
// after an unrelated edit.
func TestSaveCalendarsPreservesGoogleSources(t *testing.T) {
	s := newTestServer(t)
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "Legacy", Color: "#111", URL: "https://example.com/a.ics"},
			{Name: "Mine", Color: "#222", Kind: config.KindGoogle, CalID: "me@x.com", Account: "me@x.com"},
		},
	})

	// Edit only the iCal feed's name, exactly as the form submits it.
	w := postAuthed(t, s, "/save/calendars", "name=Renamed&url=https%3A%2F%2Fexample.com%2Fa.ics")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect; body:\n%s", w.Code, w.Body.String())
	}

	var got []string
	for _, src := range s.Store.Config().Calendars {
		got = append(got, src.Name+"/"+src.SourceKind())
	}
	want := map[string]bool{"Renamed/ical": true, "Mine/google": true}
	if len(got) != 2 {
		t.Fatalf("calendars = %v, want both the renamed feed and the Google source", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected calendar %q; got %v", g, got)
		}
	}
}

// The settings page must not render a Google source as an editable feed row:
// it has no URL, so it would show as a blank box inviting someone to type an
// address into a calendar that is not addressed that way.
func TestSettingsRendersOnlyICalFeedsAsRows(t *testing.T) {
	s := newTestServer(t)
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "Legacy", URL: "https://example.com/a.ics"},
			{Name: "GoogleOne", Kind: config.KindGoogle, CalID: "me@x.com", Account: "me@x.com"},
		},
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	body := w.Body.String()

	if !strings.Contains(body, "https://example.com/a.ics") {
		t.Error("the iCal feed's URL is not on the page")
	}
	// The Google source's name must not appear inside a url input.
	if strings.Contains(body, `value="GoogleOne"`) {
		t.Error("a Google calendar was rendered as an editable feed row")
	}
}

// With a linker present the section appears; without one it is absent
// entirely, because a button that cannot work is worse than no button.
func TestSettingsShowsGoogleSectionOnlyWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		linker  GoogleLinker
		wantSec bool
	}{
		{"no credentials", nil, false},
		{"credentials present", &fakeLinker{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			s.Google = tc.linker
			req := httptest.NewRequest("GET", "/", nil)
			req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			body := w.Body.String()
			if got := strings.Contains(body, "Connect Google Calendar"); got != tc.wantSec {
				t.Errorf("section present = %v, want %v", got, tc.wantSec)
			}
		})
	}
}

// A connected account is listed with the calendars it feeds, so disconnecting
// says what it is about to remove.
func TestSettingsListsConnectedAccounts(t *testing.T) {
	s := newTestServer(t)
	s.Google = &fakeLinker{accounts: []string{"me@x.com"}}
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "Work", Kind: config.KindGoogle, CalID: "w@x.com", Account: "me@x.com"},
			{Name: "Home", Kind: config.KindGoogle, CalID: "h@x.com", Account: "me@x.com"},
			{Name: "Other", Kind: config.KindGoogle, CalID: "o@y.com", Account: "other@y.com"},
		},
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	body := w.Body.String()

	if !strings.Contains(body, "me@x.com") {
		t.Error("the connected account is not listed")
	}
	if !strings.Contains(body, "Work") || !strings.Contains(body, "Home") {
		t.Error("the account's calendars are not listed")
	}
	// The other account is not connected, so its calendar must not be
	// attributed to this one.
	if strings.Contains(body, "Other") {
		t.Error("a calendar belonging to an unconnected account was listed")
	}
	if !strings.Contains(body, "remove 2 calendars") {
		t.Error("the disconnect button does not say what it removes")
	}
}

// Connecting an account must leave the panel fetching something: the primary
// calendar is adopted as a source the moment the token lands. Before this, a
// fresh account showed "Connected, but no calendars from this account are
// shown yet" until config.json was edited by hand.
func TestGoogleConnectAdoptsThePrimaryCalendar(t *testing.T) {
	s := newTestServer(t)
	link := &fakeLinker{
		code:       PairingCode{UserCode: "AAAA-BBBB", VerificationURL: "https://www.google.com/device"},
		deviceCode: "dev-1",
		account:    "me@x.com",
		calendars: []GoogleCalendar{
			{ID: "me@x.com", Name: "me@x.com", Color: "#9fe1e7", Primary: true},
			{ID: "team@group.calendar.google.com", Name: "Team"},
		},
	}
	s.Google = link

	postAuthed(t, s, "/google/connect", "")
	waitFor(t, func() bool {
		for _, src := range s.Store.Config().Calendars {
			if src.Account == "me@x.com" {
				return true
			}
		}
		return false
	}, "no source was added for the connected account")

	var added []config.CalendarSource
	for _, src := range s.Store.Config().Calendars {
		if src.Account == "me@x.com" {
			added = append(added, src)
		}
	}
	if len(added) != 1 {
		t.Fatalf("connect added %d sources %+v, want just the primary", len(added), added)
	}
	got := added[0]
	if got.Kind != config.KindGoogle || got.CalID != "me@x.com" || got.Name != "me@x.com" || got.Color != "#9fe1e7" {
		t.Errorf("adopted source = %+v; want the primary calendar with Google's colour", got)
	}
	// The iCal feed the fixture started with is untouched.
	if s.Store.Config().Calendars[0].URL != "https://example.com/w.ics" {
		t.Error("connecting an account disturbed the existing iCal feed")
	}

	// Reconnecting the same account (a revoked token, say) must not add a
	// second copy.
	postAuthed(t, s, "/google/connect", "")
	waitFor(t, func() bool { return !s.pairing.Load() }, "second flow did not finish")
	n := 0
	for _, src := range s.Store.Config().Calendars {
		if src.Account == "me@x.com" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("after a reconnect the account has %d sources, want 1", n)
	}
}

// A listing failure after a successful pairing keeps the account connected
// and says what went wrong, rather than dropping the token or hiding it.
func TestGoogleConnectReportsWhenTheListingFails(t *testing.T) {
	s := newTestServer(t)
	link := &fakeLinker{
		code:         PairingCode{UserCode: "AAAA-BBBB"},
		deviceCode:   "dev-1",
		account:      "me@x.com",
		calendarsErr: errors.New("calendarList: 403"),
	}
	s.Google = link
	postAuthed(t, s, "/google/connect", "")
	waitFor(t, func() bool { return !s.pairing.Load() }, "flow did not finish")
	if got := s.getGoogleErr(); !strings.Contains(got, "could not add its calendar") || !strings.Contains(got, "403") {
		t.Errorf("GoogleError = %q; want it to say the account connected but its calendar could not be added, with the cause", got)
	}
	for _, src := range s.Store.Config().Calendars {
		if src.Account == "me@x.com" {
			t.Errorf("a source was added although the listing failed: %+v", src)
		}
	}
}

// The picker rewrites one account's sources from the ticked boxes: existing
// ones keep their name and colour, new ones take the listed name and colour,
// unticked ones go, and everything else in the config is left alone.
func TestGoogleCalendarsPickerRewritesOneAccountsSources(t *testing.T) {
	s := newTestServer(t)
	s.Google = &fakeLinker{accounts: []string{"me@x.com"}}
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "Work feed", Color: "#111111", URL: "https://example.com/w.ics"},
			{Name: "Mine", Color: "#222222", Kind: config.KindGoogle, CalID: "me@x.com", Account: "me@x.com"},
			{Name: "Old", Color: "#333333", Kind: config.KindGoogle, CalID: "old@x.com", Account: "me@x.com"},
			{Name: "Theirs", Color: "#444444", Kind: config.KindGoogle, CalID: "o@y.com", Account: "other@y.com"},
		},
	})
	w := postAuthed(t, s, "/google/calendars",
		"account=me%40x.com&cal=me%40x.com&cal=team%40g.com&name_team%40g.com=Team+cal&color_team%40g.com=%23abcdef")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	got := s.Store.Config().Calendars
	want := []config.CalendarSource{
		{Name: "Work feed", Color: "#111111", URL: "https://example.com/w.ics"},
		{Name: "Theirs", Color: "#444444", Kind: config.KindGoogle, CalID: "o@y.com", Account: "other@y.com"},
		{Name: "Mine", Color: "#222222", Kind: config.KindGoogle, CalID: "me@x.com", Account: "me@x.com"},
		{Name: "Team cal", Color: "#abcdef", Kind: config.KindGoogle, CalID: "team@g.com", Account: "me@x.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sources %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("source %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A bad colour from the form falls back to the palette, never into the
	// stylesheet.
	postAuthed(t, s, "/google/calendars",
		"account=me%40x.com&cal=x%40g.com&name_x%40g.com=X&color_x%40g.com=red%3Bbackground")
	for _, src := range s.Store.Config().Calendars {
		if src.CalID == "x@g.com" && !validHexColor(src.Color) {
			t.Errorf("colour %q reached the config unvalidated", src.Color)
		}
	}
}

// The settings page shows the picker with the panel's calendars ticked, and
// falls back to the configured list with a message when Google cannot be
// asked.
func TestSettingsRendersTheCalendarPicker(t *testing.T) {
	s := newTestServer(t)
	s.Google = &fakeLinker{
		accounts: []string{"me@x.com"},
		calendars: []GoogleCalendar{
			{ID: "me@x.com", Name: "me@x.com", Primary: true},
			{ID: "team@g.com", Name: "Team", Color: "#abcdef"},
		},
	}
	s.Store.SetConfig(&config.Config{
		Calendars: []config.CalendarSource{
			{Name: "Mine", Kind: config.KindGoogle, CalID: "me@x.com", Account: "me@x.com"},
		},
	})
	get := func() string {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(testSession(t, "", "54:01:4a:4c:1b:fd"))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Body.String()
	}
	body := get()
	if !strings.Contains(body, `name="cal" value="me@x.com" checked`) {
		t.Error("the calendar the panel shows is not ticked")
	}
	if !strings.Contains(body, `name="cal" value="team@g.com">`) || strings.Contains(body, `value="team@g.com" checked`) {
		t.Error("the calendar the panel does not show should be listed unticked")
	}
	if !strings.Contains(body, `name="color_team@g.com" value="#abcdef"`) {
		t.Error("the listed colour is not carried in the form")
	}
	if !strings.Contains(body, "(primary)") {
		t.Error("the primary calendar is not marked")
	}
	if strings.Contains(body, "no calendars from this account") {
		t.Error("the empty-state hint shows although the picker is available")
	}

	s.Google = &fakeLinker{accounts: []string{"me@x.com"}, calendarsErr: errors.New("timeout")}
	body = get()
	if !strings.Contains(body, "Could not list this account") || !strings.Contains(body, "Mine") {
		t.Error("with the listing failed, the page should say so and still show the configured calendars")
	}
}
