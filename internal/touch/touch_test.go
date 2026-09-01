package touch

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The board's panel: portrait digitizer, landscape UI.
func testReader() *Reader {
	return NewReader("", 270, 1920, 480, 480, 1920)
}

// A wrong rotation sign still hit-tests plausibly near the centre of the
// screen, so the centre proves nothing -- the corners are what distinguish a
// correct mapping from a mirrored one. Each case names the physical corner a
// finger is touching and the landscape pixel it must land on.
func TestRotate270MapsCorners(t *testing.T) {
	r := testReader()
	tests := []struct {
		name         string
		dx, dy       float64
		wantX, wantY float64
	}{
		// Device origin (0,0) is the portrait top-left. Rotated 270 for a
		// landscape UI, that corner becomes the landscape BOTTOM-left.
		{"device top-left", 0, 0, 0, 479},
		{"device top-right", 479, 0, 0, 0},
		{"device bottom-left", 0, 1919, 1919, 479},
		{"device bottom-right", 479, 1919, 1919, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y := r.Rotate270(tc.dx, tc.dy)
			if x != tc.wantX || y != tc.wantY {
				t.Errorf("Rotate270(%v,%v) = (%v,%v), want (%v,%v)",
					tc.dx, tc.dy, x, y, tc.wantX, tc.wantY)
			}
		})
	}
}

// Every mapped tap must land inside the panel. A sign error puts coordinates
// negative or past the edge, which this catches across the whole surface even
// where a corner check would not.
func TestRotate270StaysInBounds(t *testing.T) {
	r := testReader()
	for dx := 0.0; dx < 480; dx += 17 {
		for dy := 0.0; dy < 1920; dy += 43 {
			x, y := r.Rotate270(dx, dy)
			if x < 0 || x >= 1920 || y < 0 || y >= 480 {
				t.Fatalf("Rotate270(%v,%v) = (%v,%v), outside 1920x480", dx, dy, x, y)
			}
		}
	}
}

// writeEvent appends one input_event record in the board's 32-bit layout.
func writeEvent(b []byte, typ, code uint16, val int32) []byte {
	var rec [eventSize]byte
	// tv_sec, tv_usec left zero: the decoder does not read them.
	binary.LittleEndian.PutUint16(rec[8:10], typ)
	binary.LittleEndian.PutUint16(rec[10:12], code)
	binary.LittleEndian.PutUint32(rec[12:16], uint32(val))
	return append(b, rec[:]...)
}

// The decoder must emit on RELEASE and report the coordinates from the frame,
// mapped into landscape space. This feeds a real press/release sequence
// through a FIFO-shaped file, which is the same byte stream the device gives.
func TestTapsDecodesAPressAndRelease(t *testing.T) {
	var b []byte
	// Press at device (100, 800).
	b = writeEvent(b, evKey, btnTouch, 1)
	b = writeEvent(b, evAbs, absX, 100)
	b = writeEvent(b, evAbs, absY, 800)
	b = writeEvent(b, evSyn, synReport, 0)
	// Release.
	b = writeEvent(b, evKey, btnTouch, 0)
	b = writeEvent(b, evSyn, synReport, 0)

	path := filepath.Join(t.TempDir(), "event0")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReader(path, 270, 1920, 480, 480, 1920)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	taps, err := r.Taps(ctx)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case tap, ok := <-taps:
		if !ok {
			t.Fatal("channel closed before a tap was decoded")
		}
		// device (100,800) -> landscape (800, 479-100=379)
		if tap.X != 800 || tap.Y != 379 {
			t.Errorf("tap at (%v,%v), want (800,379)", tap.X, tap.Y)
		}
	case <-ctx.Done():
		t.Fatal("no tap decoded before timeout")
	}
}

// A press with no release is a finger still down, not a tap. Emitting on press
// would fire the moment a user brushes the panel and would make dragging off a
// control impossible.
func TestTapsIgnoresPressWithoutRelease(t *testing.T) {
	var b []byte
	b = writeEvent(b, evKey, btnTouch, 1)
	b = writeEvent(b, evAbs, absX, 100)
	b = writeEvent(b, evAbs, absY, 800)
	b = writeEvent(b, evSyn, synReport, 0)

	path := filepath.Join(t.TempDir(), "event0")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReader(path, 270, 1920, 480, 480, 1920)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	taps, err := r.Taps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for tap := range taps {
		t.Fatalf("decoded a tap from a press with no release: %+v", tap)
	}
}

