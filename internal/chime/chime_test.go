package chime

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanstitt/upnexty/internal/calendar"
)

func TestWAVIsAWellFormedMonoFile(t *testing.T) {
	t.Parallel()
	b := WAV()
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[36:40]) != "data" {
		t.Fatalf("not a RIFF/WAVE file: %q %q %q", b[0:4], b[8:12], b[36:40])
	}
	data := binary.LittleEndian.Uint32(b[40:])
	if int(data) != len(b)-44 {
		t.Errorf("data chunk says %d bytes, file has %d after the header", data, len(b)-44)
	}
	if riff := binary.LittleEndian.Uint32(b[4:]); int(riff) != len(b)-8 {
		t.Errorf("RIFF size %d, want %d", riff, len(b)-8)
	}
	if ch := binary.LittleEndian.Uint16(b[22:]); ch != 1 {
		t.Errorf("channels = %d, want mono", ch)
	}
	if rate := binary.LittleEndian.Uint32(b[24:]); rate != SampleRate {
		t.Errorf("rate = %d, want %d", rate, SampleRate)
	}
	secs := float64(data) / 2 / SampleRate
	if secs < 1.3 || secs > 1.5 {
		t.Errorf("chime lasts %.2fs, want about %.1fs", secs, duration)
	}

	// Never clips, never ends on a click, and is not silent.
	var peakSeen int16
	for i := 44; i+1 < len(b); i += 2 {
		v := int16(binary.LittleEndian.Uint16(b[i:]))
		if v < 0 {
			v = -v
		}
		if v > peakSeen {
			peakSeen = v
		}
	}
	ceiling := int16(math.Floor(peak*32767)) + 1
	if peakSeen > ceiling {
		t.Errorf("peak sample %d exceeds the %d ceiling", peakSeen, ceiling)
	}
	if peakSeen < 1000 {
		t.Errorf("peak sample %d: the file is nearly silent", peakSeen)
	}
	if last := int16(binary.LittleEndian.Uint16(b[len(b)-2:])); last > 50 || last < -50 {
		t.Errorf("last sample %d: the fade-out did not reach silence", last)
	}
}

func ev(title string, start time.Time, allDay bool) calendar.Event {
	return calendar.Event{UID: title, Title: title, Start: start, End: start.Add(30 * time.Minute), AllDay: allDay}
}

func titles(evs []calendar.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Title)
	}
	return out
}

func TestDueRingsEachStartOnce(t *testing.T) {
	t.Parallel()
	boot := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	n := NewNotifier(boot)
	standup := ev("standup", boot.Add(15*time.Minute), false)

	// The tick at the top of the minute, a few seconds late behind a frame.
	got := n.Due([]calendar.Event{standup}, standup.Start.Add(4*time.Second))
	if len(got) != 1 || got[0].Title != "standup" {
		t.Fatalf("at the start minute Due = %v, want [standup]", titles(got))
	}
	// A wake later in the same minute, and the next tick: nothing new.
	for _, later := range []time.Duration{30 * time.Second, 64 * time.Second} {
		if got := n.Due([]calendar.Event{standup}, standup.Start.Add(later)); len(got) != 0 {
			t.Errorf("%s after start Due = %v, want nothing (already rung)", later, titles(got))
		}
	}
}

func TestDueSkipsWhatShouldNotRing(t *testing.T) {
	t.Parallel()
	boot := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	n := NewNotifier(boot)
	now := boot.Add(20 * time.Minute)
	evs := []calendar.Event{
		ev("all-day", now.Add(-10*time.Second), true),       // no start to announce
		ev("future", now.Add(time.Minute), false),           // not yet
		ev("stale", now.Add(-Grace-time.Second), false),     // start missed long ago
		ev("pre-boot", boot.Add(-time.Minute), false),       // under way when we started
		ev("just-now", now.Add(-20*time.Second), false),     // due
		ev("late-tick", now.Add(-Grace+time.Second), false), // inside the grace window
	}
	got := titles(n.Due(evs, now))
	want := []string{"just-now", "late-tick"}
	if len(got) != len(want) {
		t.Fatalf("Due = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Due[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDueForgetsOldStarts(t *testing.T) {
	t.Parallel()
	boot := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	n := NewNotifier(boot)
	e := ev("one", boot.Add(time.Minute), false)
	n.Due([]calendar.Event{e}, e.Start.Add(time.Second))
	if len(n.seen) != 1 {
		t.Fatalf("seen has %d entries after one start, want 1", len(n.seen))
	}
	n.Due(nil, e.Start.Add(3*Grace))
	if len(n.seen) != 0 {
		t.Errorf("seen still holds %d entries long after the start; the set must not grow forever", len(n.seen))
	}
}

func TestPlayerRunsAplayOnceTheFileExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	probe := filepath.Join(dir, "card0")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	p := &Player{
		Path:      filepath.Join(dir, "chime.wav"),
		CardProbe: probe,
		Run: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	for i := 0; i < 2; i++ {
		if err := p.Play(context.Background()); err != nil {
			t.Fatalf("Play %d: %v", i, err)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("aplay ran %d times, want 2", len(calls))
	}
	want := []string{"aplay", "-q", "-D", "default", p.Path}
	for i, w := range want {
		if calls[0][i] != w {
			t.Errorf("aplay argv[%d] = %q, want %q", i, calls[0][i], w)
		}
	}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[:4]) != "RIFF" {
		t.Error("the file handed to aplay is not the WAV")
	}
}

func TestPlayerIsSilentWithoutACard(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ran := false
	p := &Player{
		Path:      filepath.Join(dir, "chime.wav"),
		CardProbe: filepath.Join(dir, "no-such-card"),
		Run: func(context.Context, string, ...string) error {
			ran = true
			return nil
		},
	}
	err := p.Play(context.Background())
	if !errors.Is(err, ErrNoCard) {
		t.Errorf("Play without a card = %v, want ErrNoCard", err)
	}
	if ran {
		t.Error("aplay ran with no card present")
	}
	if _, err := os.Stat(p.Path); err == nil {
		t.Error("the WAV was written although nothing can play it")
	}
}
