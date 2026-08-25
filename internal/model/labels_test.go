package model

import (
	"testing"

	"github.com/nathanstitt/luckfox-dashboard/internal/calendar"
)

// fixed returns a measure func where every character is w px wide.
func fixed(w float64) func(string) float64 {
	return func(s string) float64 { return float64(len(s)) * w }
}

func blk(title string, x, wpx float64) Block {
	return Block{Event: calendar.Event{Title: title}, X: x, W: wpx}
}

func defaultOpts() LabelOpts {
	return LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 60, TrackWidth: 1000, MaxOverflow: 0}
}

func TestPlaceLabelsKeepsWellSpacedLabelsOnRowZero(t *testing.T) {
	bs := []Block{blk("aaa", 0, 100), blk("bbb", 400, 100), blk("ccc", 800, 100)}
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if len(ls) != 3 {
		t.Fatalf("len = %d, want 3", len(ls))
	}
	for i, l := range ls {
		if l.Row != 0 {
			t.Errorf("label %d Row = %d, want 0 (no crowding)", i, l.Row)
		}
		if l.X != bs[i].X {
			t.Errorf("label %d X = %v, want anchor %v", i, l.X, bs[i].X)
		}
	}
}

func TestPlaceLabelsPushesOverlappingLabelRight(t *testing.T) {
	// "aaaaa" is 50px wide at x=0; the next block starts at 30 — overlap.
	bs := []Block{blk("aaaaa", 0, 20), blk("bbbbb", 30, 20)}
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if ls[1].Row != 0 {
		t.Fatalf("second label Row = %d, want 0 (small push, no demotion)", ls[1].Row)
	}
	wantMin := ls[0].X + ls[0].W + 8 // Gap
	if ls[1].X < wantMin {
		t.Errorf("second label X = %v, want >= %v (pushed clear)", ls[1].X, wantMin)
	}
}

func TestPlaceLabelsDemotesWhenDriftExceedsMax(t *testing.T) {
	// A long label followed immediately by another forces a >MaxDrift push.
	bs := []Block{blk("aaaaaaaaaaaaaaaaaaaa", 0, 10), blk("bbb", 5, 10)}
	opts := defaultOpts()
	opts.MaxDrift = 20
	ls := PlaceLabels(bs, fixed(10), opts)
	if ls[1].Row != 1 {
		t.Errorf("second label Row = %d, want 1 (drift exceeded MaxDrift)", ls[1].Row)
	}
	if ls[1].X != 5 {
		t.Errorf("demoted label X = %v, want its anchor 5", ls[1].X)
	}
}

func TestPlaceLabelsNeverExceedsMaxRows(t *testing.T) {
	var bs []Block
	for i := 0; i < 8; i++ {
		bs = append(bs, blk("aaaaaaaaaa", float64(i)*3, 5))
	}
	opts := defaultOpts()
	opts.MaxDrift = 5
	ls := PlaceLabels(bs, fixed(10), opts)
	for i, l := range ls {
		if l.Row >= opts.MaxRows {
			t.Errorf("label %d Row = %d, want < MaxRows %d", i, l.Row, opts.MaxRows)
		}
	}
}

func TestPlaceLabelsRecordsAnchor(t *testing.T) {
	bs := []Block{blk("aaaaa", 0, 20), blk("bbbbb", 30, 20)}
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if ls[1].Anchor != 30 {
		t.Errorf("Anchor = %v, want the block's own X 30", ls[1].Anchor)
	}
}

func TestPlaceLabelsClampsToTrackWidth(t *testing.T) {
	bs := []Block{blk("aaaaaaaaaa", 960, 20)} // 100px label starting at 960 in a 1000 track
	ls := PlaceLabels(bs, fixed(10), defaultOpts())
	if got := ls[0].X + ls[0].W; got > 1000 {
		t.Errorf("label right edge = %v, want <= TrackWidth 1000", got)
	}
}

