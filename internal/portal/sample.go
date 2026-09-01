package portal

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// sampleEvent is one entry in the generated feed, offset from the anchor time.
//
// Offsets are relative rather than absolute so the feed is always inside the
// render window: a fixture with hardcoded dates falls out of the window the day
// after it is written, which is exactly how the two public horaro test feeds
// (dated 2016 and 2025) render as an empty timeline.
type sampleEvent struct {
	startMin int // minutes from the anchor; negative is in the past
	durMin   int
	summary  string
	allDay   bool
}

// sampleEvents fills the timeline with the cases that actually stress it.
//
// Timed against the real window (model.defaultWindow): it starts 20 min before
// now (PastContext) and sizes to fit the next 4 upcoming events, floored at
// MinSpan 4h. In practice the 4th upcoming event here ends well inside 4h, so
// the window sits exactly at that floor: now-20m .. now+3h40m. Everything must
// therefore END by +220 min or it is silently dropped from the timeline — the
// window does not grow to accommodate a late event unless fitting the first
// four requires it.
//
// Nothing is in progress. The gap between the elapsed event and the next one
// straddles now, so the left panel leads with FREE FOR (ModeFree: the next
// start is 12 min out, past the 10-minute model.ImminentMinutes threshold) and
// the agenda renders a free-time chip with the NOW bar inside it rather than a
// duration-scaled current slot. Both are states the in-progress fixture could
// not reach, and between them they cover the ModeFree branch and the gap-chip
// nowOffset arithmetic in BuildAgenda.
//
// The 12-minute offset sits deliberately close to the threshold: let the panel
// run and it crosses into the amber countdown (ModeImminent) on its own about
// two minutes later, so one fixture exercises both branches over time.
//
// What each entry is for:
//   - one just elapsed but still visible in the 20-minute past context
//   - a gap containing now, so the NOW bar lands on a free-time chip
//   - an event just outside the imminent threshold, driving the FREE FOR
//     headline that a nearer event would suppress
//   - back-to-back and overlapping blocks, which is what makes labels collide
//     and exercises LabelOpts.MaxRows (2)
//   - a very short block, testing the minBlockPx visibility floor
//   - a long title, testing wrapping and MaxOverflow
//   - an all-day pill, which renders in its own row
var sampleEvents = []sampleEvent{
	{startMin: -35, durMin: 25, summary: "Standup"},      // ended 10m ago, inside PastContext
	{startMin: 12, durMin: 30, summary: "Design Review"}, // just past imminent -> FREE FOR
	{startMin: 45, durMin: 30, summary: "1:1 with Sam"},
	{startMin: 85, durMin: 45, summary: "Sprint Planning"},
	{startMin: 95, durMin: 25, summary: "Vendor Call"}, // overlaps the above -> label collision
	{startMin: 140, durMin: 5, summary: "Standup"},     // very short block
	{startMin: 150, durMin: 45, summary: "Architecture Sync: Rendering Pipeline and Panel Timings"},
	{startMin: 200, durMin: 20, summary: "Focus Block"}, // ends at +220, the window edge
	{startMin: 0, durMin: 0, summary: "Company Holiday", allDay: true},
}

// doneEvents is the same day already finished: everything is in the past, with
// one entry tomorrow so the TOMORROW preview under the mark has something to
// show.
//
// This exists because the end-of-day panel -- the cheers mark and its quote --
// is otherwise only reachable by waiting for a real calendar to run out, which
// on a working board means late evening. Served from /sample.ical?state=done.
//
// The tomorrow entry is +20h rather than a fixed hour so it stays tomorrow
// whatever time the board is asked: at 23:00 a "+9h" event would still be
// today, and the preview would vanish exactly when someone is testing it.
var doneEvents = []sampleEvent{
	{startMin: -480, durMin: 30, summary: "Standup"},
	{startMin: -400, durMin: 60, summary: "Design Review"},
	{startMin: -300, durMin: 30, summary: "1:1 with Sam"},
	{startMin: -180, durMin: 45, summary: "Sprint Planning"},
	{startMin: -90, durMin: 25, summary: "Vendor Call"},
	{startMin: 1200, durMin: 30, summary: "Morning Standup"}, // +20h: tomorrow
	{startMin: 0, durMin: 0, summary: "Company Holiday", allDay: true},
}

// icalTime formats a UTC timestamp in the iCal DATE-TIME form.
func icalTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// sampleICal renders the fixture feed anchored at now.
//
// Written by hand rather than through a library because the output is a fixed
// shape and the dashboard's own parser is the only consumer.
func sampleICal(now time.Time) string { return sampleICalFor(now, sampleEvents) }

// sampleICalFor renders an arbitrary fixture, so a caller can pick which state
// the panel should land in.
func sampleICalFor(now time.Time, events []sampleEvent) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//upnext//luckfox sample//EN\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")
	b.WriteString("X-WR-CALNAME:Sample\r\n")

	for i, e := range events {
		b.WriteString("BEGIN:VEVENT\r\n")
		fmt.Fprintf(&b, "UID:upnext-sample-%d@localhost\r\n", i)
		fmt.Fprintf(&b, "DTSTAMP:%s\r\n", icalTime(now))
		if e.allDay {
			// DATE values are exclusive at the end, so DTEND is the next day.
			day := now.UTC().Format("20060102")
			next := now.UTC().Add(24 * time.Hour).Format("20060102")
			fmt.Fprintf(&b, "DTSTART;VALUE=DATE:%s\r\n", day)
			fmt.Fprintf(&b, "DTEND;VALUE=DATE:%s\r\n", next)
		} else {
			start := now.Add(time.Duration(e.startMin) * time.Minute)
			fmt.Fprintf(&b, "DTSTART:%s\r\n", icalTime(start))
			fmt.Fprintf(&b, "DTEND:%s\r\n", icalTime(start.Add(time.Duration(e.durMin)*time.Minute)))
		}
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", e.summary)
		b.WriteString("END:VEVENT\r\n")
	}

	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// now is the server's clock, defaulting to time.Now.
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// handleSampleICal serves a generated iCal feed for design work, so the panel
// has events on it without a real calendar account.
//
// Deliberately unauthenticated: the dashboard's own calendar fetcher requests
// this over the loopback interface and does not send credentials, the same
// reason /portal.css is open. It exposes no configuration and no user data —
// only a fixture it just generated.
func (s *Server) handleSampleICal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	// Regenerated per request against the clock, so never cache it.
	w.Header().Set("Cache-Control", "no-store")

	// ?state=done serves a day that has already finished, which is the only
	// way to reach the end-of-day panel without waiting for a real calendar to
	// run out. Any other value serves the normal fixture, so a typo shows the
	// usual timeline rather than an error nobody is watching for.
	events := sampleEvents
	if r.URL.Query().Get("state") == "done" {
		events = doneEvents
	}
	fmt.Fprint(w, sampleICalFor(s.now(), events))
}
