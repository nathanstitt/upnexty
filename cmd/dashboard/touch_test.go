package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
)

func tapFixture(t *testing.T) (*Store, model.ViewModel, *config.Config) {
	t.Helper()
	// Relative to the wall clock, not a literal date: these events must be
	// current for the agenda to lay them out the way the panel would, and a
	// pinned date silently becomes "last year" and changes the behaviour under
	// test.
	now := time.Now().UTC().Truncate(time.Hour)
	cfg := &config.Config{}
	cfg.Location.Timezone = "UTC"

	evs := []calendar.Event{
		{Title: "Standup", UID: "a", Color: "#4f9cff",
			Start: now.Add(30 * time.Minute), End: now.Add(45 * time.Minute)},
	}
	store := NewStore(cfg, filepath.Join(t.TempDir(), "config.json"))
	vm := model.Build(now, cfg, evs, nil, nil, nil)
	return store, vm, cfg
}

// centreOfFirstCard returns a point inside the first event card.
func centreOfFirstCard(t *testing.T, vm model.ViewModel) (float64, float64) {
	t.Helper()
	rects := vm.Agenda.CardRects()
	if len(rects) == 0 {
		t.Fatal("fixture produced no card rects")
	}
	r := rects[0].Rect
	return r.X + r.W/2, r.Y + r.H/2
}

func TestTapOnACardOpensItsDialog(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}
	x, y := centreOfFirstCard(t, vm)

	if !handleTap(st, vm, cfg, store, x, y) {
		t.Fatal("tap on a card did not request a redraw")
	}
	if st.dlg == nil {
		t.Fatal("tap on a card did not open a dialog")
	}
	if st.dlg.Title != "Standup" {
		t.Errorf("dialog titled %q, want Standup", st.dlg.Title)
	}
}

// Empty agenda space is not a control. A tap there must neither open anything
// nor cost a render -- the panel redrawing on every stray touch would be
// visible as a flicker.
func TestTapOnEmptySpaceDoesNothing(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}

	if handleTap(st, vm, cfg, store, model.PanelWidth-10, model.PanelHeight-10) {
		t.Error("tap on empty space requested a redraw")
	}
	if st.dlg != nil {
		t.Error("tap on empty space opened a dialog")
	}
}

func TestCloseDismissesTheDialog(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}
	x, y := centreOfFirstCard(t, vm)
	handleTap(st, vm, cfg, store, x, y)
	if st.dlg == nil {
		t.Fatal("setup: no dialog open")
	}

	closeRect, _ := actionRects(st.dlg)
	cx := closeRect.X + closeRect.W/2
	cy := closeRect.Y + closeRect.H/2
	if !handleTap(st, vm, cfg, store, cx, cy) {
		t.Error("tapping close did not request a redraw")
	}
	if st.dlg != nil {
		t.Error("dialog still open after tapping close")
	}
}

// With a sheet open, the agenda underneath it is not live. Letting a tap fall
// through would let someone open a second event through the overlay, or mute
// the wrong one.
func TestTapsAreModalWhileTheDialogIsOpen(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}
	x, y := centreOfFirstCard(t, vm)
	handleTap(st, vm, cfg, store, x, y)
	opened := st.dlg

	// Tap the card again -- it is underneath the sheet now.
	if handleTap(st, vm, cfg, store, x, y) {
		t.Error("a tap absorbed by the modal requested a redraw")
	}
	if st.dlg != opened {
		t.Error("a tap through the modal changed the open dialog")
	}
}

// The whole point of the feature: the mute button hides the event and the
// choice survives a restart, which here means it reached the config file.
func TestMuteButtonPersistsAndHidesTheEvent(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}
	x, y := centreOfFirstCard(t, vm)
	handleTap(st, vm, cfg, store, x, y)
	if st.dlg == nil {
		t.Fatal("setup: no dialog open")
	}
	key := st.key

	_, actions := actionRects(st.dlg)
	if len(actions) == 0 {
		t.Fatal("dialog has no actions")
	}
	mx := actions[0].X + actions[0].W/2
	my := actions[0].Y + actions[0].H/2
	if !handleTap(st, vm, cfg, store, mx, my) {
		t.Error("tapping mute did not request a redraw")
	}
	if st.dlg != nil {
		t.Error("dialog still open after muting")
	}

	// Muting closes the sheet AND records the event.
	if !store.Config().IsMuted(key) {
		t.Fatalf("event %q was not muted in the stored config", key)
	}

	// Reload from disk: a mute that only lived in memory would be lost on the
	// next boot, which is the failure this guards.
	reloaded, err := config.Load(store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsMuted(key) {
		t.Error("mute did not survive a reload from disk")
	}
}

// A muted event's sheet offers the way back, and taking it clears the mute.
func TestMuteButtonTogglesBackToVisible(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}
	x, y := centreOfFirstCard(t, vm)

	handleTap(st, vm, cfg, store, x, y)
	_, actions := actionRects(st.dlg)
	handleTap(st, vm, cfg, store, actions[0].X+1, actions[0].Y+1)
	key := st.key
	if !store.Config().IsMuted(key) {
		t.Fatal("setup: event was not muted")
	}

	// Re-open with the now-muted config and take the action again.
	cfg2 := store.Config()
	handleTap(st, vm, cfg2, store, x, y)
	if st.dlg == nil {
		t.Fatal("could not reopen the dialog")
	}
	if st.dlg.Actions[0].Label != "Show on panel" {
		t.Errorf("muted event offers %q, want \"Show on panel\"", st.dlg.Actions[0].Label)
	}
	_, actions2 := actionRects(st.dlg)
	handleTap(st, vm, cfg2, store, actions2[0].X+1, actions2[0].Y+1)
	if store.Config().IsMuted(key) {
		t.Error("event is still muted after tapping Show on panel")
	}
}

