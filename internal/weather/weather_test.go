package weather

import (
	"os"
	"testing"
	"time"
)

func TestParseOpenMeteo(t *testing.T) {
	b, err := os.ReadFile("testdata/openmeteo.json")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseOpenMeteo(b, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if w.Current.TempF == 0 {
		t.Error("Current.TempF is zero; expected a parsed temperature")
	}
	if len(w.Hourly) == 0 {
		t.Fatal("Hourly is empty")
	}
	if len(w.Daily) != 7 {
		t.Errorf("len(Daily) = %d, want 7", len(w.Daily))
	}
	// Hourly must be chronological — the chart depends on ordering.
	for i := 1; i < len(w.Hourly); i++ {
		if !w.Hourly[i].Time.After(w.Hourly[i-1].Time) {
			t.Fatalf("Hourly not ascending at %d: %v then %v",
				i, w.Hourly[i-1].Time, w.Hourly[i].Time)
		}
	}
	for i, d := range w.Daily {
		if d.HiF < d.LoF {
			t.Errorf("Daily[%d]: HiF %.1f < LoF %.1f", i, d.HiF, d.LoF)
		}
	}

	// Spot checks: pin specific indices to fixture values to catch off-by-one bugs.
	// Hourly index 1: 2026-08-25T01:00, 66.3°F, 1% precip, code 0
	expectedHour1 := HourPoint{
		Time:       time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC),
		TempF:      66.3,
		PrecipProb: 1,
		Code:       0,
	}
	if h := w.Hourly[1]; h.Time != expectedHour1.Time || h.TempF != expectedHour1.TempF ||
		h.PrecipProb != expectedHour1.PrecipProb || h.Code != expectedHour1.Code {
		t.Errorf("Hourly[1] = %+v, want %+v", h, expectedHour1)
	}

	// Hourly index 5: 2026-08-25T05:00, 65.8°F, 1% precip, code 0
	expectedHour5 := HourPoint{
		Time:       time.Date(2026, 8, 25, 5, 0, 0, 0, time.UTC),
		TempF:      65.8,
		PrecipProb: 1,
		Code:       0,
	}
	if h := w.Hourly[5]; h.Time != expectedHour5.Time || h.TempF != expectedHour5.TempF ||
		h.PrecipProb != expectedHour5.PrecipProb || h.Code != expectedHour5.Code {
		t.Errorf("Hourly[5] = %+v, want %+v", h, expectedHour5)
	}

	// Daily index 1: 2026-08-26, high 90.9°F, low 70.3°F, 8% precip, code 3
	expectedDay1 := DayPoint{
		Date:       time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		HiF:        90.9,
		LoF:        70.3,
		PrecipProb: 8,
		Code:       3,
	}
	if d := w.Daily[1]; d.Date != expectedDay1.Date || d.HiF != expectedDay1.HiF ||
		d.LoF != expectedDay1.LoF || d.PrecipProb != expectedDay1.PrecipProb || d.Code != expectedDay1.Code {
		t.Errorf("Daily[1] = %+v, want %+v", d, expectedDay1)
	}

	// Daily index 2: 2026-08-27, high 90.9°F, low 64.6°F, 1% precip, code 3
	expectedDay2 := DayPoint{
		Date:       time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
		HiF:        90.9,
		LoF:        64.6,
		PrecipProb: 1,
		Code:       3,
	}
	if d := w.Daily[2]; d.Date != expectedDay2.Date || d.HiF != expectedDay2.HiF ||
		d.LoF != expectedDay2.LoF || d.PrecipProb != expectedDay2.PrecipProb || d.Code != expectedDay2.Code {
		t.Errorf("Daily[2] = %+v, want %+v", d, expectedDay2)
	}
}

func TestParseOpenMeteoRejectsGarbage(t *testing.T) {
	if _, err := ParseOpenMeteo([]byte(`{"nope":true}`), time.UTC); err == nil {
		t.Fatal("expected error when hourly data is absent, got nil")
	}
}

func TestParseOpenMeteoRejectsShortSiblingArray(t *testing.T) {
	// Simulate a degraded response where temperature_2m is shorter than time.
	jsonShortTemp := `{
		"current":{"time":"2026-08-25T16:30","temperature_2m":87.0,"weather_code":2,"is_day":1},
		"hourly":{
			"time":["2026-08-25T00:00","2026-08-25T01:00"],
			"temperature_2m":[66.4],
			"precipitation_probability":[0,1],
			"weather_code":[0,0]
		},
		"daily":{
			"time":["2026-08-25"],
			"temperature_2m_max":[87.0],
			"temperature_2m_min":[65.4],
			"precipitation_probability_max":[6],
			"weather_code":[3]
		}
	}`
	_, err := ParseOpenMeteo([]byte(jsonShortTemp), time.UTC)
	if err == nil {
		t.Fatal("expected error for mismatched hourly arrays, got nil")
	}
	if err.Error() != `open-meteo response: field "temperature_2m" has 1 entries, time has 2` {
		t.Errorf("error message = %q, want field mismatch message", err.Error())
	}
}

func TestParseOpenMeteoRejectsMalformedTimestamp(t *testing.T) {
	// Timestamp that matches neither layout.
	jsonBadTime := `{
		"current":{"time":"2026-08-25T16:30","temperature_2m":87.0,"weather_code":2,"is_day":1},
		"hourly":{
			"time":["not-a-timestamp"],
			"temperature_2m":[66.4],
			"precipitation_probability":[0],
			"weather_code":[0]
		},
		"daily":{
			"time":["2026-08-25"],
			"temperature_2m_max":[87.0],
			"temperature_2m_min":[65.4],
			"precipitation_probability_max":[6],
			"weather_code":[3]
		}
	}`
	_, err := ParseOpenMeteo([]byte(jsonBadTime), time.UTC)
	if err == nil {
		t.Fatal("expected error for malformed timestamp, got nil")
	}
}
