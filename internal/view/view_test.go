package view

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// fixtureVM returns a populated view model. ViewModel now carries Hourly
// directly (Contract 1), so Render only ever needs the one value.
func fixtureVM(t *testing.T) model.ViewModel {
	t.Helper()
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"

	evs := []calendar.Event{
		// Standup and Design Review Sync start only 6 minutes apart. Design
		// Review Sync's long title collides with Standup's label and gets
		// pushed ~41px right of its own block (drift threshold is 80px), so
		// PlaceLabels marks it Drifted — this fixture exists specifically to
		// exercise the leader-mark ({{if .Drifted}}) template branch, which
		// two well-separated events would never trigger.
		{Title: "Standup", Color: "#4f9cff", Start: now.Add(10 * time.Minute), End: now.Add(20 * time.Minute)},
		{Title: "Design Review Sync", Color: "#ff7a59", Location: "Room B",
			Start: now.Add(16 * time.Minute), End: now.Add(45 * time.Minute)},
		{Title: "Company Holiday", AllDay: true, Color: "#4f9cff",
			Start: now.Truncate(24 * time.Hour), End: now.Add(24 * time.Hour)},
	}
	w := &weather.Weather{
		Current: weather.Conditions{TempF: 72, Code: 2, Time: now},
		Daily: []weather.DayPoint{
			{Date: now, HiF: 88, LoF: 64, PrecipProb: 20, Code: 2},
			{Date: now.AddDate(0, 0, 1), HiF: 90, LoF: 66, PrecipProb: 0, Code: 0},
		},
	}
	for i := 0; i < 8; i++ {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: now.Add(time.Duration(i) * time.Hour), TempF: 70 + float64(i), PrecipProb: i * 5,
		})
	}
	return model.Build(now, c, evs, w, nil, nil)
}

func TestRenderProducesCompleteDocument(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<!DOCTYPE html>", "<html", "</html>", "<style>"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// CSS must be inlined; the board loads no external resources.
	if strings.Contains(got, `<link`) {
		t.Error("output has a <link> tag; CSS must be inlined")
	}
	if strings.Contains(got, "<script") {
		t.Error("output has a <script> tag; omnidoc discards scripts")
	}
}

func TestRenderIncludesEventTitlesAndClock(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Standup", "Design Review Sync", "Company Holiday", "10:42"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderEscapesTitles(t *testing.T) {
	vm := fixtureVM(t)
	// Find an event card by kind rather than assuming index 0: the row opens
	// with a gap chip whenever there is free time before the first event, and
	// a title set on a chip is never rendered, so the test would pass while
	// proving nothing.
	idx := -1
	for i, c := range vm.Agenda.Cards {
		if c.Kind == model.CardEvent {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("fixture has no event card to escape")
	}
	vm.Agenda.Cards[idx].Event.Title = `Tom & Jerry <script>`
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<script>") {
		t.Error("title was not HTML-escaped")
	}
	if !strings.Contains(got, "&amp;") {
		t.Error("ampersand was not escaped")
	}
}

func TestRenderHandlesEmptyModel(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	got, err := Render(model.Build(now, c, nil, nil, []string{"weather: timeout"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "10:42") {
		t.Error("clock must render even with no data")
	}
}

// Both directions of the Loading branch. An empty agenda is ambiguous on its
// own -- "not fetched yet" and "nothing scheduled" produce the identical model
// -- so the panel must not assert the second while the first is true.
func TestRenderShowsFetchingWhileLoading(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, nil, nil)
	vm.Loading = true

	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Fetching") {
		t.Error("agenda must read as loading before the first fetch completes")
	}
	if strings.Contains(got, "No more events today") {
		t.Error("panel claimed the day is clear before any fetch happened")
	}
	if strings.Contains(got, "Done for the day") {
		t.Error("now block claimed the day is done before any fetch happened")
	}
}

func TestRenderShowsNoEventsOnceLoaded(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, nil, nil)
	vm.Loading = false

	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "No more events today") {
		t.Error("a genuinely empty day must still say so once fetched")
	}
	if strings.Contains(got, "Fetching") {
		t.Error("loading text leaked into a loaded render")
	}
}

