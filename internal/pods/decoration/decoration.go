// Package decoration fixes only the renderer boundary. Content/cells remain
// opaque until the decoration representation is designed; no chrome is drawn.
package decoration

import (
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"image"
)

// Input carries content, declared chrome, palette and outer geometry.
type Input struct {
	Content any
	Frame   protocol.FrameSpec
	Palette protocol.Palette
	Outer   image.Rectangle
}

// Output carries opaque cells/content and the reported inner geometry.
type Output struct {
	Cells any
	Inner image.Rectangle
}

// Function is the pure decoration boundary, not a renderer or State consumer.
type Function func(Input) Output

// PassThrough preserves content and geometry without implementing decoration.
func PassThrough(in Input) Output { return Output{Cells: in.Content, Inner: in.Outer} }
