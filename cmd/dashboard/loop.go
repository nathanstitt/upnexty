package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/nathanstitt/upnexty/internal/calendar"
	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/weather"
)

// Store holds the most recent successful fetch of each source. A failed fetch
// records an error but never discards last-good data — a transient outage must
// not blank the panel. "Each source" is per calendar feed, not per fetch round:
// see feedEvents. It is written by two fetch goroutines (calendar and weather,
// each on their own interval) and read by the render loop, so every field is
// guarded by mu.
type Store struct {
	mu      sync.RWMutex
	cfg     *config.Config
	events  []calendar.Event
	weather *weather.Weather
	evErrs  []string
	wxErrs  []string

	// feedEvents is the last-good event set for each calendar feed, keyed by
	// feedResult.Key (the feed's URL — see that type). It exists so last-good
	// data is kept PER FEED rather than globally: SetEvents used to drop the
	// whole merged fetch whenever any feed errored, so one broken calendar
	// froze every working one at whatever the last fully-clean fetch showed.
	// That was latent while every feed was iCal over HTTP and failures were
	// brief, and stops being latent with a backend that can fail for days at a
	// time (an expired OAuth token), which would pin the entire agenda.
	//
	// Entries for feeds no longer in the config are pruned on each fetch
	// rather than left to expire: a feed deleted in the portal must stop
	// contributing events immediately, and nothing else would ever collect
	// them — this map would otherwise accumulate every URL the board has ever
	// been pointed at, for the life of the process.
	feedEvents map[string][]calendar.Event

	// evFetched records that a calendar fetch has completed, successfully or
	// not. It distinguishes "no events yet" from "no events", which are
	// identical in the events slice but must not read the same on the panel.
	evFetched bool

	// configPath is where Update persists a saved config. Set once at
	// construction (see main); Update fails clearly if it is ever empty
	// instead of silently skipping the disk write.
	configPath string

	// calWake carries a nudge to the calendar fetch loop, so a feed saved in
	// the portal is read now rather than up to CalendarMinutes later.
	//
	// Buffered with room for one and sent non-blocking: the signal means
	// "refetch soon", so two saves in quick succession collapsing into one
	// wake-up is correct rather than a lost update. A nil channel (a Store
	// built by a test that does not run the loop) makes RefetchCalendars a
	// no-op instead of a panic.
	calWake chan struct{}

	// renderWake asks the render loop to draw now rather than at the next
	// minute boundary. Same shape and rationale as calWake: buffered for one,
	// sent non-blocking, nil-safe.
	renderWake chan struct{}
}

// NewStore constructs a Store ready for Update, which persists to configPath.
// cfg is installed directly (equivalent to a subsequent SetConfig) so callers
// don't need a separate call before the first render tick.
func NewStore(cfg *config.Config, configPath string) *Store {
	s := &Store{
		configPath: configPath,
		calWake:    make(chan struct{}, 1),
		renderWake: make(chan struct{}, 1),
	}
	s.SetConfig(cfg)
	return s
}

// Config returns the current configuration. The pointer is replaced rather
// than mutated on save, so the returned value is a stable snapshot: a reader
// keeps seeing its own version even if the portal swaps in a new one mid-tick.
func (s *Store) Config() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetConfig installs a new configuration. Called by the portal after a
// successful save; the next tick renders with it.
func (s *Store) SetConfig(c *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
}

// Update performs an atomic read-modify-write-save of the configuration: it
// holds the write lock across copying the current config, running fn against
// the copy, persisting the result to disk, and installing it in the Store.
//
// This exists because Config()+SetConfig() called separately (the portal's
// old save path) is a transaction split across two unsynchronized steps: two
// concurrent HTTP handlers can each read the same starting config, mutate
// their own section, and save/install one after the other -- the second
// write wins in full, silently discarding the first save both in memory and
// on flash, even though both requests observed a 303 success. go test -race
// cannot see this: each goroutine's Store access is individually
// lock-correct, and the lost update is a race at the transaction level, not
// a data race.
//
// The disk write happens while the lock is held, which blocks any concurrent
// Config() reader for the duration of that write. That's accepted here
// rather than saving outside the lock: config.json is a few hundred bytes on
// NAND, portal saves are user-initiated and rare (nothing like the once-a-
// minute render tick), and correctness -- no lost update -- matters more
// here than shaving a millisecond off a reader that would otherwise be
// blocked by the mutex anyway.
func (s *Store) Update(fn func(*config.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.configPath == "" {
		return fmt.Errorf("store: Update called with no configPath set (see NewStore)")
	}

	next := *s.cfg // shallow copy is enough; slices are replaced wholesale by fn
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.Save(s.configPath); err != nil {
		return err
	}
	s.cfg = &next
	return nil
}

// RefetchCalendars marks the calendar data stale and wakes the fetch loop.
//
// Called when the portal saves new feeds. It does both halves deliberately:
// clearing evFetched puts the panel back into "Fetching..." immediately, so
// the next render stops showing events from a feed the user has just replaced,
// and the wake makes the new ones arrive in seconds rather than at the next
// interval. Without the first, the panel would sit on stale events looking
// like the save did nothing; without the second, it would sit on "Fetching..."
// for up to ten minutes, which looks the same.
func (s *Store) RefetchCalendars() {
	s.mu.Lock()
	s.evFetched = false
	s.mu.Unlock()

	select {
	case s.calWake <- struct{}{}:
	default: // already pending, or no loop listening
	}

	// Draw now, so the panel switches to "Fetching..." as the user saves
	// rather than at the next minute boundary.
	s.RequestRender()
}

// RequestRender asks the render loop to draw before its next tick.
//
// The panel otherwise redraws on the minute, which is right for a clock and
// wrong for anything the user just did: a save would take up to a minute to
// show "Fetching...", and the arriving events another minute after that. Both
// would read as the board ignoring the change.
func (s *Store) RequestRender() {
	select {
	case s.renderWake <- struct{}{}:
	default: // a render is already pending
	}
}

// WaitRenderWake blocks until a render is requested or d elapses.
func (s *Store) WaitRenderWake(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.renderWake:
	case <-t.C:
	}
}

