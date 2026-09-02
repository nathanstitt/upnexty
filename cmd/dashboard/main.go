// Command dashboard renders the UpNext dashboard to the Luckfox framebuffer.
//
// A tick loop wakes each minute and rebuilds/re-renders the view. The clock
// advances every minute by construction, so the generated HTML always differs
// from the previous frame — there is no "skip when unchanged" fast path.
//
// Two other things drive a render: the configuration portal on :80 (a
// goroutine in this process), and taps on the panel, which open the detail
// sheet and can hide an event from the display. Both go through renderSafely
// under renderMu so only one write to /dev/fb0 is ever in flight.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nathanstitt/omnidoc/pkg/omnidoc"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/fb"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/portal"
	"github.com/nathanstitt/luckfox-dashboard/internal/quote"
	"github.com/nathanstitt/luckfox-dashboard/internal/view"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
)

const (
	pageW, pageH = 1920, 480
	rotate       = 270
	fetchTimeout = 60 * time.Second

	// brightnessPath is the panel's sysfs backlight control.
	brightnessPath = "/sys/class/backlight/waveshare_bl/brightness"
)

// retryDelay is how soon a fetch loop retries after ITS OWN fetch fails,
// rather than waiting the full interval. A var (not const) so tests can
// shrink it instead of waiting on real 30s timers.
var retryDelay = 30 * time.Second