// Every control must be reachable by a fingertip. 44px is the usual minimum
// for a phone held at arm's length; this panel is read and operated from
// further away, so the floor here is higher.
func TestDialogControlsAreFingerSized(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{}
	x, y := centreOfFirstCard(t, vm)
	handleTap(st, vm, cfg, store, x, y)

	const minSide = 56.0
	closeRect, actions := actionRects(st.dlg)
	all := append([]model.Rect{closeRect}, actions...)
	for i, r := range all {
		if r.W < minSide || r.H < minSide {
			t.Errorf("control %d is %vx%v, smaller than the %v minimum", i, r.W, r.H, minSide)
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > model.PanelWidth || r.Y+r.H > model.PanelHeight {
			t.Errorf("control %d at %+v is partly off the panel", i, r)
		}
	}
}

// Publishing a dialog and then rendering must not deadlock.
//
// The first version of this shared one mutex between the framebuffer and the
// dialog pointer, so the touch callback took a non-reentrant sync.Mutex twice
// and wedged the process on the very first tap -- the tick loop stopped with
// it, and the panel simply froze on whatever frame it had. Nothing in the unit
// tests noticed, because they call handleTap directly and never go through the
// callback the way main wires it.
//
// This reproduces main's ordering: setOpenDialog, then a render that reads the
// dialog back under renderMu.
func TestSetOpenDialogThenRenderDoesNotDeadlock(t *testing.T) {
	d := &model.Dialog{Title: "Standup"}

	// main's touch callback is exactly this: publish, then render. render
	// takes renderMu and reads the dialog back. If setOpenDialog and the
	// render share one non-reentrant mutex, the second acquisition blocks
	// forever.
	render := func() {
		renderMu.Lock()
		defer renderMu.Unlock()
		_ = currentDialog()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		setOpenDialog(d)
		render()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("setOpenDialog followed by a render deadlocked")
	}

	if currentDialog() != d {
		t.Error("dialog was not published for the next render")
	}
	setOpenDialog(nil)
}

// devInfo is a stub gatherer: the real one shells out to wpa_cli.
func devInfo() model.DeviceInfo {
	return model.DeviceInfo{
		IP: "192.168.1.81", SSID: "Argosity", Hostname: "luckfox", Password: "4c1bfd",
	}
}

// Tapping the top-left corner raises the device sheet.
//
// The corner is the only way to see the board's address from in front of it:
// the IP is a DHCP lease that changes, and the board runs no mDNS, so there is
// no name to fall back on.
func TestCornerTapOpensTheDeviceDialog(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{deviceInfo: devInfo}

	if !handleTap(st, vm, cfg, store, 10, 10) {
		t.Fatal("corner tap did not request a redraw")
	}
	if st.dlg == nil {
		t.Fatal("corner tap opened no dialog")
	}
	if st.dlg.Eyebrow != "Device" {
		t.Errorf("Eyebrow = %q, want the device sheet", st.dlg.Eyebrow)
	}

	var got []string
	for _, r := range st.dlg.Rows {
		got = append(got, r.Label+"="+r.Value)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"Address=192.168.1.81", "Password=4c1bfd"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rows %v are missing %q", got, want)
		}
	}
}

// The corner region is bounded: taps outside it must not raise the sheet.
//
// It sits over the clock, which has no other behaviour, so an oversized region
// would swallow taps meant for the agenda -- and the sheet shows a password.
func TestCornerTapRegionIsBounded(t *testing.T) {
	inside := [][2]float64{{0, 0}, {50, 50}, {99, 99}}
	outside := [][2]float64{{101, 50}, {50, 101}, {150, 150}, {200, 20}}

	for _, p := range inside {
		store, vm, cfg := tapFixture(t)
		st := &dialogState{deviceInfo: devInfo}
		if !handleTap(st, vm, cfg, store, p[0], p[1]) || st.dlg == nil {
			t.Errorf("(%v,%v) is inside the %vpx corner but opened nothing",
				p[0], p[1], model.CornerTapPx)
		}
	}
	for _, p := range outside {
		store, vm, cfg := tapFixture(t)
		st := &dialogState{deviceInfo: devInfo}
		handleTap(st, vm, cfg, store, p[0], p[1])
		if st.dlg != nil && st.dlg.Eyebrow == "Device" {
			t.Errorf("(%v,%v) is outside the %vpx corner but raised the device sheet",
				p[0], p[1], model.CornerTapPx)
		}
	}
}

// The device sheet closes sooner than the rest: it puts the admin password on
// a wall, and a panel left sitting on it is that credential on display.
func TestDeviceDialogClosesSoonerThanTheEventSheet(t *testing.T) {
	store, vm, cfg := tapFixture(t)

	st := &dialogState{deviceInfo: devInfo}
	handleTap(st, vm, cfg, store, 10, 10)
	device := st.dialogTimeout()

	st2 := &dialogState{deviceInfo: devInfo}
	x, y := centreOfFirstCard(t, vm)
	handleTap(st2, vm, cfg, store, x, y)
	event := st2.dialogTimeout()

	if device >= event {
		t.Errorf("device sheet times out after %v, no sooner than the event sheet's %v",
			device, event)
	}
}

// A state with no gatherer must not panic: the corner is still tappable.
func TestCornerTapWithoutAGathererIsInert(t *testing.T) {
	store, vm, cfg := tapFixture(t)
	st := &dialogState{} // no deviceInfo

	if handleTap(st, vm, cfg, store, 10, 10) {
		t.Error("corner tap asked for a redraw with no info to show")
	}
	if st.dlg != nil {
		t.Error("corner tap opened a dialog with no info to show")
	}
}