func TestPlaceLabelsHandlesEmptyInput(t *testing.T) {
	if ls := PlaceLabels(nil, fixed(10), defaultOpts()); len(ls) != 0 {
		t.Errorf("len = %d, want 0", len(ls))
	}
}

func TestPlaceLabelsNoOverlapWhenClampedToTrackWidth(t *testing.T) {
	// Two labels near the right edge: if the second one is clamped to TrackWidth,
	// it must not collide with the first. This is a regression test for a bug where
	// clamping re-introduced overlap that push-and-demote had avoided.
	bs := []Block{blk("aaaaaaaaaa", 900, 5), blk("bb", 950, 5)} // 100px and 20px labels
	ls := PlaceLabels(bs, fixed(10), LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 60, TrackWidth: 1000, MaxOverflow: 0})

	// Both labels should fit without overlap
	label0End := ls[0].X + ls[0].W
	label1Start := ls[1].X

	if label1Start < label0End+8 { // Gap = 8
		if ls[1].Row == ls[0].Row { // same row, so must respect Gap
			t.Errorf("labels overlap on same row: label0 ends at %v, label1 starts at %v (gap %v), want gap >= 8",
				label0End, label1Start, label1Start-label0End)
		}
	}
	// Also verify Anchor is preserved
	if ls[1].Anchor != 950 {
		t.Errorf("second label Anchor = %v, want block anchor 950", ls[1].Anchor)
	}
}

func TestPlaceLabelsNoOverlapMaxRows1(t *testing.T) {
	// Regression: two labels on a single row near the track edge.
	// The second label should not overlap the first, even if clamping is needed.
	// With MaxRows=1 there's nowhere to demote, so the second label must be pushed right.
	bs := []Block{blk("aaaaaaaaaa", 900, 5), blk("bb", 950, 5)} // 100px and 20px labels
	opts := LabelOpts{MaxRows: 1, Gap: 8, MaxDrift: 60, TrackWidth: 1000, MaxOverflow: 100}
	ls := PlaceLabels(bs, fixed(10), opts)

	if len(ls) != 2 {
		t.Fatalf("len = %d, want 2", len(ls))
	}

	// Both must be on row 0
	if ls[0].Row != 0 || ls[1].Row != 0 {
		t.Errorf("rows = %d,%d, want 0,0", ls[0].Row, ls[1].Row)
	}

	// They must not overlap
	label0End := ls[0].X + ls[0].W
	label1Start := ls[1].X

	if label1Start < label0End {
		t.Errorf("labels overlap: label0 [%v,%v), label1 [%v,%v), gap = %v",
			ls[0].X, label0End, label1Start, label1Start+ls[1].W, label1Start-label0End)
	}

	// Check overflow bound
	maxAllowed := opts.TrackWidth + opts.MaxOverflow
	for i, l := range ls {
		if l.X+l.W > maxAllowed {
			t.Errorf("label %d right edge %v exceeds maxRight %v", i, l.X+l.W, maxAllowed)
		}
	}

	// Anchor preserved
	for i, l := range ls {
		if l.Anchor != bs[i].X {
			t.Errorf("label %d Anchor = %v, want %v", i, l.Anchor, bs[i].X)
		}
	}
}

