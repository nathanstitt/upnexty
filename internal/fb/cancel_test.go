package fb

import (
	"context"
	"testing"
)

// A cancelled context must abort the render rather than run to completion: the
// board has three cores and a wedged parse would burn one with no way out.
func TestRenderHTMLHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RenderHTML(ctx, []byte("<div>hello</div>"), 200, 100)
	if err == nil {
		t.Error("RenderHTML returned a document for an already-cancelled context")
	}
}
