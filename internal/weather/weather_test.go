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
}

func TestParseOpenMeteoRejectsGarbage(t *testing.T) {
	if _, err := ParseOpenMeteo([]byte(`{"nope":true}`), time.UTC); err == nil {
		t.Fatal("expected error when hourly data is absent, got nil")
	}
}
