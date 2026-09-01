package view

import (
	"context"
	"image"
	"math"
	"testing"
	"time"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
	"github.com/nathanstitt/luckfox-dashboard/internal/config"
	"github.com/nathanstitt/luckfox-dashboard/internal/fb"
	"github.com/nathanstitt/luckfox-dashboard/internal/model"
	"github.com/nathanstitt/luckfox-dashboard/internal/weather"
	"github.com/nathanstitt/omnidoc/pkg/omnidoc"
)

// leftZoneWidthPx is the width of the left panel (clock, weather, headline).
// #timeline-zone begins after it; nothing belonging to the agenda may paint
// left of this line.
const leftZoneWidthPx = 380

// scrolledVM builds a view model whose agenda is scrolled left -- several
// events already past by the time "now" lands, so model.BuildAgenda resolves a
// negative CardRow.OffsetPx and the template emits a real translateX.
//
// The golden fixture cannot stand in for this: its agenda sits at offset 0,
// where an unclipped row has nothing to overflow with.
func scrolledVM(t *testing.T) model.ViewModel {
	t.Helper()
	now := time.Date(2026, 8, 25, 14, 30, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"

	var evs []calendar.Event
	for i := range 6 {
		start := now.Add(time.Duration(-4+i) * time.Hour)
		evs = append(evs, calendar.Event{
			Title: "Event", Color: "#4f9cff",
			Start: start, End: start.Add(30 * time.Minute),
		})
	}
	w := &weather.Weather{
		Current: weather.Conditions{TempF: 83, Code: 0, Time: now},
		Daily:   []weather.DayPoint{{Date: now, HiF: 100, LoF: 68, Code: 0}},
	}
	for i := range 8 {
		w.Hourly = append(w.Hourly, weather.HourPoint{
			Time: now.Add(time.Duration(i) * time.Hour), TempF: 70 + float64(i),
		})
	}
	return model.Build(now, c, evs, w, nil, nil)
}

// The agenda row is wider than its zone and is pre-scrolled with a negative
// translateX. Without overflow:hidden on #timeline-zone its background paints
// out past the zone's left edge and washes over the entire left panel -- the
// clock, the weather, and the headline all disappear behind it.
//
// Asserting on the left panel rather than on the row is deliberate: the defect
// is that ink lands where it must not, and that is what a viewer sees. It was
// found on the panel and reproduced host-side from the board's own HTML.
func TestAgendaScrollDoesNotPaintOverLeftZone(t *testing.T) {
	vm := scrolledVM(t)
	if vm.Agenda.OffsetPx >= 0 {
		t.Fatalf("fixture does not scroll: OffsetPx = %v, want negative", vm.Agenda.OffsetPx)
	}

	html, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fb.RenderHTML(context.Background(), []byte(html), 1920, 480,
		omnidoc.WithResourceLoader(FontLoader()))
	if err != nil {
		t.Fatal(err)
	}

	// The agenda band's own vertical extent: the ribbon is 40px and the row is
	// 240px. Sample inside it, left of the zone boundary, skipping the columns
	// nearest the edge so a 1px seam is not read as a wash.
	const bandTop, bandBottom = 60, 260
	for y := bandTop; y < bandBottom; y += 20 {
		for x := 20; x < leftZoneWidthPx-20; x += 40 {
			if got, ok := paintedOver(img, x, y); ok {
				t.Fatalf("agenda painted over the left zone at (%d,%d): %v", x, y, got)
			}
		}
	}
}

// paintedOver reports whether the pixel at (x,y) is lighter than the left
// zone's own background can be. That panel is near-black (#07080d over the
// page's own dark fill); the agenda band is #0a0d16 lifted by a blue radial
// gradient, so any wash reads well above this threshold on at least one
// channel. Returns the sampled colour for the failure message.
func paintedOver(img image.Image, x, y int) ([3]uint8, bool) {
	r, g, b, _ := img.At(x, y).RGBA()
	c := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
	// The left zone's darkest content is the background itself; its brightest
	// legitimate content is text, which this sampling grid can land on. Text is
	// light grey/white -- roughly equal channels -- while the wash is blue-cast
	// and dim. Flag only the dim-but-not-black blue case.
	blueCast := c[2] > c[0]+4 && c[2] > 16 && c[2] < 90
	return c, blueCast
}

// The dialog's buttons must actually show their labels.
//
// This engine drops a bare text child of a flex container: the box paints, the
// text does not. A "Hide from panel" button that renders as a blank amber
// rectangle is not a styling nit -- it is an unlabelled control on a panel with
// no other affordance, and every host-side structural check passes while it
// happens. The buttons use line-height centring for this reason; this test is
// what stops someone reaching for flex again.
func TestDialogButtonLabelsAreVisible(t *testing.T) {
	vm := fixtureVM(t)
	e := calendar.Event{
		Title: "Design Review", Color: "#ff7a59",
		Start: vm.Now.Add(time.Hour), End: vm.Now.Add(2 * time.Hour),
	}
	d := model.EventDialog(e, false, false)
	vm.Dialog = &d

	html, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fb.RenderHTML(context.Background(), []byte(html), 1920, 480,
		omnidoc.WithResourceLoader(FontLoader()))
	if err != nil {
		t.Fatal(err)
	}

	// The primary action is amber (#f59e0b) with near-black text. Count the
	// dark pixels inside its rect: a labelled button has glyph ink, an empty
	// one is a flat amber field.
	const (
		btnX, btnY = 380.0 + 44.0, 394.0
		btnW, btnH = 300.0, 64.0
	)
	ink := 0
	for y := int(btnY); y < int(btnY+btnH); y++ {
		for x := int(btnX); x < int(btnX+btnW); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// The label is #1a1204 on amber; anything this dark is glyph.
			if r>>8 < 90 && g>>8 < 80 && b>>8 < 60 {
				ink++
			}
		}
	}
	if ink < 200 {
		t.Errorf("primary button has %d dark pixels; its label is missing "+
			"(a flex container drops bare text in this engine)", ink)
	}
}

