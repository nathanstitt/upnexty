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

	"github.com/nathanstitt/omnidoc/pkg/omnidoc"
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
// and omnidoc's fit-within sizing preserves aspect ratio, so the render
// comes back 1280x480 and is pillarboxed with white on a 1920x480 panel.
//
// Both halves take ctx: parse/layout is the half that can run long on a
// pathological document, so the non-context OpenHTMLBytes would leave a wedged
// render burning one of the board's three cores with no way to stop it.
func RenderHTML(ctx context.Context, html []byte, pageW, pageH int, opts ...omnidoc.HTMLOption) (image.Image, error) {
	all := append([]omnidoc.HTMLOption{
		omnidoc.WithPageSize(float64(pageW), float64(pageH)),
	}, opts...)
	doc, err := omnidoc.OpenHTMLBytesContext(ctx, html, all...)
	if err != nil {
		return nil, fmt.Errorf("layout: %w", err)
	}
	img, err := doc.RasterizePage(ctx, 0, omnidoc.RasterOptions{
		MaxWidthPx: pageW, MaxHeightPx: pageH, Background: color.White,
	})
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	return img, nil
}

// Pack converts an image to XR24 bytes for a fbW x fbH framebuffer, rotating
// clockwise by the given angle. Pixel format is B,G,R,X little-endian.
//
// Preconditions (enforced by panic for contract violations):
//   - For rotate 90 or 270: fbW must equal pageH and fbH must equal pageW.
//   - For rotate 0 or 180: fbW must equal pageW and fbH must equal pageH.
//
// Silently cropping mismatched dimensions would produce undiagnosed wrong images
// on deployed hardware, so dimension violations panic immediately.
func Pack(img image.Image, fbW, fbH, rotate int) []byte {
	b := img.Bounds()
	pageW, pageH := b.Dx(), b.Dy()

	// Enforce preconditions to prevent silent data loss.
	switch rotate {
	case 90, 270:
		if fbW != pageH || fbH != pageW {
			panic(fmt.Sprintf("Pack: rotate %d requires fbW=%d, fbH=%d to match pageH=%d, pageW=%d", rotate, fbW, fbH, pageH, pageW))
		}
	case 0, 180:
		if fbW != pageW || fbH != pageH {
			panic(fmt.Sprintf("Pack: rotate %d requires fbW=%d, fbH=%d to match pageW=%d, pageH=%d", rotate, fbW, fbH, pageW, pageH))
		}
	default:
		panic(fmt.Sprintf("Pack: unsupported rotate angle %d", rotate))
	}

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
