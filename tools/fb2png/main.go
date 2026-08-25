// Command fb2png converts a raw XR24 framebuffer dump to PNG.
//
//	adb shell cat /dev/fb0 > fb.raw
//	go run ./tools/fb2png fb.raw out.png 480 1920 unrotate
//
// The panel is 480x1920 portrait; the UI is a 1920x480 landscape canvas rotated
// 270 at blit time. "unrotate" inverts that so the PNG reads upright.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: fb2png <in.raw> <out.png> <fbW> <fbH> [unrotate]")
		os.Exit(2)
	}
	fbW, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fatal("bad fbW: %v", err)
	}
	fbH, err := strconv.Atoi(os.Args[4])
	if err != nil {
		fatal("bad fbH: %v", err)
	}
	unrot := len(os.Args) > 5 && os.Args[5] == "unrotate"

	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal("%v", err)
	}
	if len(raw) < fbW*fbH*4 {
		fatal("short dump: %d bytes, need %d", len(raw), fbW*fbH*4)
	}
	at := func(x, y int) color.RGBA {
		s := y*fbW*4 + x*4
		return color.RGBA{R: raw[s+2], G: raw[s+1], B: raw[s+0], A: 255}
	}

	var img *image.RGBA
	if unrot {
		cw, ch := fbH, fbW // 1920x480
		img = image.NewRGBA(image.Rect(0, 0, cw, ch))
		for cy := 0; cy < ch; cy++ {
			for cx := 0; cx < cw; cx++ {
				img.Set(cx, cy, at(cy, cw-1-cx))
			}
		}
	} else {
		img = image.NewRGBA(image.Rect(0, 0, fbW, fbH))
		for y := 0; y < fbH; y++ {
			for x := 0; x < fbW; x++ {
				img.Set(x, y, at(x, y))
			}
		}
	}

	f, err := os.Create(os.Args[2])
	if err != nil {
		fatal("%v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fatal("%v", err)
	}
	b := img.Bounds()
	fmt.Printf("wrote %s (%dx%d)\n", os.Args[2], b.Dx(), b.Dy())
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
