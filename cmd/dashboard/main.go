// Command dashboard renders the UpNext dashboard to the Luckfox framebuffer.
//
// Display-only: there is no HTTP server and no touch handling. A tick loop
// wakes each minute and rebuilds/re-renders the view. The clock advances every
// minute by construction, so the generated HTML always differs from the
// previous frame — there is no "skip when unchanged" fast path.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"sort"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/fb"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/view"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

const (
	pageW, pageH = 1920, 480
	rotate       = 270
	fetchTimeout = 60 * time.Second
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

	store := &Store{}
	if *once {
		fetchAll(context.Background(), cfg, store)
		if err := renderOnce(cfg, store, *fbDev, fbW, fbH, *htmlOut); err != nil {
			log.Fatalf("render: %v", err)
		}
		return
	}

	go fetchLoop(cfg, store, time.Duration(cfg.Refresh.CalendarMinutes)*time.Minute, fetchCalendars)
	go fetchLoop(cfg, store, time.Duration(cfg.Refresh.WeatherMinutes)*time.Minute, fetchWeather)

	for {
		time.Sleep(nextTick(time.Now()))
		if err := renderSafely(cfg, store, *fbDev, fbW, fbH, *htmlOut); err != nil {
			log.Printf("render: %v", err)
		}
	}
}

// renderSafely wraps renderOnce with a panic recovery so that one bad frame —
// a template execution error, a nil deref surfaced by unusual fetch data,
// anything not otherwise anticipated — logs and lets the loop continue to the
// next minute rather than killing an unattended kiosk process permanently.
// The known fb.Pack geometry-mismatch panic is instead guarded against by
// failing fast at startup (see main): that is a misconfiguration, not a
// transient bad frame, so refusing to start is the right response for it.
// This recover is for everything else.
func renderSafely(cfg *config.Config, store *Store, dev string, fbW, fbH int, htmlOut string) error {
	return recoverRender(func() error {
		return renderOnce(cfg, store, dev, fbW, fbH, htmlOut)
	})
}

// recoverRender runs fn, converting any panic into an error instead of
// letting it propagate and kill the process. Factored out from renderSafely
// so the recover behavior itself can be exercised with a controllable
// panicking stub in tests, without needing a real doctaculous render to
// panic on demand.
func recoverRender(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during render: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}

// renderOnce builds the model, renders HTML, and blits it to the framebuffer.
func renderOnce(cfg *config.Config, store *Store, dev string, fbW, fbH int, htmlOut string) error {
	evs, wx, errs := store.Snapshot()
	vm := model.Build(time.Now(), cfg, evs, wx, errs)

	html, err := view.Render(vm)
	if err != nil {
		return err
	}
	if htmlOut != "" {
		if err := os.WriteFile(htmlOut, []byte(html), 0o644); err != nil {
			log.Printf("html-out: %v", err)
		}
	}

	// doctaculous discards the context on the HTML render path (see
	// docs/doctaculous-gaps.md §7), so this ctx cannot actually cancel a hung
	// render today. It is still passed through so the call becomes correctly
	// cancellable for free once that's fixed upstream; a goroutine-based
	// timeout wrapper to fake cancellation was deliberately ruled out.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	img, err := fb.RenderHTML(ctx, []byte(html), pageW, pageH)
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

// fetchLoop runs one fetcher forever, retrying sooner after a failure so a boot
// with no DNS recovers in seconds rather than a full interval. The retry
// decision is based solely on fn's own return value, never on shared Store
// state that another loop also writes to.
func fetchLoop(cfg *config.Config, store *Store, interval time.Duration, fn fetchFunc) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		ok := fn(ctx, cfg, store)
		cancel()

		wait := interval
		if !ok {
			wait = retryDelay
		}
		time.Sleep(wait)
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
	if cfg.Agenda.MaxEvents > 0 && len(all) > cfg.Agenda.MaxEvents {
		all = all[:cfg.Agenda.MaxEvents]
	}
	store.SetEvents(all, errs)
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