// The setup hint outranks Loading: a board that cannot associate will never
// fetch, so telling the user it is "Fetching..." would be a lie that hides the
// only thing they can act on.
func TestRenderSetupHintOutranksLoading(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, nil,
		&model.SetupHint{APName: "upnext-1bfd", Password: "4c1bfd"})
	vm.Loading = true

	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "upnext-1bfd") {
		t.Error("setup hint must win over the loading state")
	}
	if strings.Contains(got, "Fetching&hellip;") {
		t.Error("loading headline rendered over the setup hint")
	}
}

// TestRenderShowsStaleFlagWhenStale and TestRenderHidesStaleFlagWhenFresh
// assert both directions of the {{if .VM.Stale}} branch. Stale is meant to
// mean "this refresh cycle had a failure, so what's on screen may be
// last-good rather than current" — an indicator stuck on is exactly as
// wrong as one stuck off, and on an unattended kiosk nobody is watching for
// either failure mode, so both are asserted rather than just the happy path.
func TestRenderShowsStaleFlagWhenStale(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, []string{"weather: timeout"}, nil)
	if !vm.Stale {
		t.Fatal("fixture does not set Stale; test no longer exercises this branch")
	}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `id="stale-flag"`) {
		t.Error(`output missing id="stale-flag" when Stale is true`)
	}
}

// TestRenderShowsErrorTextWhenStale guards against the bug where the
// template's {{if .VM.Stale}}...{{else if .VM.Errors}}... branch made the
// Errors arm provably unreachable (model.Build sets Stale := len(errs) > 0,
// so the two conditions are always equivalent) — the actual diagnostic text,
// e.g. "ical(Personal): GET ...: 401 Unauthorized", was computed, carried
// through three layers, and then silently discarded, leaving only the bare
// word "Stale" on an unattended panel with no way to tell what actually
// failed. Both #stale-flag and #errors must render together.
func TestRenderShowsErrorTextWhenStale(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	wantErr := "ical(Personal): GET https://example.com/cal.ics: 401 Unauthorized"
	vm := model.Build(now, c, nil, nil, []string{wantErr}, nil)
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `id="errors"`) {
		t.Error(`output missing id="errors" when Errors is non-empty`)
	}
	if !strings.Contains(got, wantErr) {
		t.Errorf("output missing error text %q", wantErr)
	}
	// Stale and Errors must render together, not as alternatives.
	if !strings.Contains(got, `id="stale-flag"`) {
		t.Error(`output missing id="stale-flag" alongside errors`)
	}
}

func TestRenderHidesStaleFlagWhenFresh(t *testing.T) {
	vm := fixtureVM(t)
	if vm.Stale {
		t.Fatal("fixture unexpectedly sets Stale; test no longer exercises this branch")
	}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `id="stale-flag"`) {
		t.Error(`output has id="stale-flag" when Stale is false`)
	}
}

func TestRenderShowsSetupHint(t *testing.T) {
	vm := fixtureVM(t)
	vm.Setup = &model.SetupHint{APName: "upnext-1bfd", Password: "4c1bfd"}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"upnext-1bfd", "4c1bfd", "Set me up"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderOmitsSetupHintWhenConfigured(t *testing.T) {
	vm := fixtureVM(t)
	vm.Setup = nil
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	// Match the div, not the bare substring "setup-hint": that string also
	// appears in the inlined <style> block's #setup-hint selector, which is
	// always present regardless of whether the {{with .VM.Setup}} div rendered.
	if strings.Contains(got, `id="setup-hint"`) {
		t.Error("setup hint rendered when the board is configured")
	}
}

// TestRenderSetupHintShowsUnavailablePasswordWhenEmpty guards resolution #2:
// an empty Password (DefaultPassword's return when the MAC is unreadable or
// malformed) must never render as a blank credential -- that reads as a real
// but invisible password, which is worse than no hint at all. The template
// must show explicit "unavailable" text instead.
func TestRenderSetupHintShowsUnavailablePasswordWhenEmpty(t *testing.T) {
	vm := fixtureVM(t)
	vm.Setup = &model.SetupHint{APName: "upnext-setup", Password: ""}
	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "upnext-setup") {
		t.Error("output missing the fallback AP name")
	}
	if !strings.Contains(got, "unavailable") {
		t.Error(`output missing "unavailable" when Password is empty`)
	}
}

func TestRenderMatchesGolden(t *testing.T) {
	got, err := Render(fixtureVM(t))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/dashboard.html", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile("testdata/dashboard.html")
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if got != string(want) {
		t.Error("HTML differs from golden; re-run with UPDATE_GOLDEN=1 if intended")
	}
}

