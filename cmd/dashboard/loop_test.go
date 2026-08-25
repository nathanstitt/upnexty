package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestNextTickAlignsToMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 17, 0, time.UTC)
	if got, want := nextTick(now), 43*time.Second; got != want {
		t.Errorf("nextTick = %v, want %v", got, want)
	}
}

func TestNextTickAtExactBoundaryWaitsFullMinute(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 42, 0, 0, time.UTC)
	if got, want := nextTick(now), time.Minute; got != want {
		t.Errorf("nextTick = %v, want %v", got, want)
	}
}

func TestStoreKeepsLastGoodOnFailure(t *testing.T) {
	s := &Store{}
	evs := []calendar.Event{{Title: "kept"}}
	w := &weather.Weather{Current: weather.Conditions{TempF: 70}}
	s.SetEvents(evs, nil)
	s.SetWeather(w, nil)

	// A later failure must not clear what we already have.
	s.SetEvents(nil, []string{"ical: timeout"})
	s.SetWeather(nil, []string{"weather: timeout"})

	gotEvs, gotW, errs := s.Snapshot()
	if len(gotEvs) != 1 || gotEvs[0].Title != "kept" {
		t.Errorf("events = %v, want the last-good set", gotEvs)
	}
	if gotW == nil || gotW.Current.TempF != 70 {
		t.Error("weather = nil, want the last-good value")
	}
	if len(errs) != 2 {
		t.Errorf("errs = %v, want both failures reported", errs)
	}
}

func TestStoreReplacesOnSuccess(t *testing.T) {
	s := &Store{}
	s.SetEvents([]calendar.Event{{Title: "old"}}, nil)
	s.SetEvents([]calendar.Event{{Title: "new"}}, nil)
	evs, _, errs := s.Snapshot()
	if len(evs) != 1 || evs[0].Title != "new" {
		t.Errorf("events = %v, want the fresh set", evs)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none after success", errs)
	}
}

// TestFetchLoopRetryIsPerSourceIndependent guards against the cross-wiring
// bug where fetchLoop's retry decision consulted Store.Snapshot()'s combined
// errors (calendar + weather concatenated) instead of its own fetch's
// outcome. A persistently failing calendar feed must never shorten the
// weather loop's wait — that would pin an otherwise-healthy source to a 30s
// retry cadence forever (120 requests/hour instead of 4), and vice versa.
//
// The Store here carries a permanent calendar error the whole time (as if a
// sibling calendar fetchLoop were stuck retrying), while the fetchLoop under
// test always succeeds. If the retry decision looked at combined Store
// errors, it would wait retryDelay every time instead of interval.
func TestFetchLoopRetryIsPerSourceIndependent(t *testing.T) {
	s := &Store{}
	s.SetEvents(nil, []string{"ical: persistently broken"})

	const interval = 40 * time.Millisecond
	oldRetry := retryDelay
	retryDelay = 5 * time.Millisecond
	defer func() { retryDelay = oldRetry }()

	calls := make(chan time.Time, 3)
	fn := fetchFunc(func(ctx context.Context, cfg *config.Config, store *Store) bool {
		calls <- time.Now()
		return true // this source always succeeds
	})

	go fetchLoop(&config.Config{}, s, interval, fn)

	var times []time.Time
	for i := 0; i < 3; i++ {
		select {
		case tm := <-calls:
			times = append(times, tm)
		case <-time.After(2 * time.Second):
			t.Fatal("fetchLoop did not invoke fn in time")
		}
	}

	// Gaps between successful calls must track `interval`, not `retryDelay` —
	// proof the combined Store errors (from the unrelated calendar failure)
	// did not leak into this loop's own retry decision.
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap < interval/2 {
			t.Errorf("call %d..%d gap = %v, want ~%v (interval); a failing sibling source must not shorten this loop's wait", i-1, i, gap, interval)
		}
	}
}

// TestFetchLoopRetriesSoonerOnlyOnItsOwnFailure is the companion case: a
// fetch loop's OWN failure still must shorten its own wait to retryDelay.
func TestFetchLoopRetriesSoonerOnlyOnItsOwnFailure(t *testing.T) {
	const interval = 500 * time.Millisecond
	oldRetry := retryDelay
	retryDelay = 5 * time.Millisecond
	defer func() { retryDelay = oldRetry }()

	s := &Store{}
	calls := make(chan time.Time, 2)
	fail := true
	fn := fetchFunc(func(ctx context.Context, cfg *config.Config, store *Store) bool {
		calls <- time.Now()
		ok := !fail
		fail = false // only the first call fails
		return ok
	})

	go fetchLoop(&config.Config{}, s, interval, fn)

	var times []time.Time
	for i := 0; i < 2; i++ {
		select {
		case tm := <-calls:
			times = append(times, tm)
		case <-time.After(2 * time.Second):
			t.Fatal("fetchLoop did not invoke fn in time")
		}
	}

	gap := times[1].Sub(times[0])
	if gap >= interval {
		t.Errorf("gap after own failure = %v, want well under interval (%v) — should retry at ~%v", gap, interval, retryDelay)
	}
}

// TestRecoverRenderConvertsPanicToError exercises the real recover wrapper
// used by renderSafely: a panicking render (template execution error, nil
// deref from unusual fetch data, etc.) must come back as an error the caller
// can log, not a process-ending panic.
func TestRecoverRenderConvertsPanicToError(t *testing.T) {
	err := recoverRender(func() error {
		panic(errors.New("boom"))
	})
	if err == nil {
		t.Fatal("expected an error from a recovered panic, got nil")
	}
}

// TestRecoverRenderPassesThroughSuccess confirms recoverRender is a no-op
// wrapper on the non-panic path.
func TestRecoverRenderPassesThroughSuccess(t *testing.T) {
	if err := recoverRender(func() error { return nil }); err != nil {
		t.Errorf("recoverRender() = %v, want nil", err)
	}
	wantErr := errors.New("normal error")
	if err := recoverRender(func() error { return wantErr }); err != wantErr {
		t.Errorf("recoverRender() = %v, want %v", err, wantErr)
	}
}