// The next-up block's last line must survive a tall headline.
//
// #now-content clips its overflow, and omnidoc does not reserve a column flex
// child's own margin when sizing it -- so .nb-next's margin-top let .nb-now
// grow into space .nb-next had already claimed, pushing "at 10:00 AM" off the
// bottom of the panel. The 18px separator is a zero-flex sibling (.nb-next-gap)
// for that reason, the same workaround .nb-pad uses for the bottom inset.
//
// The stylesheet has warned about this failure in a comment since the block was
// written; the comment did not stop it happening. This does.
func TestNextUpLastLineIsNotClipped(t *testing.T) {
	// A "FREE FOR / 2 hr" headline is the tall case: .nb-free is 52px, which
	// is what squeezes the column.
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	evs := []calendar.Event{
		{Title: "Morning Standup", Color: "#4f9cff",
			Start: now.Add(2 * time.Hour), End: now.Add(2*time.Hour + 30*time.Minute)},
	}
	vm := model.Build(now, c, evs, nil, nil, nil)
	if vm.NowBlock.NextAt == "" {
		t.Fatal("fixture has no next-up line to clip")
	}

	html, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fb.RenderHTML(context.Background(), []byte(html), 1920, 480,
		omnidoc.WithResourceLoader(FontLoader()))
	if err != nil {
		t.Fatal(err)
	}

	// .nb-pad reserves 20px below the last line, so the bottom rows of the
	// panel must be empty. When the column overflows, the text is pushed down
	// into them and #now-content's overflow:hidden cuts it mid-glyph -- so ink
	// here is the clipping, directly.
	//
	// Asserting on the empty margin rather than on the line's own ink is what
	// makes this test discriminate: the block's title line lands in the rows
	// just above whether or not the last line survives, so a band that includes
	// it passes in both layouts. Measured: intact ends at y=470 with nothing
	// below; clipped puts ink at y=476 and y=478.
	const bandTop, bandBottom = 472, 480
	ink := 0
	for y := bandTop; y < bandBottom; y++ {
		for x := 20; x < leftZoneWidthPx-20; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// --text-mid (#6b7a96) on near-black; the threshold sits above the
			// panel's own background and its faint vignette.
			if r>>8 > 45 || g>>8 > 50 || b>>8 > 65 {
				ink++
			}
		}
	}
	if ink > 0 {
		t.Errorf("%d lit pixels in rows %d-%d, which .nb-pad reserves as empty: "+
			"the next-up block has overflowed and its last line is clipped",
			ink, bandTop, bandBottom)
	}
}

