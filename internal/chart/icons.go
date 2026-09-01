package chart

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed icons/*.svg
var iconFS embed.FS

var (
	iconOnce  sync.Once
	iconCache map[string]string
)

// wmoIcon maps a WMO weather code to an icon file stem.
func wmoIcon(code int) string {
	switch code {
	case 0:
		return "clear"
	case 1:
		return "mainly-clear"
	case 2:
		return "partly-cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 61, 63, 65, 80, 81:
		return "rain"
	case 71, 73:
		return "snow"
	case 75:
		return "heavy-snow"
	case 82:
		return "showers"
	case 95, 96, 99:
		return "thunderstorm"
	default:
		return "overcast"
	}
}

// sizeAttrs matches the width/height presentation attributes on an icon's root
// <svg>, which the source files author at the 24px viewBox size.
var sizeAttrs = regexp.MustCompile(`\s(?:width|height)="[^"]*"`)

// Icon returns inline SVG markup for a WMO weather code, sized to fill a
// sizePx box.
//
// Never returns emoji: the board carries only DejaVu and Liberation, neither of
// which has emoji, and omnidoc paints glyphs from outlines alone
// (render.Device's glyph source exposes only Outline(gid) *Path -- there is no
// CBDT/sbix bitmap or COLR/CPAL layer path). A colour emoji font would render
// blank and a monochrome one would render flat silhouettes, so vector art is
// both the only option and the better one.
//
// The size is written onto the <svg> so each call site states the box it has,
// and so the markup is self-describing rather than depending on a selector
// elsewhere. CSS width/height would also work -- probed: an icon with
// width="24" in a rule setting 80px paints at 80 either way -- so this is a
// clarity choice, not a workaround. The viewBox is preserved, so the art
// scales instead of cropping.
func Icon(code int, sizePx int) string {
	iconOnce.Do(func() {
		iconCache = map[string]string{}
		entries, _ := iconFS.ReadDir("icons")
		for _, e := range entries {
			b, err := iconFS.ReadFile("icons/" + e.Name())
			if err != nil {
				continue
			}
			stem := e.Name()[:len(e.Name())-len(".svg")]
			// Drop the authored size so each call site can impose its own.
			iconCache[stem] = sizeAttrs.ReplaceAllString(string(b), "")
		}
	})
	s, ok := iconCache[wmoIcon(code)]
	if !ok {
		s = `<svg viewBox="0 0 24 24"></svg>`
	}
	dim := fmt.Sprintf(`<svg width="%d" height="%d"`, sizePx, sizePx)
	return strings.Replace(s, "<svg", dim, 1)
}

// Describe returns the short uppercase condition text shown beside the current
// temperature, e.g. "CLEAR", "LIGHT RAIN".
func Describe(code int) string {
	switch code {
	case 0:
		return "Clear"
	case 1:
		return "Mainly clear"
	case 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45, 48:
		return "Fog"
	case 51, 53, 55:
		return "Drizzle"
	case 61, 63:
		return "Rain"
	case 65:
		return "Heavy rain"
	case 71, 73:
		return "Snow"
	case 75:
		return "Heavy snow"
	case 80, 81, 82:
		return "Showers"
	case 95, 96, 99:
		return "Thunderstorm"
	default:
		return "—"
	}
}
