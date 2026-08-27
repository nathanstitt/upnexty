package portal

import (
	"embed"
	"html/template"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
)

//go:embed templates/*.html assets/*.css
var assetFS embed.FS

var tmpl = template.Must(template.ParseFS(assetFS, "templates/*.html"))

// pageData is everything the templates read.
type pageData struct {
	Config          *config.Config
	Status          wifi.Status
	APName          string
	DefaultPassword string
	Error           string
}