func TestPlaceLabelsNoStackingInCrammedCase(t *testing.T) {
	// Regression: five 50px labels at X=960,965,970,975,980 with MaxRows=2.
	// They should not stack on top of each other at identical X values.
	var bs []Block
	for i := 0; i < 5; i++ {
		bs = append(bs, blk("12345", 960+float64(i)*5, 5))
	}
	opts := LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 60, TrackWidth: 1000, MaxOverflow: 200}
	ls := PlaceLabels(bs, fixed(10), opts)

	if len(ls) != 5 {
		t.Fatalf("len = %d, want 5", len(ls))
	}

	// Check each row for ordering (no backwards moves, no stacking)
	rowLabels := make(map[int][]Label)
	for _, l := range ls {
		rowLabels[l.Row] = append(rowLabels[l.Row], l)
	}

	for row, labels := range rowLabels {
		for i := 1; i < len(labels); i++ {
			prevEnd := labels[i-1].X + labels[i-1].W
			currStart := labels[i].X
			if currStart < prevEnd+8 { // Gap
				t.Errorf("row %d: labels %d and %d overlap/underspaced: [%v,%v) then [%v,%v), gap = %v",
					row, i-1, i, labels[i-1].X, prevEnd, currStart, currStart+labels[i].W, currStart-prevEnd)
			}
		}
	}

	// Check overflow bound
	maxAllowed := opts.TrackWidth + opts.MaxOverflow
	for _, l := range ls {
		if l.X+l.W > maxAllowed {
			t.Errorf("label at anchor %v: right edge %v exceeds maxRight %v", l.Anchor, l.X+l.W, maxAllowed)
		}
	}

	// Anchors preserved
	for i, l := range ls {
		if l.Anchor != bs[i].X {
			t.Errorf("label %d Anchor = %v, want %v", i, l.Anchor, bs[i].X)
		}
	}
}

func TestPlaceLabelsDropsWhenExceedsOverflowBound(t *testing.T) {
	// Production-scale test: 50 event-length labels (160px each) clustered
	// in a ~50px window on a 1540px track with MaxRows=2.
	// Without overflow bound, the second label would be at X=9020 (off-screen).
	// With MaxOverflow=0, labels beyond TrackWidth should be omitted.
	// With MaxOverflow=50, some may fit, but not all.

	var bs []Block
	for i := 0; i < 50; i++ {
		// Event label is 160px wide; all events start at X ~1480 in a ~50px window
		bs = append(bs, blk("Event Title", 1480+float64(i%10)*3, 5))
	}

	opts := LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 20, TrackWidth: 1540, MaxOverflow: 0}
	ls := PlaceLabels(bs, fixed(10), opts)

	// With MaxOverflow=0, most labels will be dropped because they'd exceed TrackWidth
	if len(ls) >= len(bs) {
		t.Errorf("len(returned) = %d, want < len(blocks) = %d (some should be dropped)",
			len(ls), len(bs))
	}

	// Every returned label must fit within the bound
	maxAllowed := opts.TrackWidth + opts.MaxOverflow
	for _, l := range ls {
		if l.X+l.W > maxAllowed {
			t.Errorf("returned label at anchor %v: right edge %v exceeds TrackWidth %v",
				l.Anchor, l.X+l.W, opts.TrackWidth)
		}
	}
}

func TestPlaceLabelsKeepsMildCrowdingWithinBound(t *testing.T) {
	// Test that mild crowding within the overflow allowance does NOT
	// cause labels to be dropped — only truly unbounded overflow causes dropping.
	// Use a small label width and generous MaxDrift so they can fit.
	var bs []Block
	for i := 0; i < 5; i++ {
		bs = append(bs, blk("ok", 100+float64(i)*150, 5)) // "ok" = 20px, spaced 150px apart
	}

	opts := LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 300, TrackWidth: 1000, MaxOverflow: 100}
	ls := PlaceLabels(bs, fixed(10), opts)

	// All 5 labels should fit (good spacing, small labels)
	if len(ls) < 5 {
		t.Errorf("len = %d, want >= 5 (all should fit within generous MaxOverflow and MaxDrift)", len(ls))
	}

	// All must be within the bound
	maxAllowed := opts.TrackWidth + opts.MaxOverflow
	for _, l := range ls {
		if l.X+l.W > maxAllowed {
			t.Errorf("label right edge %v exceeds maxRight %v", l.X+l.W, maxAllowed)
		}
	}

	// Anchors preserved
	for _, l := range ls {
		found := false
		for _, b := range bs {
			if l.Anchor == b.X {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("label Anchor %v not found in block anchors", l.Anchor)
		}
	}
}
