package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

	go fetchLoop(s, interval, fn)

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

	go fetchLoop(s, interval, fn)

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
