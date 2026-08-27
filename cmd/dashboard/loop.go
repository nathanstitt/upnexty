package main

import (
	"sync"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

// Store holds the most recent successful fetch of each source. A failed fetch
// records an error but never discards last-good data — a transient outage must
// not blank the panel. It is written by two fetch goroutines (calendar and
// weather, each on their own interval) and read by the render loop, so every
// field is guarded by mu.
type Store struct {
	mu      sync.RWMutex
	cfg     *config.Config
	events  []calendar.Event
	weather *weather.Weather
	evErrs  []string
	wxErrs  []string
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

// SetEvents records the result of a calendar fetch attempt. On failure (errs
// non-nil) the previous events are kept; evs is expected to be nil in that
// case, but the last-good set is preserved either way.
func (s *Store) SetEvents(evs []calendar.Event, errs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evErrs = errs
	if len(errs) == 0 {
		s.events = evs
	}
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

// nextTick returns the delay until the next minute boundary, so the clock
// changes on the minute rather than drifting.
func nextTick(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
}
