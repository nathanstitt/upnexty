package portal

import (
	"embed"
	"html/template"

	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/wifi"
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
	// Hostname is the name the board is answering to right now, which is not
	// necessarily Config.Device.Hostname: the config is empty until someone
	// sets one, and a save that could not be applied leaves the two disagreeing
	// until the next boot. Shown as the field's placeholder so an unset box
	// still tells the user what the board is called.
	Hostname string
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

	// GoogleEnabled reports that the board has OAuth credentials. When false
	// the section is hidden entirely rather than shown disabled: a button that
	// cannot work is worse than no button, and an iCal-only board is a normal
	// configuration rather than a half-configured one.
	GoogleEnabled bool

	// GoogleAccounts are the connected accounts, each with the calendars it
	// feeds, so disconnecting says what it is about to remove.
	GoogleAccounts []GoogleAccount

	// GoogleError is the most recent device-flow failure. Same problem as
	// WiFiError: the flow resolves long after its request was answered, so this
	// page is the only place left to report it.
	GoogleError string

	// Pairing is the in-flight device-flow code, shown here as well as on the
	// panel -- the user may still be holding the phone they submitted from.
	Pairing PairingCode
}

// GoogleAccount is one connected account and what it feeds.
type GoogleAccount struct {
	Email     string
	Calendars []config.CalendarSource
}

// ICalFeeds are the calendar sources editable as a URL. The settings form
// rebuilds the list from its url fields, so it must render -- and therefore
// save -- only the sources that have one.
func (p pageData) ICalFeeds() []config.CalendarSource {
	if p.Config == nil {
		return nil
	}
	var out []config.CalendarSource
	for _, src := range p.Config.Calendars {
		if src.SourceKind() == config.KindICal {
			out = append(out, src)
		}
	}
	return out
}
