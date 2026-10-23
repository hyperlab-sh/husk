package decoration

import (
	"image"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPassThroughLeavesContentAndGeometryUndecided(t *testing.T) {
	content := &struct{ Lines []string }{[]string{"content"}}
	outer := image.Rect(2, 3, 20, 10)
	out := PassThrough(Input{Content: content, Outer: outer, Frame: protocol.FrameSpec{Border: true, Label: "label", Close: true}})
	if out.Cells != content || out.Inner != outer {
		t.Fatal("boundary implemented decoration instead of passing through")
	}
}
