package portal

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nathanstitt/upnexty/internal/config"
	"github.com/nathanstitt/upnexty/internal/wifi"
)

// page renders the settings page with an optional error message.
func (s *Server) page(w http.ResponseWriter, errMsg string, code int) {
	cfg := s.Store.Config()
	data := pageData{
		Config:          cfg,
		APName:          "upnext-setup",
		DefaultPassword: DefaultPassword(s.MAC),
		Error:           errMsg,
		WiFiError:       s.getWiFiErr(),
		Hostname:        s.liveHostname(),
	}
	if s.WiFi != nil {
		if st, err := s.WiFi.Status(); err == nil {
			data.Status = st
		}
	}
	if s.MAC != "" {
		data.APName = wifi.APName(s.MAC)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		// Headers are already sent; log-and-stop is all that is left.
		fmt.Fprintf(w, "\n<!-- render error: %v -->", err)
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.page(w, "", http.StatusOK)
}

// save persists a mutated copy and installs it. Never mutates the live config:
// readers hold that pointer for a whole tick.
//
// Delegates the whole copy-mutate-save-install sequence to Store.Update so it
// runs under a single lock -- two concurrent POSTs to different sections
// (e.g. /save/wifi and /save/display) must not each read the same starting
// config and have the second one overwrite the first's change wholesale, in
// memory and on disk, while both requests see a 303 success.
func (s *Server) save(mutate func(*config.Config) error) error {
	if err := s.Store.Update(mutate); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}
	return nil
}

