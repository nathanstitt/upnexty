package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

func TestStoreConfigSwap(t *testing.T) {
	s := &Store{}
	first := &config.Config{}
	first.Location.Timezone = "UTC"
	s.SetConfig(first)

	if got := s.Config(); got.Location.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", got.Location.Timezone)
	}

	second := &config.Config{}
	second.Location.Timezone = "America/Chicago"
	s.SetConfig(second)

	if got := s.Config(); got.Location.Timezone != "America/Chicago" {
		t.Errorf("Timezone = %q, want the swapped value", got.Location.Timezone)
	}
}

func TestStoreConfigSnapshotIsStable(t *testing.T) {
	// A reader that grabbed the pointer must keep seeing its own snapshot even
	// if the portal swaps in a new config mid-tick.
	s := &Store{}
	first := &config.Config{}
	first.Location.Timezone = "UTC"
	s.SetConfig(first)

	held := s.Config()
	swapped := &config.Config{}
	swapped.Location.Timezone = "America/Chicago"
	s.SetConfig(swapped)

	if held.Location.Timezone != "UTC" {
		t.Error("a held snapshot changed underneath the reader")
	}
}

// TestConcurrentUpdatesToDifferentSectionsBothSurvive is the regression test
// for the lost-update bug: two concurrent Update calls, each mutating a
// different section of the config (WiFi SSID and display brightness), must
// both land -- neither may silently overwrite the other's change, in memory
// or on disk.
//
// This does NOT reduce to a data race: each goroutine mutates its own copy,
// and every individual Store field access is correctly locked, so
// `go test -race` is silent on the old, buggy Config()+SetConfig() sequence.
// The bug is at the transaction level (read snapshot -> mutate -> write back
// as three unsynchronized steps), which only shows up as a *lost update* --
// so this test proves correctness by asserting the outcome (both writes
// present after every iteration), not by asking the race detector.
//
// Looped many times with two goroutines released via a barrier (not a sleep)
// so each iteration is a genuine interleaving race rather than one lucky
// (or unlucky) ordering -- the finding's own reproduction needed 40 runs to
// fail every time under the old code, so this test uses the same count.
func TestConcurrentUpdatesToDifferentSectionsBothSurvive(t *testing.T) {
	const iterations = 40

	for i := 0; i < iterations; i++ {
		cfg := &config.Config{}
		b := 200
		cfg.Display.Brightness = &b
		cfg.WiFi.SSID = "OldNetwork"

		store := NewStore(cfg, filepath.Join(t.TempDir(), "config.json"))

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			_ = store.Update(func(c *config.Config) error {
				c.WiFi.SSID = "NewNetwork"
				return nil
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			v := 42
			_ = store.Update(func(c *config.Config) error {
				c.Display.Brightness = &v
				return nil
			})
		}()

		close(start) // release both goroutines at once to force contention
		wg.Wait()

		got := store.Config()
		if got.WiFi.SSID != "NewNetwork" {
			t.Fatalf("iteration %d: WiFi.SSID = %q, want %q (lost update)", i, got.WiFi.SSID, "NewNetwork")
		}
		if got.BrightnessValue() != 42 {
			t.Fatalf("iteration %d: BrightnessValue() = %d, want 42 (lost update)", i, got.BrightnessValue())
		}
	}
}

func TestStoreConfigRace(t *testing.T) {
	// Run with -race: concurrent readers and a writer must not race.
	s := &Store{}
	s.SetConfig(&config.Config{})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c := &config.Config{}
			c.Agenda.MaxEvents = i
			s.SetConfig(c)
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = s.Config().Agenda.MaxEvents
	}
	<-done
}

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

// A fresh store has not fetched, so an empty agenda means "not known yet" and
// the panel must say so rather than claiming the day is clear.
func TestStoreCalendarPendingUntilFirstFetch(t *testing.T) {
	s := &Store{}
	if !s.CalendarPending() {
		t.Error("CalendarPending = false on a fresh store, want true")
	}
	s.SetEvents([]calendar.Event{{Title: "e"}}, nil)
	if s.CalendarPending() {
		t.Error("CalendarPending = true after a successful fetch, want false")
	}
}

