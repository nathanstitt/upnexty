package model

import (
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// An imminent event leads with the countdown, not the title.
//
// The panel answers "can I start something right now"; "in 4 min" is that
// answer and the meeting's name is the detail. It also matches ModeFree's
// shape -- both put a duration in the headline -- so the two states the user
// is most often in do not swap which line is large every few minutes. The
// colour is what separates them.
func TestImminentLeadsWithTheCountdown(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	evs := []calendar.Event{
		{Title: "Design Review", Color: "#ff7a59",
			Start: now.Add(4 * time.Minute), End: now.Add(34 * time.Minute)},
	}
	nb := BuildNowBlock(evs, now, false)

	if nb.Mode != ModeImminent {
		t.Fatalf("Mode = %v, want ModeImminent", nb.Mode)
	}
	if !nb.BigIsDuration {
		t.Error("BigIsDuration = false; the headline should be the countdown")
	}
	if !nb.BigUrgent {
		t.Error("BigUrgent = false; an imminent countdown is amber, not the green of free time")
	}
	if nb.Big != "4 min" {
		t.Errorf("Big = %q, want the countdown %q", nb.Big, "4 min")
	}
	if nb.Sub != "Design Review" {
		t.Errorf("Sub = %q, want the event title", nb.Sub)
	}
	if nb.Accent != "#ff7a59" {
		t.Errorf("Accent = %q, want the event colour for the bar beside the title", nb.Accent)
	}
}

// Free time and an imminent start share a shape and differ by colour.
func TestFreeAndImminentShareTheHeadlineShape(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	free := BuildNowBlock([]calendar.Event{
		{Title: "Later", Start: now.Add(40 * time.Minute), End: now.Add(70 * time.Minute)},
	}, now, false)
	soon := BuildNowBlock([]calendar.Event{
		{Title: "Soon", Start: now.Add(4 * time.Minute), End: now.Add(34 * time.Minute)},
	}, now, false)

	if !free.BigIsDuration || !soon.BigIsDuration {
		t.Error("both states should put a duration in the headline")
	}
	if free.BigUrgent {
		t.Error("free time is not urgent; it should stay green")
	}
	if !soon.BigUrgent {
		t.Error("an imminent start should be amber")
	}
}