// The condition text must not sit on the hairline below it.
//
// The gap comes from #wx-widget's asymmetric padding, not from anything on
// #wx-desc. That row is align-items: center, so its children are centred as a
// unit: height added inside #wx-desc pushes the block down by half of what it
// adds beneath it, and the gap barely changes. Both margin-bottom and
// padding-bottom were tried there and each measured 2px.
//
// So this asserts on rendered pixels rather than on the declaration. A rule
// that looks right in the stylesheet is exactly how this shipped touching in
// the first place.
func TestConditionTextClearsTheHairline(t *testing.T) {
	vm := fixtureVM(t)
	html, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fb.RenderHTML(context.Background(), []byte(html), 1920, 480,
		omnidoc.WithResourceLoader(FontLoader()))
	if err != nil {
		t.Fatal(err)
	}

	// The hairline spans the left panel; glyphs never do. Scan down from the
	// temperature for the first row that is wide enough to be the rule, and
	// remember the last row of ink above it.
	rule, inkBottom := -1, -1
	for y := 100; y < 200; y++ {
		wide, ink := 0, 0
		for x := 5; x < leftZoneWidthPx-5; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 > 30 || g>>8 > 34 || b>>8 > 44 {
				wide++
			}
			if x >= 20 && x < leftZoneWidthPx-20 && r>>8+g>>8+b>>8 > 110 {
				ink++
			}
		}
		if wide > 300 {
			rule = y
			break
		}
		if ink > 0 {
			inkBottom = y
		}
	}
	if rule < 0 || inkBottom < 0 {
		t.Fatalf("could not locate the rule (%d) or the text above it (%d)", rule, inkBottom)
	}

	// 3px is the floor for "not touching" at this size; the rule asks for 6.
	if gap := rule - inkBottom - 1; gap < 3 {
		t.Errorf("condition text ends at y=%d and the hairline is at y=%d: a %dpx gap. "+
			"margin-bottom is swallowed on a flex child here -- use padding-bottom",
			inkBottom, rule, gap)
	}
}

// relLuminance and contrastRatio implement WCAG 2.1 SC 1.4.3.
func relLuminance(r, g, b uint8) float64 {
	f := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b)
}

