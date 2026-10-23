package contracts

import (
	"context"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TerminalPort serves plugin-owned terminal streams on occupied surfaces.
type TerminalPort interface {
	TerminalAttach(context.Context, Caller, protocol.TerminalAttachParams) (protocol.TerminalAttachResult, error)
	TerminalWrite(context.Context, Caller, protocol.TerminalWriteParams) error
	TerminalDetach(context.Context, Caller, protocol.TerminalDetachParams) error
}

// TerminalCursor is a cursor relative to the surface's inner rectangle.
type TerminalCursor struct {
	X, Y    int
	Visible bool
	Style   int
	Blink   bool
}

// TerminalDrawer is a loop-only view of an attached emulator. The frontend
// supplies a clipped screen; Draw never changes emulator state.
type TerminalDrawer interface {
	Draw(uv.Screen, uv.Rectangle)
	TerminalCursor() TerminalCursor
	Title() string
}

// TerminalMetadata is the screen-free State entry for one attachment.
type TerminalMetadata struct {
	Title      string `json:"title"`
	AltScreen  bool   `json:"alt_screen"`
	Mouse      bool   `json:"mouse"`
	Scrollback int    `json:"scrollback"`
}