func main() {
	once := flag.Bool("once", false, "render a single frame and exit")
	fbDev := flag.String("fb", "/dev/fb0", "framebuffer device")
	cfgPath := flag.String("config", "/root/config.json", "path to config.json")
	htmlOut := flag.String("html-out", "", "also write the generated HTML here (debugging)")
	// :80 so the captive-portal sheet lands directly on the settings page. A
	// phone's connectivity probe is an HTTP GET on port 80; serving the portal
	// anywhere else means the probe hits a closed port (no sheet at all) or, if
	// something answers 80 and redirects, the sheet follows a hop that iOS
	// renders as a bare error. Running as root, so binding a privileged port is
	// not a problem here.
	portalAddr := flag.String("portal", ":80", "address for the configuration portal")
	touchDev := flag.String("touch", "/dev/input/event0", "touchscreen input device")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// fb.Pack panics on a geometry mismatch (a guard added after Task 10's
	// review) rather than silently cropping or misrendering. That's the right
	// behavior for a one-shot call, but a panic in the middle of an infinite
	// service loop would kill the process on every subsequent tick too — so
	// validate expected geometry once, up front, and fail fast with a clear
	// message instead of letting Pack panic deep inside the loop. rotate 270
	// requires fbW==pageH and fbH==pageW (see fb.Pack's doc comment).
	fbW, fbH, err := fb.Geometry(*fbDev)
	if err != nil {
		log.Printf("framebuffer geometry: %v (falling back to %dx%d)", err, pageH, pageW)
		fbW, fbH = pageH, pageW
	}
	if fbW != pageH || fbH != pageW {
		log.Fatalf("framebuffer geometry %dx%d does not match expected %dx%d for rotate %d; refusing to start (fb.Pack would panic on every frame)",
			fbW, fbH, pageH, pageW, rotate)
	}

	store := NewStore(cfg, *cfgPath)

	// Created before the *once branch so both the single-frame path and the
	// service loop can derive the setup hint from the same client. The
	// tracker is likewise shared and long-lived (not reconstructed per tick)
	// -- its whole purpose is remembering consecutive misses across ticks.
	wc := &wifi.Client{R: wifi.ExecRunner{}}
	hintTracker := &setupHintTracker{}

	if *once {
		fetchAll(context.Background(), cfg, store)
		if err := renderOnce(store.Config(), store, wc, hintTracker, nil, *fbDev, fbW, fbH, *htmlOut); err != nil {
			log.Fatalf("render: %v", err)
		}
		return
	}

	// The panel keeps whatever brightness it had; apply the configured value so
	// a reboot honours it -- including an explicit 0, which the settings page
	// documents as "turns the panel off" and which must survive a reboot just
	// like any other value. Applying it again on later config changes is the
	// portal's job (see internal/portal/handlers.go's applyBrightness), not
	// this startup path's.
	if err := applyStartupBrightness(brightnessPath, cfg.BrightnessValue()); err != nil {
		log.Printf("brightness: %v", err)
	}

	ps := &portal.Server{
		Store: store,
		WiFi:  wc,
		MAC:   wc.MAC(),
	}
	go func() {
		// The portal is a goroutine in this process, not a second binary, so a
		// save can swap the config pointer directly. A failure here must not
		// stop the dashboard: a panel that renders without a config UI is far
		// better than no panel.
		if err := http.ListenAndServe(*portalAddr, ps.Handler()); err != nil {
			log.Printf("portal: %v", err)
		}
	}()

	// render is the one entry point both the tick loop and the touch handler
	// use, so framebuffer writes never overlap.
	render := func() {
		renderMu.Lock()
		defer renderMu.Unlock()
		if err := renderSafely(store.Config(), store, wc, hintTracker, ps, *fbDev, fbW, fbH, *htmlOut); err != nil {
			log.Printf("render: %v", err)
		}
	}

	// Paint once before fetching so the panel shows "Fetching..." rather than
	// staying on the previous boot's frame (or black) for the duration. The
	// store is empty here, so CalendarPending is true and the agenda renders
	// its loading state instead of claiming there is nothing scheduled.
	render()

	// Then fetch synchronously, so the first data frame is real. The service
	// used to start both loops as goroutines and enter the tick loop straight
	// away, which meant the first minute or two rendered an empty store and the
	// panel asserted "No more events today" before it had asked anything. A
	// frame costs ~10s on this hardware and the fetch a few seconds more; that
	// is a bounded, one-time delay in exchange for never publishing a claim the
	// data does not support.
	fetchAll(context.Background(), store.Config(), store)
	render()

	// fetchLoop fetches at the top of each iteration, so it would immediately
	// repeat the fetch just done. Sleep one interval first to skip that
	// duplicate; every later iteration is unchanged. fetchLoop itself keeps its
	// fetch-first shape, which is what lets a failed boot retry in retryDelay
	// rather than a full interval.
	cfg0 := store.Config()
	go func() {
		// The startup offset is itself interruptible. A plain sleep here left
		// a window -- ten minutes by default -- in which a portal save cleared
		// the pending flag and sent a wake that nothing was listening for, so
		// the buffered signal was dropped and the panel sat on "Fetching..."
		// until this delay expired. Waiting on the same channel the loop uses
		// means a save during startup is honoured rather than lost.
		store.WaitCalendarWake(calendarInterval(cfg0))
		// The calendar loop is the wakeable one: saving a feed in the portal
		// should show it now, not at the next interval.
		fetchLoopWake(store, calendarInterval, fetchCalendars,
			func(s *Store, d time.Duration) { s.WaitCalendarWake(d) })
	}()
	go delayThen(weatherInterval(cfg0), func() {
		fetchLoop(store, weatherInterval, fetchWeather)
	})

	// Taps open the event sheet. A missing or unreadable input device is
	// logged and the dashboard runs on as a display -- see watchTaps.
	st := &dialogState{deviceInfo: func() model.DeviceInfo {
		// Gathered at tap time, not cached: the address is a DHCP lease and
		// the whole point of the sheet is reporting the current one.
		cfg := store.Config()
		info := model.DeviceInfo{
			Hostname:         hostname(),
			Password:         portal.DefaultPassword(wc.MAC()),
			PasswordIsCustom: cfg.Portal.PasswordHash != "",
		}
		if status, err := wc.Status(); err == nil {
			info.IP, info.SSID = status.IP, status.SSID
		} else {
			log.Printf("device sheet: wifi status: %v", err)
		}
		return info
	}}
	go watchTaps(context.Background(), *touchDev, st, store,
		func() model.ViewModel {
			// Only the agenda geometry is needed for hit-testing, so the
			// setup hint and error list are deliberately omitted -- they do
			// not move a card.
			evs, wx, _ := store.Snapshot()
			return model.Build(time.Now(), store.Config(), evs, wx, nil, nil)
		},
		func() {
			setOpenDialog(st.dlg)
			render()
		})

	for {
		// Woken early by a save or an arriving fetch; otherwise this is the
		// once-a-minute clock tick.
		store.WaitRenderWake(nextTick(time.Now()))
		render()
	}
}

