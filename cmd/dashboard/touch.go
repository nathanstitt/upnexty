package main

import (
	"context"
	"log"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/touch"
)

// dialogState is the panel's interactive state: which dialog is open, and what
// it is about. Guarded by the render loop rather than a mutex -- every read and
// write happens on the goroutine that owns rendering, and taps arrive as
// messages rather than as concurrent callers.
type dialogState struct {
	dlg *model.Dialog
	// key identifies the event the open dialog describes, so the mute action
	// knows its target without the dialog carrying model state.
	key   string
	muted config.MutedEvent
	// openedAt drives the idle timeout.
	openedAt time.Time
	// timeout is how long this particular dialog may sit open. The device
	// sheet closes sooner than the rest because it puts a password on a wall.
	timeout time.Duration

	// deviceInfo gathers what the device sheet reports. It is a function
	// rather than a value because it shells out to wpa_cli: doing that on
	// every tap, for a sheet raised a few times a year, would put a subprocess
	// in the path of every card tap.
	deviceInfo func() model.DeviceInfo
}

// dialogTimeout closes a dialog nobody is interacting with.
//
// A wall panel has no "walk away" event: someone taps a card, reads it, and
// leaves. Without this the panel would sit on a stale sheet indefinitely,
// hiding the clock and the agenda it exists to show. The close control is
// still the documented way out -- this is the backstop for the times it is not
// used, which on an unattended display is most of them.
const dialogTimeout = 45 * time.Second

// deviceDialogTimeout is shorter: this sheet shows the admin password, and a
// wall panel left sitting on it is the credential on display to the room. Long
// enough to read an IP and a six-character password out loud, not long enough
// to forget about.
const deviceDialogTimeout = 20 * time.Second

// actionRects returns where the dialog's controls land on the panel.
//
// These mirror the geometry in style.css. They are stated here rather than
// measured from the render because nothing reads back the rasterized page --
// and a tap that misses its button is indistinguishable from a dead panel, so
// the two must be pinned together by a test (see TestDialogHitRectsMatchCSS).
func actionRects(d *model.Dialog) (close model.Rect, actions []model.Rect) {
	const (
		sheetX = 380.0 // #dlg left edge
		sheetW = 1540.0
		padL   = 44.0 // #dlg-head / #dlg-actions left inset
		padR   = 40.0

		closeW, closeH = 150.0, 60.0
		closeTop       = 20.0 // #dlg-head top

		btnW, btnH = 300.0, 64.0
		btnTop     = 394.0 // #dlg-actions top
		btnGap     = 16.0
	)

	close = model.Rect{
		X: sheetX + sheetW - padR - closeW, Y: closeTop, W: closeW, H: closeH,
	}
	x := sheetX + padL
	for range d.Actions {
		actions = append(actions, model.Rect{X: x, Y: btnTop, W: btnW, H: btnH})
		x += btnW + btnGap
	}
	return close, actions
}