// A failed fetch still counts as having asked. Gating on success would strand a
// board with an unreachable feed on "Fetching..." forever.
func TestStoreCalendarPendingClearsOnFailedFetch(t *testing.T) {
	s := &Store{}
	s.SetEvents(nil, []string{"ical: timeout"})
	if s.CalendarPending() {
		t.Error("CalendarPending = true after a failed fetch, want false")
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
	s.SetConfig(&config.Config{})
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

	go fetchLoop(s, func(*config.Config) time.Duration { return interval }, fn)

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
	s.SetConfig(&config.Config{})
	calls := make(chan time.Time, 2)
	fail := true
	fn := fetchFunc(func(ctx context.Context, cfg *config.Config, store *Store) bool {
		calls <- time.Now()
		ok := !fail
		fail = false // only the first call fails
		return ok
	})

	go fetchLoop(s, func(*config.Config) time.Duration { return interval }, fn)

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

// TestIntervalSelectorsFollowConfig guards against reverting fetchLoop's
// interval selector to a plain time.Duration captured once at startup. The
// production selectors (calendarInterval, weatherInterval) must derive their
// result from whatever config they're handed, not a closed-over value — so
// two different configs must yield two different durations.
func TestIntervalSelectorsFollowConfig(t *testing.T) {
	c5 := &config.Config{}
	c5.Refresh.CalendarMinutes = 5
	c5.Refresh.WeatherMinutes = 5

	c20 := &config.Config{}
	c20.Refresh.CalendarMinutes = 20
	c20.Refresh.WeatherMinutes = 20

	if got, want := calendarInterval(c5), 5*time.Minute; got != want {
		t.Errorf("calendarInterval(5) = %v, want %v", got, want)
	}
	if got, want := calendarInterval(c20), 20*time.Minute; got != want {
		t.Errorf("calendarInterval(20) = %v, want %v", got, want)
	}
	if got, want := weatherInterval(c5), 5*time.Minute; got != want {
		t.Errorf("weatherInterval(5) = %v, want %v", got, want)
	}
	if got, want := weatherInterval(c20), 20*time.Minute; got != want {
		t.Errorf("weatherInterval(20) = %v, want %v", got, want)
	}
}

// TestFetchLoopCadenceFollowsConfigSwap is the end-to-end proof: a config
// swapped into the Store mid-run (as the portal would do after a save) must
// change fetchLoop's actual sleep cadence, not just the cfg value handed to
// fn. Each iteration reads cfg once and uses it for both the call and that
// iteration's sleep, so a swap made while iteration N is asleep cannot affect
// iteration N's already-computed wait — it takes effect starting with
// iteration N+1's read. The test swaps in a short-interval config right after
// iteration 1 fires (while iteration 1 is still asleep for the long
// interval), then asserts the gap between iteration 2 and iteration 3 — both
// entirely after the swap — is short. Under the old frozen-interval bug that
// gap would still be `long` regardless of the swap.
func TestFetchLoopCadenceFollowsConfigSwap(t *testing.T) {
	s := &Store{}
	longCfg := &config.Config{}
	s.SetConfig(longCfg)

	const long = 300 * time.Millisecond
	const short = 10 * time.Millisecond
	selector := func(c *config.Config) time.Duration {
		if c == longCfg {
			return long
		}
		return short
	}

	calls := make(chan time.Time, 3)
	fn := fetchFunc(func(ctx context.Context, cfg *config.Config, store *Store) bool {
		calls <- time.Now()
		return true
	})

	go fetchLoop(s, selector, fn)

	select {
	case <-calls: // iteration 1, using longCfg
	case <-time.After(1 * time.Second):
		t.Fatal("fetchLoop did not invoke fn for its first tick in time")
	}

	// Swap in the short-interval config right away; fetchLoop is asleep for
	// `long` at this point (that wait was already computed from longCfg), so
	// this lands well before iteration 2's config read, mimicking a portal
	// save landing mid-cycle.
	shortCfg := &config.Config{}
	s.SetConfig(shortCfg)

	var second time.Time
	select {
	case second = <-calls: // iteration 2, using shortCfg for its own wait
	case <-time.After(1 * time.Second):
		t.Fatal("fetchLoop did not reach a second tick in time")
	}

	select {
	case third := <-calls: // iteration 3, entirely after the swap
		if gap := third.Sub(second); gap >= long {
			t.Errorf("gap between iterations 2 and 3 = %v, want well under the old interval (%v) — cadence is still frozen at startup", gap, long)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("fetchLoop did not pick up the shortened interval after the config swap")
	}
}

// TestFetchCalendarsSortsAcrossFeedsBeforeTruncating guards against the bug
// where fetchCalendars concatenated each feed's (individually sorted) events
// and then truncated to MaxEvents — a concatenation of sorted slices is NOT
// sorted, so truncating it can keep an entire early feed's late events while
// discarding a later feed's earlier, more imminent ones. Feed A here
// contributes only LATE events; feed B contributes only EARLY events. With
// MaxEvents smaller than the combined total, the retained set must be the
// chronologically earliest events across BOTH feeds — i.e. it must include
// feed B's events, not just a prefix of feed A's.
func TestFetchCalendarsSortsAcrossFeedsBeforeTruncating(t *testing.T) {
	icsFeed := func(events ...[2]string) string {
		var sb strings.Builder
		sb.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n")
		for _, e := range events {
			uid, dtstart := e[0], e[1]
			sb.WriteString("BEGIN:VEVENT\r\n")
			sb.WriteString("UID:" + uid + "\r\n")
			sb.WriteString("DTSTART:" + dtstart + "\r\n")
			sb.WriteString("DTEND:" + dtstart + "\r\n")
			sb.WriteString("SUMMARY:" + uid + "\r\n")
			sb.WriteString("END:VEVENT\r\n")
		}
		sb.WriteString("END:VCALENDAR\r\n")
		return sb.String()
	}

	// Anchored to the real clock (fetchCalendars calls time.Now() internally,
	// with no seam to inject a fixed one) but offset well into the future so
	// the test is not sensitive to what wall-clock time it happens to run at.
	base := time.Now().Add(48 * time.Hour).UTC()
	day := func(n int) string { return base.AddDate(0, 0, n).Format("20060102T150405Z") }

	// Feed A: three LATE events (appended to `all` first).
	feedA := icsFeed(
		[2]string{"A-late-1", day(10)},
		[2]string{"A-late-2", day(11)},
		[2]string{"A-late-3", day(12)},
	)
	// Feed B: three EARLY events (appended second, but chronologically first).
	feedB := icsFeed(
		[2]string{"B-early-1", day(0)},
		[2]string{"B-early-2", day(1)},
		[2]string{"B-early-3", day(2)},
	)

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(feedA))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(feedB))
	}))
	defer srvB.Close()

	cfg := &config.Config{
		Calendars: []config.CalendarSource{
			{Name: "A", URL: srvA.URL},
			{Name: "B", URL: srvB.URL},
		},
	}
	cfg.Agenda.DaysAhead = 20
	cfg.Agenda.MaxEvents = 4

	store := &Store{}
	ok := fetchCalendars(context.Background(), cfg, store)
	if !ok {
		t.Fatal("fetchCalendars returned false, want true (both feeds should succeed)")
	}

	evs, _, _ := store.Snapshot()
	if len(evs) != 4 {
		t.Fatalf("got %d events, want 4 (MaxEvents)", len(evs))
	}

	wantTitles := map[string]bool{
		"B-early-1": true, "B-early-2": true, "B-early-3": true, "A-late-1": true,
	}
	for _, e := range evs {
		if !wantTitles[e.Title] {
			t.Errorf("retained event %q, want only the 4 chronologically earliest across both feeds (%v)", e.Title, wantTitles)
		}
	}
	// Must include events from feed B (the second-appended feed) — proof the
	// merge was sorted before truncation rather than truncated per-append-order.
	var sawFeedB bool
	for _, e := range evs {
		if strings.HasPrefix(e.Title, "B-") {
			sawFeedB = true
		}
	}
	if !sawFeedB {
		t.Error("no feed B events retained; truncation kept only feed A's prefix instead of sorting the merge first")
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

// Saving new feeds puts the panel back into "Fetching..." and wakes the loop.
//
// Both halves matter. Without the pending reset the panel keeps showing events
// from the feed that was just replaced, which reads as the save having failed;
// without the wake it shows "Fetching..." for up to CalendarMinutes, which
// reads the same way.
func TestRefetchCalendarsResetsPendingAndWakes(t *testing.T) {
	s := NewStore(&config.Config{}, t.TempDir()+"/config.json")
	s.SetEvents(nil, nil)
	if s.CalendarPending() {
		t.Fatal("CalendarPending = true after a fetch, want false")
	}

	s.RefetchCalendars()

	if !s.CalendarPending() {
		t.Error("CalendarPending = false after RefetchCalendars; the panel would " +
			"keep showing the old feed's events")
	}
	if !s.WaitCalendarWake(2 * time.Second) {
		t.Error("the fetch loop was not woken; the new feed would not be read " +
			"until the next interval")
	}
}

// The wait returns on its own when nothing asks for a refetch, so the loop
// still runs on its interval.
func TestWaitCalendarWakeTimesOut(t *testing.T) {
	s := NewStore(&config.Config{}, t.TempDir()+"/config.json")
	start := time.Now()
	if s.WaitCalendarWake(50 * time.Millisecond) {
		t.Error("WaitCalendarWake reported a wake with nothing pending")
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("returned after %v, well before the %v asked for", d, 50*time.Millisecond)
	}
}

// Two saves in quick succession collapse into one wake-up rather than queueing.
//
// The signal means "refetch soon", so coalescing is correct: the loop re-reads
// the config when it wakes, and a second pass would fetch the same feeds twice.
func TestRefetchCalendarsCoalesces(t *testing.T) {
	s := NewStore(&config.Config{}, t.TempDir()+"/config.json")
	s.RefetchCalendars()
	s.RefetchCalendars()
	s.RefetchCalendars()

	if !s.WaitCalendarWake(time.Second) {
		t.Fatal("no wake delivered")
	}
	if s.WaitCalendarWake(50 * time.Millisecond) {
		t.Error("a second wake was queued; three saves should collapse into one fetch")
	}
}

// A store with no wake channel must not panic. Tests construct these.
func TestRefetchCalendarsWithoutAChannelIsSafe(t *testing.T) {
	s := &Store{configPath: t.TempDir() + "/config.json"}
	s.SetConfig(&config.Config{})
	s.SetEvents(nil, nil)
	s.RefetchCalendars() // must not panic or block
	if !s.CalendarPending() {
		t.Error("CalendarPending = false; the reset should happen regardless of the channel")
	}
}

// A save asks for a redraw, so the panel shows "Fetching..." at once rather
// than at the next minute boundary.
func TestRefetchCalendarsRequestsARender(t *testing.T) {
	s := NewStore(&config.Config{}, t.TempDir()+"/config.json")
	s.RefetchCalendars()

	done := make(chan struct{})
	go func() { s.WaitRenderWake(2 * time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("no render requested; the panel would keep the old frame for up to a minute")
	}
}

// The render wait returns on its own, so the clock still ticks when nothing
// has happened.
func TestWaitRenderWakeTimesOut(t *testing.T) {
	s := NewStore(&config.Config{}, t.TempDir()+"/config.json")
	start := time.Now()
	s.WaitRenderWake(50 * time.Millisecond)
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("returned after %v, well before the %v asked for", d, 50*time.Millisecond)
	}
}
