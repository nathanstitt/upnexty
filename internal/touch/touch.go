// Package touch reads taps from the panel's Linux input device.
//
// The panel is a Goodix capacitive digitizer on /dev/input/event0, reporting
// absolute coordinates in the display's native PORTRAIT orientation (0..479 x,
// 0..1919 y) regardless of how the framebuffer is rotated. The dashboard
// renders landscape, so every tap is rotated into panel coordinates here --
// see Rotate270.
//
// Only single taps are decoded. The digitizer reports multitouch slots, but a
// wall panel is operated with one finger and every extra code would be state to
// get wrong; ABS_X/ABS_Y carry the first contact, which is all a tap needs.
package touch

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

// Linux input_event codes. See include/uapi/linux/input-event-codes.h.
const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport = 0x00

	absX = 0x00
	absY = 0x01

	btnTouch = 0x14a
)

// eventSize is sizeof(struct input_event) on a 32-bit kernel: two 32-bit
// timeval words, then u16 type, u16 code, s32 value.
//
// This is ARCH-DEPENDENT and the board is armv7l. A 64-bit kernel uses 64-bit
// time_t and the struct is 24 bytes; decoding with the wrong size yields
// plausible-looking garbage rather than an error, so this constant is pinned
// by TestEventSizeMatchesBoard rather than left to inference.
const eventSize = 16

// Tap is a completed touch: a press followed by a release, reported in
// landscape panel coordinates.
type Tap struct {
	X, Y float64
	At   time.Time
}

// Reader decodes taps from an input device.
type Reader struct {
	path   string
	rotate int
	// panelW and panelH are the LANDSCAPE dimensions taps are reported in.
	panelW, panelH float64
	// deviceW and deviceH are the digitizer's own coordinate space, portrait.
	deviceW, deviceH float64
}

// NewReader builds a reader for a device path. rotate must match the angle the
// framebuffer is rendered at (270 for this panel's landscape layout); the
// mapping is the inverse of the display rotation, so a tap on a pixel returns
// that pixel's landscape coordinates.
func NewReader(path string, rotate int, panelW, panelH, deviceW, deviceH float64) *Reader {
	return &Reader{
		path: path, rotate: rotate,
		panelW: panelW, panelH: panelH,
		deviceW: deviceW, deviceH: deviceH,
	}
}

// Rotate270 maps a portrait digitizer coordinate to landscape panel space.
//
// The panel is physically portrait (480 wide x 1920 tall) and the UI is drawn
// rotated 270 degrees, so landscape x runs along the device's y axis and
// landscape y runs backwards along the device's x axis:
//
//	panelX = deviceY
//	panelY = (deviceW - 1) - deviceX
//
// Derived rather than guessed, and asserted at the corners by
// TestRotate270MapsCorners: getting the sign wrong produces a mirrored panel
// that still hit-tests plausibly near the centre, which is the failure mode
// that survives casual testing.
func (r *Reader) Rotate270(dx, dy float64) (x, y float64) {
	switch r.rotate {
	case 270:
		return dy, (r.deviceW - 1) - dx
	case 90:
		return (r.deviceH - 1) - dy, dx
	case 180:
		return (r.deviceW - 1) - dx, (r.deviceH - 1) - dy
	default:
		return dx, dy
	}
}

// Taps opens the device and streams completed taps until ctx is cancelled.
//
// A tap is emitted on RELEASE, not on press: releasing is when the user has
// committed, and it means a press-and-drag off a control does not fire it. The
// coordinates reported are the last ones seen before release.
//
// The device is opened non-blocking-free (a plain blocking read) and the
// goroutine parks in Read until a finger arrives, which costs nothing while
// idle. Closing the returned channel is the reader's job; the caller only
// cancels the context.
func (r *Reader) Taps(ctx context.Context) (<-chan Tap, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("open touch device: %w", err)
	}

	// Buffered, and deliberately shallow. The consumer spends over a second
	// inside a render, and an UNBUFFERED channel makes the decode loop block on
	// the send for that whole time -- so it stops reading the device, taps pile
	// up in the kernel's evdev queue, and the next one delivered is a stale
	// coordinate from a frame the user has already moved on from. That is what
	// made "close" appear not to work: the close tap was queued behind the tap
	// that opened the dialog.
	//
	// A small buffer keeps the reader draining the device. It is 1 rather than
	// larger because a wall panel wants the LATEST intent, not a backlog: see
	// the drop-oldest policy at the send.
	out := make(chan Tap, 1)
	go func() {
		defer close(out)
		defer f.Close()

		// Cancelling the context must unblock a read that is parked waiting for
		// a finger. Closing the file is what does that -- the read returns an
		// error and the loop exits.
		go func() {
			<-ctx.Done()
			f.Close()
		}()

		var (
			buf     = make([]byte, eventSize*32)
			curX    float64
			curY    float64
			haveX   bool
			haveY   bool
			pressed bool
			// released is set by BTN_TOUCH going 0 within the current frame and
			// acted on at SYN_REPORT, so the coordinates reported are the
			// complete set for that frame.
			released bool
		)

		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			for off := 0; off+eventSize <= n; off += eventSize {
				rec := buf[off : off+eventSize]
				typ := binary.LittleEndian.Uint16(rec[8:10])
				code := binary.LittleEndian.Uint16(rec[10:12])
				val := int32(binary.LittleEndian.Uint32(rec[12:16]))

				switch typ {
				case evAbs:
					switch code {
					case absX:
						curX, haveX = float64(val), true
					case absY:
						curY, haveY = float64(val), true
					}
				case evKey:
					if code == btnTouch {
						if val == 1 {
							pressed = true
						} else {
							released = true
						}
					}
				case evSyn:
					if code != synReport {
						continue
					}
					if released && pressed && haveX && haveY {
						x, y := r.Rotate270(curX, curY)
						tap := Tap{X: x, Y: y, At: time.Now()}
						// Never block the decode loop. If the consumer is still
						// rendering the previous tap, drop the OLDEST queued tap
						// and keep this one: on a panel the most recent touch is
						// the user's current intent, and delivering a stale
						// coordinate after a 1.2s render is worse than dropping
						// it -- it acts on a screen that is no longer there.
						select {
						case out <- tap:
						case <-ctx.Done():
							return
						default:
							select {
							case <-out:
							default:
							}
							select {
							case out <- tap:
							case <-ctx.Done():
								return
							default:
							}
						}
					}
					if released {
						// Reset the whole frame, not just the button state.
						// haveX/haveY leaking across taps means a frame that
						// reports no coordinates (the digitizer only sends an
						// axis when it CHANGES) is decoded with the previous
						// tap's position.
						pressed, released = false, false
						haveX, haveY = false, false
					}
				}
			}
		}
	}()
	return out, nil
}