// applyStartupBrightness writes v to the panel's sysfs backlight control once
// at process start. path is a parameter (rather than the brightnessPath
// constant used directly) so a test can point it at a temp file instead of
// real hardware.
//
// 0 is a valid, deliberate value -- the settings page documents it as "turns
// the panel off" (config.Config.Display.Brightness is a *int specifically so
// nil-vs-0 is distinguishable) -- so it must be written like any other value,
// not skipped. Only a value outside the sysfs range (0-255) is invalid; those
// are clamped rather than skipped or rejected, matching config.Config's own
// normalization of a stored out-of-range value (see internal/config/config.go),
// so startup and the portal's save path agree on what an out-of-range value
// means instead of one silently no-op'ing while the other clamps.
func applyStartupBrightness(path string, v int) error {
	if v < 0 {
		v = 0
	} else if v > 255 {
		v = 255
	}
	return os.WriteFile(path, []byte(strconv.Itoa(v)), 0o644)
}

// renderSafely wraps renderOnce with a panic recovery so that one bad frame —
// a template execution error, a nil deref surfaced by unusual fetch data,
// anything not otherwise anticipated — logs and lets the loop continue to the
// next minute rather than killing an unattended kiosk process permanently.
// The known fb.Pack geometry-mismatch panic is instead guarded against by
// failing fast at startup (see main): that is a misconfiguration, not a
// transient bad frame, so refusing to start is the right response for it.
// This recover is for everything else.
func renderSafely(cfg *config.Config, store *Store, wc *wifi.Client, hintTracker *setupHintTracker, ps *portal.Server, dev string, fbW, fbH int, htmlOut string) error {
	return recoverRender(func() error {
		return renderOnce(cfg, store, wc, hintTracker, ps, dev, fbW, fbH, htmlOut)
	})
}

// renderMu serializes framebuffer writes. Two things trigger a render -- the
// minute loop and a tap -- and they run on different goroutines. Without this
// a tap landing mid-tick would interleave two Write calls into /dev/fb0 and
// tear the frame.
var renderMu sync.Mutex

// openDialog is the modal sheet the next render should draw, or nil.
//
// It has its own mutex rather than sharing renderMu. Guarding it with renderMu
// deadlocked the process on the very first tap: the touch callback published
// the dialog and then called render, taking a non-reentrant sync.Mutex twice,
// which wedged the tick loop along with it. The two locks protect different
// things -- this one a pointer, renderMu the framebuffer -- and conflating them
// is what created a lock ordering at all.
var (
	dialogMu   sync.Mutex
	openDialog *model.Dialog
)

// setOpenDialog publishes the dialog for subsequent renders. It must not be
// called while holding renderMu.
func setOpenDialog(d *model.Dialog) {
	dialogMu.Lock()
	defer dialogMu.Unlock()
	openDialog = d
}

// currentDialog returns the dialog to draw.
func currentDialog() *model.Dialog {
	dialogMu.Lock()
	defer dialogMu.Unlock()
	return openDialog
}

// recoverRender runs fn, converting any panic into an error instead of
// letting it propagate and kill the process. Factored out from renderSafely
// so the recover behavior itself can be exercised with a controllable
// panicking stub in tests, without needing a real omnidoc render to
// panic on demand.
func recoverRender(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during render: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}

// setupHintMissThreshold is how many consecutive non-connected Status() polls
// are required before the panel shows the setup hint. Status() reports
// transient non-COMPLETED states (e.g. SCANNING) during wpa_supplicant's
// ordinary re-scans on a board that is, and remains, connected -- showing the
// hint on the first such poll would make it blink on and off every minute on
// a healthy board, which is its own defect. Requiring 2 consecutive misses
// (i.e. confirmed on the poll after the first miss) rides out a one-tick
// blip while still surfacing the wrong-password/AP-fallback case within two
// render ticks (two minutes) of it happening -- fast enough to matter for a
// user standing in front of the panel. This does not apply to Status()
// *errors*: those show the hint immediately (see setupHintTracker.Update).
const setupHintMissThreshold = 2

