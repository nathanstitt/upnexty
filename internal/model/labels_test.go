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
	return LabelOpts{MaxRows: 2, Gap: 8, MaxDrift: 60, TrackWidth: 1000}
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
