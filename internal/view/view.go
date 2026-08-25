// Package view renders the view model to a complete HTML document. CSS is
// inlined and SVG embedded: the board loads no external resources, and
// doctaculous discards <script>, so all layout must be static.
package view

import (
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/nathanstitt/luckfox-dashboard/internal/chart"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
)

//go:embed templates/*.html assets/*.css
var assetFS embed.FS

// labelRowTop is the y offset of each label row within the track.
var labelRowTop = []float64{78, 52}

type labelView struct {
	Text    string
	X       float64
	Top     float64
	Anchor  float64
	Drifted bool
}

type forecastView struct {
	Day  string
	Code int
	Hi   string
	Lo   string
	Pop  string
}

type pageData struct {
	VM       model.ViewModel
	CSS      template.CSS
	Chart    template.HTML
	Labels   []labelView
	Forecast []forecastView
}

var funcs = template.FuncMap{
	"px":   func(v float64) template.CSS { return template.CSS(fmt.Sprintf("%.1fpx", v)) },
	"icon": func(code int) template.HTML { return template.HTML(chart.Icon(code)) },
}

var tmpl = template.Must(
	template.New("dashboard.html").Funcs(funcs).ParseFS(assetFS, "templates/dashboard.html"),
)

// Render produces the full HTML document for a view model.
//
// vm.Labels is NOT index-aligned with vm.Blocks (model.PlaceLabels may omit
// labels that don't fit), so blocks and labels are iterated independently
// here and in the template — never zipped by index. Each label carries
// Anchor (its block's own X) to draw a leader mark when it has drifted.
func Render(vm model.ViewModel) (string, error) {
	css, err := assetFS.ReadFile("assets/style.css")
	if err != nil {
		return "", err
	}

	labels := make([]labelView, 0, len(vm.Labels))
	for _, l := range vm.Labels {
		row := l.Row
		if row >= len(labelRowTop) {
			row = len(labelRowTop) - 1
		}
		labels = append(labels, labelView{
			Text: l.Text, X: l.X, Top: labelRowTop[row],
			Anchor: l.Anchor, Drifted: l.X-l.Anchor > 2,
		})
	}

	forecast := make([]forecastView, 0, len(vm.Forecast))
	for i, d := range vm.Forecast {
		day := d.Date.Format("Mon")
		if i == 0 {
			day = "Today"
		}
		pop := "—"
		if d.PrecipProb > 0 {
			pop = fmt.Sprintf("%d%%", d.PrecipProb)
		}
		forecast = append(forecast, forecastView{
			Day: day, Code: d.Code,
			Hi:  fmt.Sprintf("%.0f", d.HiF),
			Lo:  fmt.Sprintf("%.0f", d.LoF),
			Pop: pop,
		})
	}

	// chart.Hourly wants a *weather.Weather but only ever reads .Hourly off
	// it; ViewModel carries the hourly series directly (Render takes no
	// separate weather argument), so build a throwaway Weather just to
	// satisfy that signature rather than widening Render's contract.
	w := &weather.Weather{Hourly: vm.Hourly}

	data := pageData{
		VM:       vm,
		CSS:      template.CSS(css),
		Chart:    template.HTML(chart.Hourly(w, vm.Window, 104)),
		Labels:   labels,
		Forecast: forecast,
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}