// WaitCalendarWake blocks until a refetch is requested or d elapses, reporting
// whether it was woken. The fetch loop uses this in place of a plain sleep.
func (s *Store) WaitCalendarWake(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.calWake:
		return true
	case <-t.C:
		return false
	}
}

// feedResult is one calendar feed's outcome from a single fetch round.
type feedResult struct {
	// Key identifies the feed across fetches. The URL is what we key on: it is
	// the only field of config.CalendarSource that actually selects the data,
	// so a feed renamed or recoloured in the portal keeps its cache instead of
	// being treated as a brand-new feed and blanking until its next successful
	// fetch. Name looks like the friendlier key and is exactly wrong for that
	// reason — it is free text the user edits.
	//
	// Two sources sharing a URL therefore share one cache entry. Doing that
	// deliberately: they fetch identical data, so a per-source cache would
	// hold two copies and a failure of one but not the other would merge the
	// same events in twice. Collapsing them means duplicate sources behave in
	// the cached path exactly as they do in the live path, where the later
	// SetEvents assignment for a key simply wins.
	Key string
	// Events is what this feed returned, already parsed but not yet merged
	// with the other feeds. Ignored when OK is false.
	Events []calendar.Event
	// OK distinguishes "this feed returned nothing" from "this feed failed".
	// They are the same empty slice and must not be: the first replaces the
	// cache (a genuinely empty calendar), the second preserves it.
	OK bool
}

// SetEvents records the result of a calendar fetch round, one entry per feed
// that was attempted, and rebuilds the merged event set from it.
//
// Feeds that succeeded replace their cached events; feeds that failed keep
// theirs, so an outage on one calendar no longer freezes the others. errs
// still carries every failure, whether or not cached events covered for it —
// model.Build turns a non-empty errs into ViewModel.Stale, and a panel served
// partly from cache is exactly what that indicator is for.
//
// merge is applied to the concatenation of all live feeds' events while the
// lock is held. The caller owns it because the merge policy (sort by start,
// TrimPast, MaxEvents) is about the agenda rather than about storage, and it
// has to run over the merged set: cached events must be sorted and trimmed
// alongside fresh ones, or a feed being served from cache would look different
// on the panel — out of order, or past events still on the row — than the same
// feed succeeding. A nil merge stores the concatenation as-is.
//
// Feeds absent from results are dropped from the cache entirely; see
// Store.feedEvents. That makes results the authoritative list of live feeds,
// so the caller must pass an entry for every configured feed it attempted,
// including the failures — omitting a failed feed would silently delete its
// cache, which is the bug this method exists to fix.
//
// A failed attempt still sets evFetched: the panel has now tried, and an empty
// agenda is the honest answer. Gating on success instead would leave a board
// with an unreachable feed showing "Fetching..." forever.
func (s *Store) SetEvents(results []feedResult, errs []string, merge func([]calendar.Event) []calendar.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evErrs = errs
	s.evFetched = true

	next := make(map[string][]calendar.Event, len(results))
	var all []calendar.Event
	for _, r := range results {
		evs := r.Events
		if !r.OK {
			// The failing feed contributes whatever it last returned. A feed
			// that has never succeeded has no entry, so the lookup yields nil
			// and it contributes nothing — the first fetch of a broken feed
			// must show an empty row, not invent events for it.
			evs = s.feedEvents[r.Key]
		}
		next[r.Key] = evs
		all = append(all, evs...)
	}
	s.feedEvents = next

	if merge != nil {
		all = merge(all)
	}
	s.events = all
}

// SetWeather records the result of a weather fetch attempt. On failure w is
// nil and the previous weather is kept.
func (s *Store) SetWeather(w *weather.Weather, errs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wxErrs = errs
	if w != nil {
		s.weather = w
	}
}

// Snapshot returns the current data plus any errors from the latest attempts.
// The returned events slice is a fresh copy made under the lock, so the
// caller can read it after unlocking without racing a future SetEvents call —
// callers here never mutate a previously returned slice in place, but copying
// keeps that true even if a caller does someday.
func (s *Store) Snapshot() ([]calendar.Event, *weather.Weather, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	evs := append([]calendar.Event(nil), s.events...)
	errs := append(append([]string(nil), s.evErrs...), s.wxErrs...)
	return evs, s.weather, errs
}

// CalendarPending reports that no calendar fetch has finished yet, so an empty
// agenda should read as "still loading" rather than "nothing scheduled".
func (s *Store) CalendarPending() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.evFetched
}

// nextTick returns the delay until the next minute boundary, so the clock
// changes on the minute rather than drifting.
func nextTick(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
}