// setupHintTracker decides whether to show the setup hint from live
// association state rather than saved WiFi credentials. It must be gated on
// wifi.Client.Status()'s Connected field (wpa_state == COMPLETED), not on
// cfg.WiFi.SSID: the portal sets WiFi.SSID the moment credentials are saved
// (see internal/portal/handlers.go's handleSaveWiFi), before anything tries
// to associate -- gating on that field hides the hint the instant a wrong
// password is typed in, exactly when the board falls back to broadcasting
// its setup AP and the hint is the only thing telling the user its name and
// password.
//
// Holds a consecutive-miss counter (see setupHintMissThreshold) rather than
// consulting the clock, so the hysteresis decision is a pure function of the
// poll sequence and is testable without time.Now() or sleeps.
type setupHintTracker struct {
	misses int
}

// Update runs one Status() poll through the tracker and returns the hint to
// show this tick, or nil. wc and mac are separated (rather than deriving mac
// from wc again here) because the caller already has wc.MAC() from building
// the Status call in some paths; passing it in also keeps this method free of
// its own wifi.Client method calls beyond Status, which simplifies testing.
func (t *setupHintTracker) Update(status wifi.Status, statusErr error, mac string) *model.SetupHint {
	switch {
	case statusErr != nil:
		// An error (wpa_cli missing, supplicant not running) is not evidence
		// the board is fine -- it is evidence we cannot tell. Show the hint
		// immediately, bypassing the miss counter entirely: an error is not
		// the "ordinary re-scan on a working board" case the hysteresis
		// exists to smooth over, and erring toward showing is required
		// regardless of how many consecutive polls have failed.
		t.misses = 0
		return hint(mac)
	case status.Connected:
		t.misses = 0
		return nil
	default:
		t.misses++
		if t.misses < setupHintMissThreshold {
			return nil
		}
		return hint(mac)
	}
}

// hint builds the panel's setup hint from the board's WiFi MAC. Computing
// both strings here, via the wifi and portal packages, rather than passing
// wc/cfg through to the template keeps the template independent of whether
// those packages are reachable, and keeps the panel and the portal's own
// login page unable to disagree about what the password is -- both derive it
// from the same portal.DefaultPassword call.
func hint(mac string) *model.SetupHint {
	return &model.SetupHint{
		APName:   wifi.APName(mac),
		Password: portal.DefaultPassword(mac),
		URL:      portal.PortalURL,
	}
}

