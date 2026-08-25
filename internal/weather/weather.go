// Package weather fetches and normalizes forecast data from Open-Meteo.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/config"
)

const openMeteoURL = "https://api.open-meteo.com/v1/forecast"

// Conditions is the current observation.
type Conditions struct {
	Time   time.Time
	TempF  float64
	Code   int // WMO weather code
	IsDay  bool
}

// HourPoint is one hourly forecast sample.
type HourPoint struct {
	Time       time.Time
	TempF      float64
	PrecipProb int
	Code       int
}

// DayPoint is one daily forecast summary.
type DayPoint struct {
	Date       time.Time
	HiF, LoF   float64
	PrecipProb int
	Code       int
}

// Weather is the normalized forecast the rest of the app consumes.
type Weather struct {
	Current Conditions
	Hourly  []HourPoint
	Daily   []DayPoint
}

type omResponse struct {
	Current struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		WeatherCode int     `json:"weather_code"`
		IsDay       int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time          []string  `json:"time"`
		Temperature   []float64 `json:"temperature_2m"`
		PrecipProb    []int     `json:"precipitation_probability"`
		WeatherCode   []int     `json:"weather_code"`
	} `json:"hourly"`
	Daily struct {
		Time           []string  `json:"time"`
		TempMax        []float64 `json:"temperature_2m_max"`
		TempMin        []float64 `json:"temperature_2m_min"`
		PrecipProbMax  []int     `json:"precipitation_probability_max"`
		WeatherCode    []int     `json:"weather_code"`
	} `json:"daily"`
}

// ParseOpenMeteo normalizes an Open-Meteo response. Timestamps are local-naive
// (no zone suffix), so they are interpreted in loc.
func ParseOpenMeteo(body []byte, loc *time.Location) (*Weather, error) {
	var r omResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse open-meteo: %w", err)
	}
	if len(r.Hourly.Time) == 0 {
		return nil, fmt.Errorf("open-meteo response has no hourly data")
	}

	w := &Weather{}
	w.Current = Conditions{
		Time:  parseLocal(r.Current.Time, loc),
		TempF: r.Current.Temperature,
		Code:  r.Current.WeatherCode,
		IsDay: r.Current.IsDay == 1,
	}
	for i, ts := range r.Hourly.Time {
		p := HourPoint{Time: parseLocal(ts, loc)}
		if i < len(r.Hourly.Temperature) {
			p.TempF = r.Hourly.Temperature[i]
		}
		if i < len(r.Hourly.PrecipProb) {
			p.PrecipProb = r.Hourly.PrecipProb[i]
		}
		if i < len(r.Hourly.WeatherCode) {
			p.Code = r.Hourly.WeatherCode[i]
		}
		w.Hourly = append(w.Hourly, p)
	}
	for i, ts := range r.Daily.Time {
		d := DayPoint{Date: parseLocal(ts, loc)}
		if i < len(r.Daily.TempMax) {
			d.HiF = r.Daily.TempMax[i]
		}
		if i < len(r.Daily.TempMin) {
			d.LoF = r.Daily.TempMin[i]
		}
		if i < len(r.Daily.PrecipProbMax) {
			d.PrecipProb = r.Daily.PrecipProbMax[i]
		}
		if i < len(r.Daily.WeatherCode) {
			d.Code = r.Daily.WeatherCode[i]
		}
		w.Daily = append(w.Daily, d)
	}
	return w, nil
}

// parseLocal handles both "2006-01-02T15:04" and "2006-01-02" forms.
func parseLocal(s string, loc *time.Location) time.Time {
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Fetch retrieves the current forecast for the configured location.
func Fetch(ctx context.Context, c *config.Config) (*Weather, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%g", c.Location.Latitude))
	q.Set("longitude", fmt.Sprintf("%g", c.Location.Longitude))
	q.Set("current", "temperature_2m,weather_code,is_day")
	q.Set("hourly", "temperature_2m,precipitation_probability,weather_code")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_probability_max,weather_code")
	q.Set("temperature_unit", c.Units.Temperature)
	q.Set("timezone", c.Location.Timezone)
	q.Set("forecast_days", "7")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openMeteoURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ParseOpenMeteo(body, c.TimeLocation())
}
