package view

import (
	"context"
	"image"
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