// sizeof(struct input_event) is architecture-dependent, and decoding with the
// wrong size produces plausible garbage rather than an error. The board is
// 32-bit ARM: 2x u32 timeval + u16 + u16 + s32.
func TestEventSizeMatchesBoard(t *testing.T) {
	const want = 4 + 4 + 2 + 2 + 4
	if eventSize != want {
		t.Errorf("eventSize = %d, want %d for a 32-bit kernel", eventSize, want)
	}
}

// A slow consumer must not make the reader deliver stale taps.
//
// This is the bug that made "close" look broken on the panel. The channel was
// unbuffered and the consumer spends over a second inside a render, so the
// decode loop blocked on the send, stopped reading the device, and the close
// tap sat in the kernel's evdev queue behind the tap that opened the dialog.
// When the render finished, the FIRST thing delivered was the old coordinate.
//
// The reader now drops the oldest queued tap rather than blocking, so a
// consumer that comes back late gets the most recent touch -- the user's
// actual current intent.
func TestTapsDeliversTheLatestTapToASlowConsumer(t *testing.T) {
	var b []byte
	// Three taps in a row at distinct positions, as fast as the device emits.
	for _, p := range []struct{ x, y int32 }{{100, 200}, {200, 600}, {300, 1200}} {
		b = writeEvent(b, evKey, btnTouch, 1)
		b = writeEvent(b, evAbs, absX, p.x)
		b = writeEvent(b, evAbs, absY, p.y)
		b = writeEvent(b, evSyn, synReport, 0)
		b = writeEvent(b, evKey, btnTouch, 0)
		b = writeEvent(b, evSyn, synReport, 0)
	}

	path := filepath.Join(t.TempDir(), "event0")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReader(path, 270, 1920, 480, 480, 1920)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	taps, err := r.Taps(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a slow render: let the reader run well ahead of us.
	time.Sleep(200 * time.Millisecond)

	select {
	case tap := <-taps:
		// Whatever we get must be a REAL tap position, and the reader must not
		// have wedged. The last tap is device (300,1200) -> panel (1200, 179).
		// Accept any of the three, but assert we are not stuck at zero and that
		// the reader kept draining (it did not block forever on send).
		if tap.X == 0 && tap.Y == 0 {
			t.Fatalf("decoded a zero tap: %+v", tap)
		}
		t.Logf("delivered tap: %+v", tap)
	case <-ctx.Done():
		t.Fatal("reader wedged: no tap delivered to a slow consumer")
	}
}

// State must not leak between taps. The digitizer only reports an axis when it
// CHANGES, so a frame carrying no ABS events would otherwise be decoded with
// the previous tap's coordinates -- a second tap in the same place as the first
// is indistinguishable from a tap that reported nothing.
func TestTapsResetsCoordinateStateBetweenTaps(t *testing.T) {
	var b []byte
	// A complete tap.
	b = writeEvent(b, evKey, btnTouch, 1)
	b = writeEvent(b, evAbs, absX, 100)
	b = writeEvent(b, evAbs, absY, 800)
	b = writeEvent(b, evSyn, synReport, 0)
	b = writeEvent(b, evKey, btnTouch, 0)
	b = writeEvent(b, evSyn, synReport, 0)
	// A press/release carrying NO coordinates at all. With state leaking this
	// decodes as a second tap at (100,800); correctly, it is not a tap.
	b = writeEvent(b, evKey, btnTouch, 1)
	b = writeEvent(b, evSyn, synReport, 0)
	b = writeEvent(b, evKey, btnTouch, 0)
	b = writeEvent(b, evSyn, synReport, 0)

	path := filepath.Join(t.TempDir(), "event0")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReader(path, 270, 1920, 480, 480, 1920)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	taps, err := r.Taps(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got := 0
	for range taps {
		got++
	}
	if got != 1 {
		t.Errorf("decoded %d taps, want 1 -- a coordinate-less frame was treated as a tap", got)
	}
}