// renderOnce builds the model, renders HTML, and blits it to the framebuffer.
func renderOnce(cfg *config.Config, store *Store, wc *wifi.Client, hintTracker *setupHintTracker, ps *portal.Server, dev string, fbW, fbH int, htmlOut string) error {
	evs, wx, errs := store.Snapshot()

	// One wpa_cli subprocess per render tick (once a minute; see nextTick),
	// not tighter -- renderOnce is only ever called from the once-per-minute
	// service loop or the single -once invocation.
	status, statusErr := wc.Status()
	setup := hintTracker.Update(status, statusErr, wc.MAC())

	// An association attempt in flight overrides the hysteresis entirely. The
	// tracker's job is deciding whether an unassociated board should nag; this
	// is a different question -- the user just submitted credentials and the
	// browser that submitted them is about to lose its connection, so the panel
	// has to report progress even on the very first tick, and even in the
	// window where status still says "connected" to the old network.
	if ps != nil {
		if ssid := ps.Connecting(); ssid != "" {
			if setup == nil {
				setup = hint(wc.MAC())
			}
			setup.Connecting = ssid
		}
	}

	vm := model.Build(time.Now(), cfg, evs, wx, errs, setup)
	// The dialog is interactive state, not derived data, so it is attached
	// after Build rather than passed into it.
	vm.Dialog = currentDialog()
	// Likewise Loading: whether a fetch has happened is a fact about the
	// process, not about the calendar, so Build cannot derive it.
	vm.Loading = store.CalendarPending()

	// The end-of-day panel carries a quote. Attach whatever is cached and
	// kick off a refresh in the background when the day has just ended: the
	// fetch must never sit in the render path, because a slow or hung request
	// would stall a frame on a panel whose whole job is showing the time.
	//
	// Fetched on entering the state rather than on a timer -- the quote is
	// only visible for the few hours after the last event, so a periodic
	// refresh would spend most of its requests on a screen nobody is reading.
	if vm.NowBlock.Mode == model.ModeDone {
		if q := quotes.Last(); !q.Empty() {
			vm.NowBlock.Quote, vm.NowBlock.QuoteAuthor = q.Text, q.Author
		}
		refreshQuote()
	} else {
		// Out of the done state: let the next entry into it fetch a new line.
		resetQuoteFetch()
	}

	html, err := view.Render(vm)
	if err != nil {
		return err
	}
	if htmlOut != "" {
		if err := os.WriteFile(htmlOut, []byte(html), 0o644); err != nil {
			log.Printf("html-out: %v", err)
		}
	}

	// omnidoc discards the context on the HTML render path (see
	// docs/omnidoc-gaps.md §7), so this ctx cannot actually cancel a hung
	// render today. It is still passed through so the call becomes correctly
	// cancellable for free once that's fixed upstream; a goroutine-based
	// timeout wrapper to fake cancellation was deliberately ruled out.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The font loader serves the embedded @font-face files; without it the
	// panel silently falls back to DejaVu (see view.FontLoader).
	img, err := fb.RenderHTML(ctx, []byte(html), pageW, pageH,
		omnidoc.WithResourceLoader(view.FontLoader()))
	if err != nil {
		return err
	}

	// fb.Pack's dimension check was validated once against fbW/fbH at startup
	// (see main), so this call should never hit its panic path in practice —
	// fbW/fbH/rotate/pageW/pageH are all fixed for the process lifetime. If it
	// somehow does, renderSafely's recover keeps the loop alive regardless.
	if err := fb.Write(dev, fb.Pack(img, fbW, fbH, rotate)); err != nil {
		return err
	}
	return nil
}

// fetchFunc runs one fetch attempt and reports whether it succeeded.
// fetchLoop uses that return value — not the Store's combined error state —
// to decide its own retry cadence, because Store.Snapshot concatenates errors
// from BOTH sources. A persistently failing calendar feed must not pin the
// independent weather loop to the 30s retry cadence forever (or vice versa) —
// each loop's retry decision must depend only on its own fetch's outcome.
type fetchFunc func(context.Context, *config.Config, *Store) (ok bool)

// calendarInterval and weatherInterval are the interval selectors passed to
// fetchLoop for each source. Named (rather than inline closures) so tests can
// assert directly that they derive from Refresh.CalendarMinutes /
// Refresh.WeatherMinutes on whatever config fetchLoop hands them, instead of
// a value captured once at startup.
func calendarInterval(c *config.Config) time.Duration {
	return time.Duration(c.Refresh.CalendarMinutes) * time.Minute
}

func weatherInterval(c *config.Config) time.Duration {
	return time.Duration(c.Refresh.WeatherMinutes) * time.Minute
}

// delayThen sleeps then runs fn. Used to offset the periodic fetch loops past
// the synchronous startup fetch so the first interval is not spent repeating
// work already done.
func delayThen(d time.Duration, fn func()) {
	time.Sleep(d)
	fn()
}

// fetchLoop runs one fetcher forever, retrying sooner after a failure so a boot
// with no DNS recovers in seconds rather than a full interval. The retry
// decision is based solely on fn's own return value, never on shared Store
// state that another loop also writes to.
//
// cfg is read from the store fresh at the top of each iteration rather than
// captured once, so a config change the portal saves mid-run (e.g. new
// calendar URLs, timezone, or MaxEvents) takes effect on the next cycle
// instead of being frozen at startup. interval is a selector rather than a
// plain time.Duration for the same reason: it is invoked against that same
// fresh cfg each iteration, so a refresh interval changed in the portal also
// takes effect on the next cycle instead of requiring a restart.
func fetchLoop(store *Store, interval func(*config.Config) time.Duration, fn fetchFunc) {
	fetchLoopWake(store, interval, fn, func(s *Store, d time.Duration) { time.Sleep(d) })
}

