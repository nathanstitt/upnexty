// Command dashboard renders the UpNext dashboard to the Luckfox framebuffer.
//
// Display-only: there is no HTTP server and no touch handling. A tick loop
// wakes each minute, rebuilds the view model, and re-renders only when the
// generated HTML differs from the last frame.
package main

import (
	"context"
	"flag"
	"log"
	"os"
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
	retryDelay   = 30 * time.Second
	fetchTimeout = 60 * time.Second
)

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
		if err := renderOnce(cfg, store, *fbDev, fbW, fbH, *htmlOut, nil); err != nil {
			log.Fatalf("render: %v", err)
		}
		return
	}

	go fetchLoop(cfg, store, time.Duration(cfg.Refresh.CalendarMinutes)*time.Minute, fetchCalendars)
	go fetchLoop(cfg, store, time.Duration(cfg.Refresh.WeatherMinutes)*time.Minute, fetchWeather)

	var last string
	for {
		time.Sleep(nextTick(time.Now()))
		if err := renderOnce(cfg, store, *fbDev, fbW, fbH, *htmlOut, &last); err != nil {
			log.Printf("render: %v", err)
		}
	}
}

// renderOnce builds the model, renders HTML, and blits it. When last is
// non-nil it is used to skip the raster when the HTML has not changed.
func renderOnce(cfg *config.Config, store *Store, dev string, fbW, fbH int, htmlOut string, last *string) error {
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
	if last != nil && html == *last {
		return nil // nothing changed; skip the ~1.5s render
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
	// fbW/fbH/rotate/pageW/pageH are all fixed for the process lifetime.
	if err := fb.Write(dev, fb.Pack(img, fbW, fbH, rotate)); err != nil {
		return err
	}
	if last != nil {
		*last = html
	}
	return nil
}

type fetchFunc func(context.Context, *config.Config, *Store)

// fetchLoop runs one fetcher forever, retrying sooner after a failure so a boot
// with no DNS recovers in seconds rather than a full interval.
func fetchLoop(cfg *config.Config, store *Store, interval time.Duration, fn fetchFunc) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		fn(ctx, cfg, store)
		cancel()

		wait := interval
		if _, _, errs := store.Snapshot(); len(errs) > 0 {
			wait = retryDelay
		}
		time.Sleep(wait)
	}
}

func fetchAll(ctx context.Context, cfg *config.Config, store *Store) {
	fetchCalendars(ctx, cfg, store)
	fetchWeather(ctx, cfg, store)
}

func fetchCalendars(ctx context.Context, cfg *config.Config, store *Store) {
	var all []calendar.Event
	var errs []string
	now := time.Now()
	for _, src := range cfg.Calendars {
		if src.URL == "" || len(src.URL) > 6 && src.URL[:6] == "PASTE_" {
			continue
		}
		evs, err := calendar.Fetch(ctx, src, now, cfg.Agenda.DaysAhead)
		if err != nil {
			errs = append(errs, "ical("+src.Name+"): "+err.Error())
			continue
		}
		all = append(all, evs...)
	}
	if cfg.Agenda.MaxEvents > 0 && len(all) > cfg.Agenda.MaxEvents {
		all = all[:cfg.Agenda.MaxEvents]
	}
	store.SetEvents(all, errs)
}

func fetchWeather(ctx context.Context, cfg *config.Config, store *Store) {
	w, err := weather.Fetch(ctx, cfg)
	if err != nil {
		store.SetWeather(nil, []string{"weather: " + err.Error()})
		return
	}
	store.SetWeather(w, nil)
}
