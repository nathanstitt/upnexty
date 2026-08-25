// Package fb rasterizes HTML and writes it to the Linux framebuffer.
package fb

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strconv"
	"strings"

	"github.com/nathanstitt/doctaculous/pkg/doctaculous"
)

// Geometry reads the visible resolution from sysfs.
func Geometry(dev string) (int, int, error) {
	name := dev[strings.LastIndex(dev, "/")+1:]
	raw, err := os.ReadFile("/sys/class/graphics/" + name + "/virtual_size")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected virtual_size %q", raw)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return w, h, nil
}

// RenderHTML lays out and rasterizes an HTML document at exactly pageW x pageH.
//
// WithPageSize is required: without it the layout viewport defaults to 1280px,
// and doctaculous's fit-within sizing preserves aspect ratio, so the render
// comes back 1280x480 and is pillarboxed with white on a 1920x480 panel.
func RenderHTML(ctx context.Context, html []byte, pageW, pageH int) (image.Image, error) {
	doc, err := doctaculous.OpenHTMLBytes(html, doctaculous.WithPageSize(float64(pageW), float64(pageH)))
	if err != nil {
		return nil, fmt.Errorf("layout: %w", err)
	}
	img, err := doc.RasterizePage(ctx, 0, doctaculous.RasterOptions{
		MaxWidthPx: pageW, MaxHeightPx: pageH, Background: color.White,
	})
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	return img, nil
}

// Pack converts an image to XR24 bytes for a fbW x fbH framebuffer, rotating
// clockwise by the given angle. Pixel format is B,G,R,X little-endian.
func Pack(img image.Image, fbW, fbH, rotate int) []byte {
	b := img.Bounds()
	pageW, pageH := b.Dx(), b.Dy()

	canvas := image.NewRGBA(image.Rect(0, 0, pageW, pageH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, b.Min, draw.Src)

	buf := make([]byte, fbW*fbH*4)
	for y := 0; y < fbH; y++ {
		row := y * fbW * 4
		for x := 0; x < fbW; x++ {
			var sx, sy int
			switch rotate {
			case 90:
				sx, sy = y, pageH-1-x
			case 180:
				sx, sy = pageW-1-x, pageH-1-y
			case 270:
				sx, sy = pageW-1-y, x
			default:
				sx, sy = x, y
			}
			if sx < 0 || sy < 0 || sx >= pageW || sy >= pageH {
				continue
			}
			s := sy*canvas.Stride + sx*4
			d := row + x*4
			buf[d+0] = canvas.Pix[s+2] // B
			buf[d+1] = canvas.Pix[s+1] // G
			buf[d+2] = canvas.Pix[s+0] // R
			buf[d+3] = 0               // X
		}
	}
	return buf
}

// Write writes a packed buffer to the framebuffer device.
func Write(dev string, buf []byte) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteAt(buf, 0)
	return err
}
