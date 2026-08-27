package portal

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/wifi"
)

// page renders the settings page with an optional error message.
func (s *Server) page(w http.ResponseWriter, errMsg string, code int) {
	cfg := s.Store.Config()
	data := pageData{
		Config:          cfg,
		APName:          "upnext-setup",
		DefaultPassword: DefaultPassword(s.MAC),
		Error:           errMsg,
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
func (s *Server) save(mutate func(*config.Config) error) error {
	cur := s.Store.Config()
	next := *cur // shallow copy is enough; slices are replaced wholesale below
	if err := mutate(&next); err != nil {
		return err
	}
	if err := next.Save(s.ConfigPath); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}
	s.Store.SetConfig(&next)
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

	var feeds []config.CalendarSource
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
		feeds = append(feeds, config.CalendarSource{
			Name:  name,
			Color: calendarColor(len(feeds)),
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
	// redirect is sent first and the reconnect happens after.
	if s.WiFi != nil {
		go func() {
			_ = s.WiFi.Connect(ssid, pw)
		}()
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// calendarColor assigns feed colours from the dashboard's palette in order, so
// a user never has to pick a hex value.
func calendarColor(i int) string {
	palette := []string{"#4f9cff", "#ff7a59", "#8b97ab", "#e8ecf3"}
	return palette[i%len(palette)]
}

// applyBrightness writes the panel's sysfs control. A failure is not fatal --
// the value is saved either way and takes effect on the next boot.
func applyBrightness(v int) {
	_ = os.WriteFile("/sys/class/backlight/waveshare_bl/brightness",
		[]byte(strconv.Itoa(v)), 0o644)
}
