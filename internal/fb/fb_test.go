package fb

import (
	"image"
	"image/color"
	"testing"
)

func TestPackProducesCorrectSize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 480))
	buf := Pack(img, 480, 1920, 270)
	if len(buf) != 480*1920*4 {
		t.Errorf("len = %d, want %d", len(buf), 480*1920*4)
	}
}

func TestPackWritesBGRX(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	// Fill with pure red so byte order is unambiguous.
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	buf := Pack(img, 2, 4, 270)
	if buf[0] != 0 || buf[1] != 0 || buf[2] != 255 {
		t.Errorf("pixel bytes = %d,%d,%d; want B=0,G=0,R=255", buf[0], buf[1], buf[2])
	}
}

// Rotating 270 must map the canvas's top-left corner to a known framebuffer
// position. With fb(x,y) = canvas(pageW-1-y, x), canvas(0,0) lands at
// fb(x=0, y=pageW-1).
func TestPackRotates270(t *testing.T) {
	const pageW, pageH = 4, 2
	img := image.NewRGBA(image.Rect(0, 0, pageW, pageH))
	for y := 0; y < pageH; y++ {
		for x := 0; x < pageW; x++ {
			img.Set(x, y, color.RGBA{A: 255}) // black
		}
	}
	img.Set(0, 0, color.RGBA{G: 255, A: 255}) // mark the corner

	fbW, fbH := pageH, pageW // 2x4
	buf := Pack(img, fbW, fbH, 270)
	off := (pageW-1)*fbW*4 + 0*4
	if buf[off+1] != 255 {
		t.Errorf("green marker not at fb(0,%d); got B=%d G=%d R=%d",
			pageW-1, buf[off], buf[off+1], buf[off+2])
	}
}

func TestPackRotate0IsIdentity(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	buf := Pack(img, 2, 2, 0)
	if buf[0] != 255 {
		t.Errorf("blue pixel not at origin; got B=%d", buf[0])
	}
}

func TestPackPanicsOnDimensionMismatch270(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 480))
	defer func() {
		if r := recover(); r == nil {
			t.Error("Pack(1920x480, 480x480, 270) did not panic on dimension mismatch")
		}
	}()
	Pack(img, 480, 480, 270) // Wrong: fbH should be 1920
}

func TestPackPanicsOnDimensionMismatchRotate0(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	defer func() {
		if r := recover(); r == nil {
			t.Error("Pack(2x2, 4x4, 0) did not panic on dimension mismatch")
		}
	}()
	Pack(img, 4, 4, 0) // Wrong: both should be 2x2
}

// Rotating 90 degrees: sx, sy = y, pageH-1-x, so canvas(0,0) is read when
// fb_y=0, fb_x=pageH-1. For a 4x2 canvas rotated to 2x4 fb, that's fb(1,0).
func TestPackRotates90(t *testing.T) {
	const pageW, pageH = 4, 2
	img := image.NewRGBA(image.Rect(0, 0, pageW, pageH))
	for y := 0; y < pageH; y++ {
		for x := 0; x < pageW; x++ {
			img.Set(x, y, color.RGBA{A: 255}) // black
		}
	}
	img.Set(0, 0, color.RGBA{B: 255, A: 255}) // mark the corner

	fbW, fbH := pageH, pageW // 2x4
	buf := Pack(img, fbW, fbH, 90)
	off := 0*fbW*4 + (pageH-1)*4 // fb(pageH-1, 0) = fb(1, 0)
	if buf[off] != 255 {
		t.Errorf("blue marker not at fb(1,0); got B=%d", buf[off])
	}
}

// Rotating 180 degrees should map canvas(x,y) to fb(pageW-1-x, pageH-1-y).
// For a 2x2 canvas, canvas(0,0) should land at fb(1,1).
func TestPackRotates180(t *testing.T) {
	const pageW, pageH = 2, 2
	img := image.NewRGBA(image.Rect(0, 0, pageW, pageH))
	for y := 0; y < pageH; y++ {
		for x := 0; x < pageW; x++ {
			img.Set(x, y, color.RGBA{A: 255}) // black
		}
	}
	img.Set(0, 0, color.RGBA{R: 255, A: 255}) // mark the corner

	fbW, fbH := pageW, pageH // 2x2
	buf := Pack(img, fbW, fbH, 180)
	off := (pageH-1)*fbW*4 + (pageW-1)*4 // fb(pageW-1, pageH-1) = fb(1, 1)
	if buf[off+2] != 255 {
		t.Errorf("red marker not at fb(1,1); got R=%d", buf[off+2])
	}
}
