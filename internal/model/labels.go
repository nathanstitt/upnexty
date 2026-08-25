package model

// Label is an event title positioned independently of its block. Because the
// time axis is strictly linear, a short meeting's block can be far narrower
// than its title; the label floats free and may overflow the block.
type Label struct {
	Text   string
	X, W   float64
	Row    int
	Anchor float64 // the block's own X, for drawing a leader when drifted
}

// LabelOpts tunes label placement.
type LabelOpts struct {
	MaxRows    int     // hard cap on stacked label rows
	Gap        float64 // minimum horizontal space between labels on a row
	MaxDrift   float64 // how far a label may be pushed before it is demoted
	TrackWidth float64 // labels are clamped to this width
}

// PlaceLabels positions one label per block, avoiding overlap. Labels are laid
// out left to right; a label that would collide is pushed right, and demoted to
// the next row if pushing would drift it more than MaxDrift from its anchor.
func PlaceLabels(blocks []Block, measure func(string) float64, opts LabelOpts) []Label {
	if len(blocks) == 0 {
		return nil
	}
	if opts.MaxRows < 1 {
		opts.MaxRows = 1
	}
	rowEnd := make([]float64, opts.MaxRows) // right edge occupied per row
	for i := range rowEnd {
		rowEnd[i] = -1e9
	}

	out := make([]Label, 0, len(blocks))
	for _, b := range blocks {
		text := b.Event.Title
		w := measure(text)
		if w > opts.TrackWidth {
			w = opts.TrackWidth
		}

		placed := false
		var lab Label
		for row := 0; row < opts.MaxRows; row++ {
			x := b.X
			if min := rowEnd[row] + opts.Gap; x < min {
				x = min
			}
			// Only the drift caused by pushing counts; demote if it is too far.
			if x-b.X > opts.MaxDrift && row < opts.MaxRows-1 {
				continue
			}
			if x+w > opts.TrackWidth {
				x = opts.TrackWidth - w
				if x < 0 {
					x = 0
				}
			}
			lab = Label{Text: text, X: x, W: w, Row: row, Anchor: b.X}
			rowEnd[row] = x + w
			placed = true
			break
		}
		if !placed { // every row drifted too far: accept the last row anyway
			row := opts.MaxRows - 1
			x := rowEnd[row] + opts.Gap
			if x+w > opts.TrackWidth {
				x = opts.TrackWidth - w
				if x < 0 {
					x = 0
				}
			}
			lab = Label{Text: text, X: x, W: w, Row: row, Anchor: b.X}
			rowEnd[row] = x + w
		}
		out = append(out, lab)
	}
	return out
}
