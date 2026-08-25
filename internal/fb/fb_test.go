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