// fetchLoopWake is fetchLoop with its sleep injected, so the calendar loop can
// be woken by a portal save while the weather loop keeps a plain sleep.
func fetchLoopWake(store *Store, interval func(*config.Config) time.Duration, fn fetchFunc,
	wake func(*Store, time.Duration)) {
	for {
		cfg := store.Config()
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		ok := fn(ctx, cfg, store)
		cancel()

		wait := interval(cfg)
		if !ok {
			wait = retryDelay
		}
		// Interruptible: a feed saved in the portal wakes this immediately
		// rather than waiting out the interval. wake is ignored beyond ending
		// the sleep -- the next iteration re-reads the config either way.
		wake(store, wait)
	}
}

func fetchAll(ctx context.Context, cfg *config.Config, store *Store) {
	fetchCalendars(ctx, cfg, store)
	fetchWeather(ctx, cfg, store)
}

func fetchCalendars(ctx context.Context, cfg *config.Config, store *Store) bool {
	var all []calendar.Event
	var errs []string
	now := time.Now()
	for _, src := range cfg.Calendars {
		if src.URL == "" || len(src.URL) > 6 && src.URL[:6] == "PASTE_" {
			continue
		}
		evs, err := calendar.Fetch(ctx, src, now, cfg.Agenda.DaysAhead, cfg.TimeLocation())
		if err != nil {
			errs = append(errs, "ical("+src.Name+"): "+err.Error())
			continue
		}
		all = append(all, evs...)
	}
	// Each feed arrives individually sorted, but concatenating sorted slices
	// does not produce a sorted slice — sort the merge before truncating so a
	// MaxEvents cutoff drops the chronologically latest events across ALL
	// feeds, not just whichever feed happened to be appended last.
	sort.SliceStable(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	// Trim past events across the merged set, not per feed. Parse cannot do it:
	// it sees one calendar at a time, so with several feeds each would keep its
	// own last finished event and the row would show one past card per
	// calendar, with a bare gap chip between each pair.
	all = calendar.TrimPast(all, now, calendar.KeepPast)
	if cfg.Agenda.MaxEvents > 0 && len(all) > cfg.Agenda.MaxEvents {
		all = all[:cfg.Agenda.MaxEvents]
	}
	store.SetEvents(all, errs)
	// Draw as soon as the events land. Without this a refetch triggered by a
	// save would finish in a second and sit unseen until the next minute
	// boundary, so the panel would show "Fetching..." for most of a minute
	// after the data had already arrived.
	store.RequestRender()
	return len(errs) == 0
}

func fetchWeather(ctx context.Context, cfg *config.Config, store *Store) bool {
	w, err := weather.Fetch(ctx, cfg)
	if err != nil {
		store.SetWeather(nil, []string{"weather: " + err.Error()})
		return false
	}
	store.SetWeather(w, nil)
	return true
}

// The end-of-day quote, and the guard that fetches it once per entry into that
// state rather than on every tick.
//
// renderOnce runs once a minute, so an unguarded fetch would hit zenquotes 60
// times an hour for a line that only changes when the day ends. quoteFetching
// latches on the first render of the done state and clears when the day rolls
// over into events again, which is the only point a new quote is wanted.
var (
	quotes        = &quote.Client{}
	quoteMu       sync.Mutex
	quoteFetching bool
)

// refreshQuote fetches in the background, at most once per entry into ModeDone.
//
// The fetch is detached from the render because it is remote: a hung request
// must not hold up a frame. The result lands in the client's cache and is
// picked up by the next tick, a minute later -- which is soon enough for a
// panel that has just told the reader their day is over.
func refreshQuote() {
	quoteMu.Lock()
	if quoteFetching {
		quoteMu.Unlock()
		return
	}
	quoteFetching = true
	quoteMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := quotes.Get(ctx); err != nil {
			// Not fatal: the panel renders the block without a quote, or with
			// the last good one. Worth a line in the log to explain a blank.
			log.Printf("quote: %v", err)
		}
	}()
}

// resetQuoteFetch re-arms refreshQuote, so the next end of day fetches a new
// line rather than reusing the one from the previous day.
func resetQuoteFetch() {
	quoteMu.Lock()
	quoteFetching = false
	quoteMu.Unlock()
}

// hostname returns the board's name for the device sheet, or "" if it cannot
// be read. Not fatal: the sheet simply omits the row.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
