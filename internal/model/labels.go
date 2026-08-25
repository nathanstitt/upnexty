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
	MaxRows     int     // hard cap on stacked label rows
	Gap         float64 // minimum horizontal space between labels on a row
	MaxDrift    float64 // how far a label may be pushed before it is demoted
	TrackWidth  float64 // labels are clamped to this width
	MaxOverflow float64 // how far past TrackWidth a label's right edge may extend
} // before it is omitted entirely (prevents runaway overflow)

// PlaceLabels positions one label per block, avoiding overlap. Labels are laid
// out left to right; a label that would collide is pushed right, and demoted to
// the next row if pushing would drift it more than MaxDrift from its anchor.
//
// IMPORTANT: The returned slice may be SHORTER than the input blocks slice.
// Labels that would exceed TrackWidth + MaxOverflow are omitted entirely,
// not placed off-screen. This prevents runaway overflow in dense time windows.
// Callers must NOT assume index alignment between blocks and returned labels.
// Use the Anchor field (block's own X) to correlate a label with its block.
func PlaceLabels(blocks []Block, measure func(string) float64, opts LabelOpts) []Label {
	if len(blocks) == 0 {
		return nil
	}
	if opts.MaxRows < 1 {
		opts.MaxRows = 1
	}
	maxRight := opts.TrackWidth + opts.MaxOverflow // hard ceiling for label right edges
	rowEnd := make([]float64, opts.MaxRows)        // right edge occupied per row
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

		for row := 0; row < opts.MaxRows; row++ {
			x := b.X
			if min := rowEnd[row] + opts.Gap; x < min {
				x = min
			}
			// Only the drift caused by pushing counts; demote if it is too far.
			if x-b.X > opts.MaxDrift && row < opts.MaxRows-1 {
				continue
			}

			// Check if label would overflow TrackWidth. If so, try to clamp it,
			// but re-check for collision after clamping.
			if x+w > opts.TrackWidth {
				clampedX := opts.TrackWidth - w
				if clampedX < 0 {
					clampedX = 0
				}
				// After clamping, check if the clamped position collides
				// with the previous label on this row.
				if clampedX < rowEnd[row] {
					// Collision after clamp. If we have more rows, try the next one.
					if row < opts.MaxRows-1 {
						continue
					}
					// On the last row: push right instead, overflow the track.
					// This is more readable than multiple labels stacked at X=950.
					x = rowEnd[row] + opts.Gap
				} else {
					// No collision after clamp; use the clamped position.
					x = clampedX
				}
			}

			// Check if label would exceed the overflow ceiling. If so, skip it.
			if x+w > maxRight {
				// This label cannot be placed within bounds; omit it entirely
				// rather than placing it off-screen where it won't be visible.
				break
			}

			lab := Label{Text: text, X: x, W: w, Row: row, Anchor: b.X}
			// Update rowEnd with the true right edge, even if it overflows TrackWidth.
			// This prevents later labels from thinking the row is free.
			rowEnd[row] = x + w
			out = append(out, lab)
			break
		}
	}
	return out
}
