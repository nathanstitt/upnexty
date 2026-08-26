package chart

import (
	"embed"
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

// Icon returns inline SVG markup for a WMO weather code. Never returns emoji:
// the board has no emoji font and missing glyphs render as nothing at all.
func Icon(code int) string {
	iconOnce.Do(func() {
		iconCache = map[string]string{}
		entries, _ := iconFS.ReadDir("icons")
		for _, e := range entries {
			b, err := iconFS.ReadFile("icons/" + e.Name())
			if err != nil {
				continue
			}
			stem := e.Name()[:len(e.Name())-len(".svg")]
			iconCache[stem] = string(b)
		}
	})
	if s, ok := iconCache[wmoIcon(code)]; ok {
		return s
	}
	return `<svg viewBox="0 0 24 24" width="24" height="24"></svg>`
}
