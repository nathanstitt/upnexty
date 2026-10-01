// Package chime plays a short sound when a meeting starts.
//
// Three pieces, kept apart so each can be tested without a speaker: WAV
// synthesizes the sound, Notifier decides which event starts are due one,
// and Player hands the file to aplay. Nothing here touches ALSA directly --
// the image ships aplay, the USB speaker is an ordinary ALSA card once the
// snd-usb-audio modules are installed (CLAUDE.md, "USB audio"), and a child
// process is the simplest thing that cannot corrupt the renderer's heap.
package chime

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/nathanstitt/upnexty/internal/calendar"
)

// The sound: a soft major arpeggio, C5 E5 G5, each note a bell-like tone
// (fundamental plus two quiet harmonics) with a fast attack and a long
// exponential decay, the notes overlapping so the chord rings out together.
// 22.05kHz mono 16-bit keeps the file around 60KB, which is nothing on tmpfs.
const (
	SampleRate = 22050
	duration   = 1.4  // seconds
	noteGap    = 0.18 // seconds between note onsets
	decay      = 0.45 // seconds for the amplitude to fall to 1/e
	attack     = 0.006
	peak       = 0.55 // of full scale; the mixer sets the loudness, not this
)

var notes = []float64{523.25, 659.25, 783.99}

// WAV returns the chime as a complete RIFF/WAVE file.
func WAV() []byte {
	n := int(duration * SampleRate)
	samples := make([]float64, n)
	for i := range samples {
		t := float64(i) / SampleRate
		var v float64
		for k, f := range notes {
			on := float64(k) * noteGap
			if t < on {
				continue
			}
			dt := t - on
			env := math.Exp(-dt / decay)
			if dt < attack {
				env *= dt / attack
			}
			w := 2 * math.Pi * f * dt
			v += env * (math.Sin(w) + 0.30*math.Sin(2*w) + 0.12*math.Sin(3*w))
		}
		samples[i] = v
	}
	// Normalise to the chosen peak so the harmonics cannot push it into
	// clipping, then fade the last 50ms so the file does not end on a click.
	var max float64
	for _, v := range samples {
		max = math.Max(max, math.Abs(v))
	}
	scale := peak * 32767 / max
	fade := SampleRate / 20 // 50ms
	pcm := make([]byte, 2*n)
	for i, v := range samples {
		if left := n - i; left < fade {
			v *= float64(left) / float64(fade)
		}
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(v*scale)))
	}

	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1) // mono
	binary.LittleEndian.PutUint32(hdr[24:], SampleRate)
	binary.LittleEndian.PutUint32(hdr[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(pcm)))
	return append(hdr, pcm...)
}

// Grace is how far past its start an event may still be chimed. The render
// tick fires at the top of the minute but can arrive late behind a ten-second
// frame, and a wake from an arriving fetch can land anywhere in the minute;
// this window catches both without reaching back to events that started
// while nobody was listening.
const Grace = 90 * time.Second

// Notifier decides which event starts are due a chime, once each.
type Notifier struct {
	since time.Time
	seen  map[string]time.Time
}

// NewNotifier starts listening at now. Events already under way at that
// moment are not chimed: a board rebooting mid-meeting has nothing to
// announce.
func NewNotifier(now time.Time) *Notifier {
	return &Notifier{since: now, seen: map[string]time.Time{}}
}

// Due returns the events whose start falls in (now-Grace, now] that have not
// been returned before, and records them. All-day events have no start to
// announce and are skipped. Callers pass events already filtered for muting:
// an event hidden from the panel should not ring either.
func (n *Notifier) Due(events []calendar.Event, now time.Time) []calendar.Event {
	var due []calendar.Event
	for _, e := range events {
		if e.AllDay || e.Start.After(now) || !e.Start.After(now.Add(-Grace)) || !e.Start.After(n.since) {
			continue
		}
		k := e.Key()
		if _, ok := n.seen[k]; ok {
			continue
		}
		n.seen[k] = now
		due = append(due, e)
	}
	// The set only ever needs the last Grace window; drop older entries so a
	// board that runs for months does not keep every start it ever saw.
	for k, t := range n.seen {
		if now.Sub(t) > 2*Grace {
			delete(n.seen, k)
		}
	}
	return due
}

// ErrNoCard is returned by Play when no sound card is present, so a board
// without a speaker skips the chime silently rather than logging a failed
// aplay every meeting.
var ErrNoCard = errors.New("chime: no sound card")

// Player plays the chime through aplay.
type Player struct {
	// Path is where the WAV is written on first use. Default
	// /tmp/upnext-chime.wav: tmpfs, so nothing lands on NAND.
	Path string
	// Device is the ALSA device name; default "default".
	Device string
	// CardProbe is a path whose existence means a card is present; default
	// /proc/asound/card0.
	CardProbe string
	// Run executes the player command; default runs it. Tests inject one.
	Run func(ctx context.Context, name string, args ...string) error

	mu      sync.Mutex
	written bool
}

// Play writes the chime file if needed and plays it, serialising concurrent
// calls so two starts in one minute ring twice rather than over each other.
func (p *Player) Play(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	probe := p.CardProbe
	if probe == "" {
		probe = "/proc/asound/card0"
	}
	if _, err := os.Stat(probe); err != nil {
		return ErrNoCard
	}
	path := p.Path
	if path == "" {
		path = "/tmp/upnext-chime.wav"
	}
	if !p.written {
		if err := os.WriteFile(path, WAV(), 0o644); err != nil {
			return fmt.Errorf("chime: write %s: %w", path, err)
		}
		p.written = true
	}
	dev := p.Device
	if dev == "" {
		dev = "default"
	}
	run := p.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := run(ctx, "aplay", "-q", "-D", dev, path); err != nil {
		return fmt.Errorf("chime: aplay: %w", err)
	}
	return nil
}