// handleTap advances the interactive state for one tap and reports whether the
// panel needs redrawing.
//
// The rules, in the order they are tested:
//
//   - With a dialog open, only the dialog's own controls are live. A tap
//     anywhere else is swallowed, NOT passed through to the agenda beneath: a
//     modal that lets you operate the thing it covers is how you mute the wrong
//     event. The close control is the way out, per the design.
//   - With no dialog open, a tap on an event card opens its sheet.
//   - Anything else does nothing, and specifically does not redraw. A stray
//     touch on the weather chart should cost nothing.
func handleTap(st *dialogState, vm model.ViewModel, cfg *config.Config, store *Store, x, y float64) bool {
	if st.dlg != nil {
		closeRect, actionRects := actionRects(st.dlg)
		if closeRect.Contains(x, y) {
			st.dlg = nil
			return true
		}
		for i, r := range actionRects {
			if !r.Contains(x, y) {
				continue
			}
			switch st.dlg.Actions[i].ID {
			case model.ActionMute:
				applyMute(st, cfg, store)
				st.dlg = nil
				return true
			}
		}
		// Inside the sheet but not on a control, or outside it entirely:
		// absorbed.
		return false
	}

	// The top-left corner raises the device sheet. Tested before the card hit
	// test because it sits over the clock, which is not tappable -- but the
	// order matters if the agenda ever extends left, and this reads as the
	// intent either way.
	if x < model.CornerTapPx && y < model.CornerTapPx {
		if st.deviceInfo == nil {
			return false // no gatherer wired up (tests that do not need it)
		}
		d := model.DeviceDialog(st.deviceInfo())
		st.dlg = &d
		st.key = ""
		st.openedAt = time.Now()
		st.timeout = deviceDialogTimeout
		return true
	}

	// A capped stack's summary row stands for several events, so it opens a list
	// rather than one event's detail. Tested before EventAt, which reports a
	// miss there precisely so this can claim the tap.
	if evs, ok := vm.Agenda.OverflowAt(x, y); ok {
		d := model.ConflictDialog(evs, cfg.Units.Clock24h)
		st.dlg = &d
		// No key and no muted target: the mute action needs one event, and this
		// sheet does not offer it. Same as the device sheet.
		st.key = ""
		st.openedAt = time.Now()
		st.timeout = dialogTimeout
		return true
	}

	e, ok := vm.Agenda.EventAt(x, y)
	if !ok {
		return false
	}
	key := e.Key()
	d := model.EventDialog(e, cfg.Units.Clock24h, cfg.IsMuted(key))
	st.dlg = &d
	st.key = key
	st.muted = config.MutedEvent{
		Key: key, Title: e.Title, Start: e.Start, End: e.End, Muted: time.Now(),
	}
	st.openedAt = time.Now()
	st.timeout = dialogTimeout
	return true
}

// applyMute toggles the open event's muted state and persists it.
//
// A failed write is logged and the state left alone rather than applied in
// memory only: a mute that silently forgets itself on reboot is worse than one
// that visibly did not take, because the user has no reason to try again.
func applyMute(st *dialogState, cfg *config.Config, store *Store) {
	muted := cfg.IsMuted(st.key)
	m := st.muted
	err := store.Update(func(c *config.Config) error {
		// Replace the slice wholesale -- Store.Update shallow-copies the
		// config, so mutating the shared backing array would leak into the
		// snapshot readers are holding.
		next := append([]config.MutedEvent(nil), c.Muted...)
		c.Muted = next
		// Prune BEFORE muting, never after. Pruning afterwards can delete the
		// entry just written -- an event that has already ended is exactly the
		// kind a user mutes (the in-progress one, or a past card still in the
		// row), and the mute would vanish before the next render. Ordering it
		// this way makes that impossible regardless of the grace period.
		c.PruneMuted(time.Now())
		if muted {
			c.Unmute(m.Key)
		} else {
			c.Mute(m)
		}
		return nil
	})
	if err != nil {
		log.Printf("touch: saving mute: %v", err)
	}
}

// watchTaps reads the touch device and drives the dialog state, asking for a
// render whenever the panel's appearance changed.
//
// render is called on this goroutine, so it must be safe to call concurrently
// with the once-a-minute loop -- both go through renderSafely, which serializes
// on the framebuffer.
func watchTaps(ctx context.Context, dev string, st *dialogState, store *Store,
	viewModel func() model.ViewModel, render func()) {

	r := touch.NewReader(dev, 270, model.PanelWidth, model.PanelHeight, 480, 1920)
	taps, err := r.Taps(ctx)
	if err != nil {
		// No touch device is not fatal: the dashboard is a display first and
		// every other surface still works.
		log.Printf("touch: %v (taps disabled)", err)
		return
	}

	// The timeout is checked on a ticker rather than a timer per dialog: one
	// ticker is less state, and a second of imprecision on a 45s idle close is
	// not observable.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-taps:
			if !ok {
				return
			}
			if handleTap(st, viewModel(), store.Config(), store, t.X, t.Y) {
				render()
			}
		case <-tick.C:
			if st.dlg != nil && time.Since(st.openedAt) > st.dialogTimeout() {
				st.dlg = nil
				render()
			}
		}
	}
}

// dialogTimeout returns how long the open dialog may sit idle.
//
// Falls back to the standard timeout when unset, so a dialog opened by a path
// that forgets to set it still closes rather than sticking forever.
func (st *dialogState) dialogTimeout() time.Duration {
	if st.timeout > 0 {
		return st.timeout
	}
	return dialogTimeout
}