func contrastRatio(fg, bg [3]uint8) float64 {
	a, b := relLuminance(fg[0], fg[1], fg[2]), relLuminance(bg[0], bg[1], bg[2])
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

// The next-up block must stay readable, measured rather than declared.
//
// It has failed this twice. Originally --text-dim at opacity 0.70 rendered as
// rgb(45,54,72) -- 1.65:1, unreadable on the panel. Lifting the colour to
// --text-mid but leaving opacity 0.85 gave 3.61:1, which still fails AA and
// still looked "fixed" in the stylesheet. Opacity is the trap: it multiplies
// whatever colour is declared, so the rule and the rendering disagree.
//
// 18px is below the 24px large-text threshold, so the bar is 4.5:1.
func TestNextUpBlockMeetsContrastAA(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	c := &config.Config{}
	c.Location.Timezone = "UTC"
	evs := []calendar.Event{
		{Title: "Morning Standup", Color: "#4f9cff",
			Start: now.Add(2 * time.Hour), End: now.Add(2*time.Hour + 30*time.Minute)},
	}
	vm := model.Build(now, c, evs, nil, nil, nil)
	html, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fb.RenderHTML(context.Background(), []byte(html), 1920, 480,
		omnidoc.WithResourceLoader(FontLoader()))
	if err != nil {
		t.Fatal(err)
	}

	// Each line has to be checked on its own. The block's three lines are
	// different colours -- the title is lighter than the lead and the time --
	// so the brightest pixel across the whole band is the title, and taking it
	// alone reports a pass while the other two fail. That is exactly the bug
	// this test exists to catch, so it must not average or maximise over them.
	//
	// Anti-aliased edges are dimmer than the fill, so within a single text line
	// the brightest pixel is the colour actually being asked for.
	bg := [3]uint8{7, 8, 13} // --bg
	type line struct {
		top, bottom int
		peak        [3]uint8
		lum         float64
	}
	var lines []line
	cur := line{top: -1}
	for y := 380; y < 476; y++ {
		var peak [3]uint8
		var lum float64
		for x := 20; x < leftZoneWidthPx-20; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			c := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
			if l := relLuminance(c[0], c[1], c[2]); l > lum {
				lum, peak = l, c
			}
		}
		// The background itself sits at ~0.003; anything above it is glyph.
		if lum > relLuminance(bg[0], bg[1], bg[2])*2 {
			if cur.top < 0 {
				cur = line{top: y, peak: peak, lum: lum}
			} else if lum > cur.lum {
				cur.peak, cur.lum = peak, lum
			}
			cur.bottom = y
		} else if cur.top >= 0 {
			lines = append(lines, cur)
			cur = line{top: -1}
		}
	}
	if cur.top >= 0 {
		lines = append(lines, cur)
	}

	if len(lines) < 3 {
		t.Fatalf("expected the lead, title and time lines, found %d", len(lines))
	}
	for _, l := range lines {
		if got := contrastRatio(l.peak, bg); got < 4.5 {
			t.Errorf("next-up line at y=%d-%d renders rgb%v on rgb%v = %.2f:1, below "+
				"the 4.5:1 WCAG AA floor for 18px text (an opacity on the block "+
				"multiplies whatever colour the rule declares)",
				l.top, l.bottom, l.peak, bg, got)
		}
	}
}

// The NOW label's letters must share a horizontal centre.
//
// text-align does not reach glyphs in this engine's vertical writing-mode, so
// the label is stacked blocks instead -- see the stylesheet. N and O carry 8px
// of ink against W's 12px, so a left-aligned stack is visibly ragged at the
// 2px level this measures.
func TestNowLabelLettersAreCentred(t *testing.T) {
	vm := fixtureVM(t)
	html, err := Render(vm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := fb.RenderHTML(context.Background(), []byte(html), 1920, 480,
		omnidoc.WithResourceLoader(FontLoader()))
	if err != nil {
		t.Fatal(err)
	}

	// Amber glyphs near the bar, excluding the bar's own dense column.
	barX := int(vm.Agenda.NowBarXPx) + leftZoneWidthPx
	type row struct{ lo, hi int }
	var rows []row
	for y := 45; y < 140; y++ {
		lo, hi := 1<<30, -1
		for x := barX + 5; x < barX+34 && x < 1920; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 > 150 && g>>8 > 90 && b>>8 < 90 {
				if x < lo {
					lo = x
				}
				if x > hi {
					hi = x
				}
			}
		}
		if hi >= 0 {
			rows = append(rows, row{lo, hi})
		} else {
			rows = append(rows, row{-1, -1})
		}
	}

	var centres []float64
	inLetter := false
	lo, hi := 1<<30, -1
	for _, r := range rows {
		if r.hi >= 0 {
			inLetter = true
			if r.lo < lo {
				lo = r.lo
			}
			if r.hi > hi {
				hi = r.hi
			}
		} else if inLetter {
			centres = append(centres, float64(lo+hi)/2)
			inLetter, lo, hi = false, 1<<30, -1
		}
	}
	if inLetter {
		centres = append(centres, float64(lo+hi)/2)
	}

	if len(centres) != 3 {
		t.Fatalf("expected 3 letters in the NOW label, found %d (centres %v)", len(centres), centres)
	}
	mn, mx := centres[0], centres[0]
	for _, c := range centres {
		if c < mn {
			mn = c
		}
		if c > mx {
			mx = c
		}
	}
	if spread := mx - mn; spread > 1.0 {
		t.Errorf("NOW letters centre at %v -- a %.1fpx spread, so the stack reads ragged",
			centres, spread)
	}
}
