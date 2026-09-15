// Package view renders the view model to a complete HTML document. CSS is
// inlined and SVG embedded: the board loads no external resources, and
// omnidoc discards <script>, so all layout must be static.
package view

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"path"
	"strings"

	"github.com/nathanstitt/omnidoc/pkg/resource"

	"github.com/nathanstitt/upnexty/internal/chart"
	"github.com/nathanstitt/upnexty/internal/model"
	"github.com/nathanstitt/upnexty/internal/weather"
)

//go:embed templates/*.html assets/*.css assets/fonts/*.ttf assets/img/*.svg
var assetFS embed.FS

// FontLoader serves the embedded font files to omnidoc, which resolves
// @font-face url() refs through a resource loader.
//
// The fonts must arrive this way rather than by installing them on the board:
// omnidoc's OS font lookup goes through adrg/sysfont, whose matcher only
// recognizes families in its own hardcoded registry. That registry has 32
// DejaVu entries and zero for Roboto, so an
// installed-but-unregistered family silently resolves to DejaVu no matter which
// directory it sits in. Verified on hardware: fonts in /usr/share/fonts (the
// real xdg search path) still rendered as DejaVu; the same files loaded via
// @font-face url() rendered correctly.
//
// Embedding also means the fonts ship inside the binary -- nothing extra to
// deploy, and no way for the panel to lose its typefaces.
func FontLoader() resource.ResourceLoader {
	return fontLoader{}
}

type fontLoader struct{}

func (fontLoader) Load(_ context.Context, ref string) ([]byte, string, error) {
	// Only font refs are served; the stylesheet is inlined into the document.
	name := path.Base(ref)
	b, err := assetFS.ReadFile("assets/fonts/" + name)
	if err != nil {
		return nil, "", fmt.Errorf("view: font %q: %w", name, err)
	}
	return b, "font/ttf", nil
}

// chartHeightPx is the weather curve band's height and MUST match #wx-strip in
// style.css. The hour axis (#wx-hours) is a separate row below it. The chart
// insets its own top and bottom (topPadPx/bottomPadPx), so it takes the whole
// strip.
const chartHeightPx = 104

type forecastView struct {
	Day    string
	Code   int
	Hi     string
	Lo     string
	Pop    string
	HasPop bool
}

// hourTick is one label on the weather strip's time axis.
type hourTick struct {
	XPx   float64
	Label string
}

type pageData struct {
	VM        model.ViewModel
	CSS       template.CSS
	Chart     template.HTML
	Forecast  []forecastView
	HourTicks []hourTick
	TodayPop  string
}

var funcs = template.FuncMap{
	"px": func(v float64) template.CSS { return template.CSS(fmt.Sprintf("%.1fpx", v)) },
	// Sized at the call site so the markup states the box it occupies.
	"icon": func(code, sizePx int) template.HTML { return template.HTML(chart.Icon(code, sizePx)) },
	// The end-of-day mark, inlined rather than served: the engine has no
	// network and an <img src> would need a resource-loader round trip for a
	// file that never changes.
	"cheers": func() template.HTML { return template.HTML(cheersSVG) },
	// alpha renders a calendar colour as a faint tint for all-day pills. The
	// engine supports #RRGGBBAA, so the suffix is appended rather than
	// converted to rgba().
	"alpha":   func(hex string) template.CSS { return template.CSS(safeHexColor(hex, "#4a90d9") + "22") },
	"wmoText": func(code int) string { return chart.Describe(code) },
	"pct":     func(v float64) template.CSS { return template.CSS(fmt.Sprintf("%.1f%%", v)) },
	// accent is a calendar colour used as the dialog's accent. Same validation
	// as alpha: these values come from a remote iCal feed.
	"accent": func(hex string) template.CSS { return template.CSS(safeHexColor(hex, "#4a90d9")) },
}

// safeHexColor returns hex if it is a literal #RGB/#RRGGBB colour, else def.
//
// These strings come from a remote calendar feed and are interpolated into a
// style attribute, where template/html cannot sanitize them: it treats
// template.CSS as already-trusted. A feed serving a COLOR of
// "red;background:url(...)" would otherwise inject declarations. Validating
// the shape is cheaper than escaping and leaves nothing to reason about.
func safeHexColor(hex, def string) string {
	if n := len(hex); (n != 4 && n != 7) || hex[0] != '#' {
		return def
	}
	for _, r := range hex[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return def
		}
	}
	return hex
}

var tmpl = template.Must(
	template.New("dashboard.html").Funcs(funcs).ParseFS(assetFS, "templates/dashboard.html"),
)

// Render produces the full HTML document for a view model.
func Render(vm model.ViewModel) (string, error) {
	css, err := assetFS.ReadFile("assets/style.css")
	if err != nil {
		return "", err
	}

	forecast := make([]forecastView, 0, len(vm.Forecast))
	for i, d := range vm.Forecast {
		day := d.Date.Format("Mon")
		if i == 0 {
			day = "Today"
		}
		pop, hasPop := "—", d.PrecipProb > 0
		if hasPop {
			pop = fmt.Sprintf("%d%%", d.PrecipProb)
		}
		forecast = append(forecast, forecastView{
			Day: day, Code: d.Code,
			Hi:     fmt.Sprintf("%.0f", d.HiF),
			Lo:     fmt.Sprintf("%.0f", d.LoF),
			Pop:    pop,
			HasPop: hasPop,
		})
	}

	// chart.Hourly wants a *weather.Weather but only ever reads .Hourly off
	// it; ViewModel carries the hourly series directly (Render takes no
	// separate weather argument), so build a throwaway Weather just to
	// satisfy that signature rather than widening Render's contract.
	w := &weather.Weather{Hourly: vm.Hourly}

	// The weather strip and its hour axis share one window so the labels line
	// up with the curve. It is anchored to the NOW bar's x so the bar crosses
	// the curve at the current temperature -- the bar spans both bands and
	// claims one instant for the whole panel, so the chart follows it.
	wxWin := chart.SpanWindow(vm.Window, vm.Now, vm.Agenda.NowBarXPx)
	ticks := make([]hourTick, 0, 16)
	for _, t := range chart.HourTicks(wxWin) {
		ticks = append(ticks, hourTick{XPx: t.XPx, Label: t.Label})
	}

	todayPop := "0%"
	if len(vm.Forecast) > 0 {
		todayPop = fmt.Sprintf("%d%%", vm.Forecast[0].PrecipProb)
	}

	data := pageData{
		VM:  vm,
		CSS: template.CSS(css),
		// chartHeightPx is the curve band only; the hour axis sits below it.
		Chart:     template.HTML(chart.Hourly(w, wxWin, chartHeightPx)),
		Forecast:  forecast,
		HourTicks: ticks,
		TodayPop:  todayPop,
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// cheersSVG is the end-of-day illustration, read once at startup.
//
// Inlined into the page rather than referenced with <img src>: the SVG is
// static, and inlining avoids a resource-loader round trip per render. It
// carries its own fills, so nothing in style.css needs to reach into it --
// which is just as well, since CSS does not cascade into an inline <svg> on
// this engine (see the note in internal/chart).
//
// The file keeps its width/height of 100%, so it fills whatever box the
// stylesheet gives it and the caller states the size.
var cheersSVG = func() string {
	b, err := assetFS.ReadFile("assets/img/cheers.svg")
	if err != nil {
		// Embedded at build time: a failure here means the asset was renamed
		// or dropped, which no runtime fallback can repair.
		panic("view: cheers.svg missing from the embedded assets: " + err.Error())
	}
	return string(b)
}()