// The agenda's arithmetic lives in internal/model but the layout it predicts is
// produced by style.css. Nothing makes the two agree: when the row's 18px
// padding was missing from the model, the NOW bar rendered beside the current
// card instead of through it and every test still passed.
//
// This asserts the stylesheet still declares what the model assumes. Each check
// is scoped to the rule that owns the value -- a bare substring search matches
// the same number in an unrelated rule and silently passes.
func TestStylesheetMatchesModelGeometry(t *testing.T) {
	css, err := assetFS.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)

	// ruleBody returns the declarations of the first rule with this selector.
	ruleBody := func(selector string) string {
		t.Helper()
		i := strings.Index(s, selector+" {")
		if i < 0 {
			t.Fatalf("style.css has no %q rule", selector)
		}
		body := s[i+len(selector)+2:]
		end := strings.Index(body, "}")
		if end < 0 {
			t.Fatalf("%q rule is unterminated", selector)
		}
		return body[:end]
	}

	for _, c := range []struct {
		selector, decl, why string
	}{
		{"#agenda-row", fmt.Sprintf("gap: %dpx", model.CardGapPx),
			"the row's gap is the spacing model.BuildAgenda advances x by"},
		{"#agenda-row", fmt.Sprintf("padding: 10px %dpx 12px", model.CardPadPx),
			"model.CardPadPx offsets the row against the unpadded NOW bar"},

		// The dialog's hit rects are stated in cmd/dashboard/touch.go and must
		// match what is painted. Nothing reads back the rasterized page, so a
		// drift here is a button that looks right and does nothing when
		// tapped -- silent, and only findable by hand on the hardware.
		{"#dlg", "left: 380px", "actionRects anchors the sheet at the left-panel seam"},
		{"#dlg", "width: 1540px", "actionRects computes the close button from the sheet width"},
		{"#dlg-head", "top: 20px", "actionRects places the close button at this y"},
		{"#dlg-actions", "left: 44px", "actionRects starts the button row at this x"},
		{"#dlg-actions", "top: 394px", "actionRects places the buttons at this y"},
		{".dlg-close", "width: 150px", "actionRects sizes the close hit target"},
		{".dlg-close", "height: 60px", "actionRects sizes the close hit target"},
		{".dlg-btn", "width: 300px", "actionRects sizes and steps the action buttons"},
		{".dlg-btn", "height: 64px", "actionRects sizes the action buttons"},
		{"#dlg-actions", "gap: 16px", "actionRects steps x by button width plus this gap"},

		// The agenda band's vertical geometry is what CardRects hit-tests
		// against.
		{".ad-ribbon", fmt.Sprintf("height: %dpx", model.RibbonHeightPx),
			"model.RibbonHeightPx is the top of the card band in CardRects"},
		{"#agenda-row", fmt.Sprintf("height: %dpx", model.AgendaHeightPx),
			"model.AgendaHeightPx is the card band's height in CardRects"},
	} {
		if body := ruleBody(c.selector); !strings.Contains(body, c.decl) {
			t.Errorf("%s does not declare %q: %s", c.selector, c.decl, c.why)
		}
	}
}

// While an association attempt runs the panel must report progress rather than
// the join instructions. The browser that submitted the credentials loses its
// connection when the AP comes down, so this is the only surface left that can
// tell the user anything.
func TestRenderShowsConnectingInsteadOfJoinSteps(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, nil, &model.SetupHint{
		APName: "upnext-1bfd", Password: "4c1bfd",
		URL: "http://192.168.4.1", Connecting: "HomeNet",
	})

	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "HomeNet") {
		t.Error("panel does not name the network being joined")
	}
	// The join steps must be gone: telling the user to join the setup AP while
	// that AP is being torn down sends them at a network about to vanish.
	if strings.Contains(got, "Join Wi-Fi") {
		t.Error("panel still shows the join instructions during an attempt")
	}
	if strings.Contains(got, "4c1bfd") {
		t.Error("panel still shows the setup password during an attempt")
	}
}

// Once the attempt finishes, the join steps come back -- a failed association
// must leave the user able to retry.
func TestRenderRestoresJoinStepsWhenNotConnecting(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	vm := model.Build(now, c, nil, nil, nil, &model.SetupHint{
		APName: "upnext-1bfd", Password: "4c1bfd", URL: "http://192.168.4.1",
	})

	got, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Join Wi-Fi") {
		t.Error("join instructions missing when no attempt is running")
	}
}