func (s *Server) handleSaveDisplay(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	b, err := strconv.Atoi(r.FormValue("brightness"))
	if err != nil || b < 0 || b > 255 {
		s.page(w, "Brightness must be a number between 0 and 255.", http.StatusBadRequest)
		return
	}
	temp := r.FormValue("temperature")
	if temp != "fahrenheit" && temp != "celsius" {
		temp = "fahrenheit"
	}
	clock := r.FormValue("clock_24h") != ""

	if err := s.save(func(c *config.Config) error {
		c.Display.Brightness = &b
		c.Units.Clock24h = clock
		c.Units.Temperature = temp
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	applyBrightness(b)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleSaveHostname sets the name the board answers to and announces.
//
// The name is normalized before validation so the stored value is the one that
// will actually be used -- "UpNext" and "upnext" are the same host, and saving
// the former would show the user a name the network never sees.
//
// Unlike brightness, a failure to apply is surfaced rather than swallowed: see
// ApplyHostname. The config is still saved in that case, so the name takes
// effect at the next boot and the error explains why it has not yet.
func (s *Server) handleSaveHostname(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	name := config.NormalizeHostname(r.FormValue("hostname"))
	if err := config.ValidateHostname(name); err != nil {
		s.page(w, capitalize(err.Error())+".", http.StatusBadRequest)
		return
	}

	if err := s.save(func(c *config.Config) error {
		c.Device.Hostname = name
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := ApplyHostname(s.etcHostname(), s.procHostname(), name); err != nil {
		log.Printf("portal: hostname saved as %q but not applied: %v", name, err)
		s.page(w, fmt.Sprintf("Saved %q, but it could not be applied right now. "+
			"It will take effect after a reboot.", name), http.StatusInternalServerError)
		return
	}
	// The DHCP lease carries the old name until it is renewed, so the router's
	// DNS entry does not change until the board reassociates. Saying so is
	// better than leaving the user to wonder why the new name does not resolve.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// liveHostname is the name the kernel is using, read through the override path
// so a test can set it. Falls back to os.Hostname, and to "" if even that
// fails -- the placeholder is a convenience, not something worth an error page.
func (s *Server) liveHostname() string {
	if b, err := os.ReadFile(s.procHostname()); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" {
			return n
		}
	}
	n, err := os.Hostname()
	if err != nil {
		return ""
	}
	return n
}

// etcHostname and procHostname resolve the override fields to real paths.
func (s *Server) etcHostname() string {
	if s.EtcHostname != "" {
		return s.EtcHostname
	}
	return EtcHostnamePath
}

func (s *Server) procHostname() string {
	if s.ProcHostname != "" {
		return s.ProcHostname
	}
	return ProcHostnamePath
}

// capitalize upper-cases the first letter, so a lower-case error string reads
// as a sentence on the settings page.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) handleSavePlace(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("latitude")), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("longitude")), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		s.page(w, "Enter a latitude between -90 and 90, and a longitude between -180 and 180.", http.StatusBadRequest)
		return
	}
	tz := strings.TrimSpace(r.FormValue("timezone"))
	if _, err := time.LoadLocation(tz); err != nil {
		s.page(w, fmt.Sprintf("%q is not a time zone this device knows. Use a name like America/Chicago.", tz), http.StatusBadRequest)
		return
	}
	if err := s.save(func(c *config.Config) error {
		c.Location.Latitude = lat
		c.Location.Longitude = lon
		c.Location.Timezone = tz
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSaveCalendars(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	names := r.Form["name"]
	urls := r.Form["url"]

	// The form carries only name and url, so colors must be preserved from the
	// stored config rather than re-derived positionally -- otherwise deleting
	// one feed silently recolors every feed after it.
	prev := map[string]string{}
	for _, c := range s.Store.Config().Calendars {
		prev[c.URL] = c.Color
	}

	var feeds []config.CalendarSource
	taken := map[string]bool{}
	for i := range urls {
		u := strings.TrimSpace(urls[i])
		if u == "" {
			continue // an empty row is a deletion, or the unused "add" row
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "webcal://") {
			s.page(w, "A calendar address should start with https:// or webcal://.", http.StatusBadRequest)
			return
		}
		u = strings.Replace(u, "webcal://", "https://", 1)
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		if name == "" {
			name = "Calendar"
		}
		color := prev[u]
		if color == "" {
			color = nextColor(taken)
		}
		taken[color] = true
		feeds = append(feeds, config.CalendarSource{
			Name:  name,
			Color: color,
			URL:   u,
		})
	}
	if err := s.save(func(c *config.Config) error {
		c.Calendars = feeds
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-read the feeds now. Without this the panel keeps showing events from
	// the URL that was just replaced until the next interval comes round --
	// up to CalendarMinutes, ten by default -- which reads as the save having
	// done nothing. The store also drops back to "pending", so the panel says
	// "Fetching..." rather than displaying the old feed's events in the
	// meantime.
	if rf, ok := s.Store.(CalendarRefetcher); ok {
		rf.RefetchCalendars()
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSavePassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	if pw == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther) // blank means "keep current"
		return
	}
	if len(pw) < 6 {
		s.page(w, "Use at least 6 characters.", http.StatusBadRequest)
		return
	}
	if err := s.save(func(c *config.Config) error {
		c.Portal.PasswordHash = HashPassword(pw)
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSaveWiFi(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	ssid := strings.TrimSpace(r.FormValue("ssid"))
	pw := strings.TrimSpace(r.FormValue("password"))
	if ssid == "" {
		s.page(w, "Enter the name of the network to join.", http.StatusBadRequest)
		return
	}
	cur := s.Store.Config()
	if pw == "" {
		pw = cur.WiFi.Password // blank means "keep current"
	}
	if err := s.save(func(c *config.Config) error {
		c.WiFi.SSID = ssid
		c.WiFi.Password = pw
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Associating drops the connection this request arrived on, so the
	// redirect is sent first and the reconnect happens after -- this cannot
	// be made synchronous without blocking the response on tearing down the
	// very AP the browser is talking to.
	if s.WiFi != nil {
		// Single-flight: if a previous submit's connect attempt is still
		// running, don't stack a second `S99wlan0 restart` on top of it --
		// concurrent restarts can kill each other's supplicant (see
		// Server.connecting's doc comment). The config is still saved above
		// either way; only the reconnect attempt is skipped.
		if s.connecting.CompareAndSwap(false, true) {
			// Publish before the goroutine starts so the very next render can
			// show it -- a frame takes ~10s on this hardware, so a late write
			// would miss the first one.
			s.pendingSSID.Store(ssid)
			go func() {
				defer s.connecting.Store(false)
				if err := s.WiFi.Connect(ssid, pw); err != nil {
					// Connect runs after the response is already sent, so this
					// is the only place the failure can still be reported: at
					// minimum in the log, and on the settings page for the
					// next load. Without this, a bad password or a write
					// failure (read-only rootfs, ENOSPC) leaves config.json
					// pointing at a network the board never actually joined,
					// with no signal to the user that re-entering the same
					// credentials will fail the same way.
					log.Printf("portal: wifi connect to %q failed: %v", ssid, err)
					s.setWiFiErr(fmt.Errorf("could not connect to %q: %w", ssid, err))
				} else {
					s.setWiFiErr(nil)
				}
			}()
		}
	}
	// The pending page rather than a redirect to "/". A redirect lands back on
	// the settings form, which looks like the submit did nothing -- and the
	// association is about to tear down the very AP this browser is talking to,
	// so any later navigation will fail. Saying that up front is the difference
	// between "it broke" and "it is working, and here is what happens next".
	s.pendingPage(w, ssid)
}

// loginPage renders the password prompt. next is the path to return to once
// the password is accepted, so a user who aimed at a specific section (or whose
// session expired mid-edit) is not dumped back at the top.
func (s *Server) loginPage(w http.ResponseWriter, errMsg, next string) {
	if !strings.HasPrefix(next, "/") {
		next = "/"
	}
	data := pageData{
		Config:          s.Store.Config(),
		APName:          "upnext-setup",
		DefaultPassword: DefaultPassword(s.MAC),
		Error:           errMsg,
		Next:            next,
	}
	if s.WiFi != nil {
		if st, err := s.WiFi.Status(); err == nil {
			data.Status = st
		}
	}
	if s.MAC != "" {
		data.APName = wifi.APName(s.MAC)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 200, not 401: a captive-portal sheet renders a 401 body as an error page
	// rather than a document, which is what made the Basic-auth challenge show
	// up blank. The page IS the response to "you need to log in".
	w.WriteHeader(http.StatusOK)
	if err := tmpl.ExecuteTemplate(w, "login", data); err != nil {
		log.Printf("portal: render login: %v", err)
	}
}

// pendingPage renders the "update pending" screen shown after a WiFi save.
func (s *Server) pendingPage(w http.ResponseWriter, ssid string) {
	data := pageData{
		Config:      s.Store.Config(),
		APName:      "upnext-setup",
		PendingSSID: ssid,
	}
	if s.MAC != "" {
		data.APName = wifi.APName(s.MAC)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := tmpl.ExecuteTemplate(w, "pending", data); err != nil {
		log.Printf("portal: render pending: %v", err)
	}
}

// calendarPalette assigns feed colours from the dashboard's palette, so a user
// never has to pick a hex value. #ff7a59 (the alarm colour) is deliberately
// excluded -- that hue is reserved for errors, and handing it to an ordinary
// calendar feed would make routine events look like an alert.
var calendarPalette = []string{"#4f9cff", "#8b97ab", "#e8ecf3"}

// nextColor returns a palette colour not already in use. Colours carried
// forward from the stored config are unknown to a positional index, so
// assigning by position alone can hand a new feed the same colour as an
// existing one -- which defeats the point of colouring feeds at all.
func nextColor(taken map[string]bool) string {
	for _, c := range calendarPalette {
		if !taken[c] {
			return c
		}
	}
	// More feeds than colours: reuse in order rather than leaving one blank.
	return calendarPalette[len(taken)%len(calendarPalette)]
}

// applyBrightness writes the panel's sysfs control. A failure is not fatal --
// the value is saved either way and takes effect on the next boot.
func applyBrightness(v int) {
	_ = os.WriteFile("/sys/class/backlight/waveshare_bl/brightness",
		[]byte(strconv.Itoa(v)), 0o644)
}

// Where the hostname lives on the board. Both are needed and they do different
// jobs: /proc is the running kernel's name, which changes immediately and is
// what os.Hostname reads; /etc is what the rootfs restores at boot.
const (
	ProcHostnamePath = "/proc/sys/kernel/hostname"
	EtcHostnamePath  = "/etc/hostname"
)

// ApplyHostname sets the running hostname and records it for the next boot.
//
// The paths are parameters so this is testable without being root -- the same
// reason applyStartupBrightness takes one. Callers on the board pass
// ProcHostnamePath and EtcHostnamePath.
//
// Errors are returned rather than swallowed, unlike applyBrightness. Brightness
// is cosmetic and self-evident -- if the screen does not dim you can see that.
// A hostname that failed to apply is invisible: the settings page would show
// the new name, the config would hold it, and the board would keep answering to
// the old one with no indication anywhere. The caller decides what to do, but
// it must at least be told.
//
// Writing /proc alone would be lost on reboot; writing /etc alone would leave
// the running system on the old name until then. Neither half is worth having
// without the other, so a failure on either is reported -- but /etc is written
// first, because a name that survives a reboot is the more important of the two
// and should not be skipped just because the running kernel refused.
func ApplyHostname(etcPath, procPath, name string) error {
	// Trailing newline: /etc/hostname is read by shell scripts at boot, and the
	// image's own file has one.
	if err := os.WriteFile(etcPath, []byte(name+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", etcPath, err)
	}
	// No newline here -- this is the kernel's value, not a text file, and a
	// trailing newline becomes part of the hostname.
	if err := os.WriteFile(procPath, []byte(name), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", procPath, err)
	}
	return nil
}

// handleUnmute restores an event hidden by a tap on the panel.
//
// Unmuting is the only thing this page can do to the muted list -- there is no
// way to mute from here. Muting is a decision made while looking at the event
// on the panel, and offering it in a settings form would mean picking an event
// out of a list of every occurrence in the next week, which is a worse version
// of the same action.
func (s *Server) handleUnmute(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	key := r.FormValue("key")
	if key == "" {
		s.page(w, "That event could not be identified. Reload and try again.", http.StatusBadRequest)
		return
	}
	if err := s.save(func(c *config.Config) error {
		// Replace the slice rather than mutating it: Store.Update copies the
		// config shallowly, so the backing array is shared with the snapshot
		// readers are already holding.
		c.Muted = append([]config.MutedEvent(nil), c.Muted...)
		c.Unmute(key)
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleGoogleConnect starts the OAuth device flow.
//
// The response redirects immediately and the flow continues in the background,
// for the same reason handleSaveWiFi does: the user has to read a code off the
// panel, walk to another device, sign in, and consent. Google's own parameters
// say how long that is allowed to take -- a 5s poll interval over a 30-minute
// window -- so blocking the request on it is not an option.
//
// The code therefore reaches the user through the PANEL rather than this
// response. See portal.PairingCode.
func (s *Server) handleGoogleConnect(w http.ResponseWriter, r *http.Request) {
	if s.Google == nil {
		s.page(w, "This board was built without Google credentials.", http.StatusBadRequest)
		return
	}
	// Single-flight, exactly as for WiFi: a second flow would mint a second
	// code, and the panel has one place to show one. The user resubmitting
	// because nothing has appeared yet is the expected way to reach this.
	if !s.pairing.CompareAndSwap(false, true) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Ask for the code synchronously. It is one fast request, and doing it here
	// means a failure (no network, bad credentials) is reported on the page the
	// user is looking at rather than only on the panel.
	code, handle, err := s.Google.StartDeviceFlow(r.Context())
	if err != nil {
		s.pairing.Store(false)
		s.setGoogleErr(err)
		s.page(w, "Could not start Google sign-in: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Publish before starting the goroutine so the very next render shows it.
	// A frame takes ~10s on this hardware, so a late write misses one entirely
	// -- the same ordering handleSaveWiFi documents for pendingSSID.
	s.pairingCode.Store(code)
	s.setGoogleErr(nil)

	go func() {
		defer func() {
			s.pairing.Store(false)
			s.pairingCode.Store(PairingCode{})
		}()
		// Not r.Context(): that is cancelled the moment this response is
		// written, which would kill the flow before the user has read the code
		// off the panel. The bound is the device code's own lifetime.
		ctx, cancel := context.WithTimeout(context.Background(), deviceFlowWindow)
		defer cancel()

		account, err := s.Google.AwaitToken(ctx, handle)
		s.setGoogleErr(err)
		if err != nil {
			return
		}
		// A newly connected account is useless until something fetches with
		// it, and the next scheduled fetch may be ten minutes away.
		if rf, ok := s.Store.(CalendarRefetcher); ok {
			rf.RefetchCalendars()
		}
		_ = account
	}()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// deviceFlowWindow bounds the background poll. Google expires a device code
// after 30 minutes; a little longer here means the poll loop sees Google's own
// expired_token response and reports something specific, rather than this
// timeout firing first and reporting a generic cancellation.
const deviceFlowWindow = 35 * time.Minute

// handleGoogleDisconnect deletes one account's token.
func (s *Server) handleGoogleDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.Google == nil {
		s.page(w, "This board was built without Google credentials.", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.page(w, "That form could not be read. Try again.", http.StatusBadRequest)
		return
	}
	account := strings.TrimSpace(r.FormValue("account"))
	if account == "" {
		s.page(w, "Which account should be disconnected?", http.StatusBadRequest)
		return
	}
	if err := s.Google.Disconnect(account); err != nil {
		s.page(w, "Could not disconnect "+account+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Drop the calendars that account fed. Leaving them would point every
	// fetch at a token that no longer exists, so the row would go stale behind
	// an error the user has already acted on by disconnecting.
	if err := s.save(func(c *config.Config) error {
		kept := c.Calendars[:0]
		for _, src := range c.Calendars {
			if src.SourceKind() == config.KindGoogle && src.Account == account {
				continue
			}
			kept = append(kept, src)
		}
		c.Calendars = kept
		return nil
	}); err != nil {
		s.page(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.setGoogleErr(nil)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
