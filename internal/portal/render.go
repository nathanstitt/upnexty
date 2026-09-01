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
	// WiFiError is the most recent background Connect failure (see
	// Server.setWiFiErr), shown on the network section so a bad password or
	// write failure is not silently invisible -- the connect attempt runs
	// after the request that triggered it has already redirected.
	WiFiError string

	// PendingSSID is the network a just-submitted save is trying to join. Only
	// the pending page reads it.
	PendingSSID string

	// Next is the path to return to after a successful login. Only the login
	// page reads it.
	Next string
}
