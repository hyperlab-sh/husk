package host

import (
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/terminals"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type terminalDrawer struct{ *terminals.Terminal }

func (d terminalDrawer) TerminalCursor() contracts.TerminalCursor {
	c := d.Cursor()
	return contracts.TerminalCursor{X: c.X, Y: c.Y, Visible: c.Visible, Style: c.Style, Blink: c.Blink}
}
func (d terminalDrawer) Title() string { return d.Metadata().Title }

// TerminalView implements contracts.Frontend.
func (r *Root) TerminalView(surface protocol.SurfaceID) contracts.TerminalDrawer {
	if t := r.engine.Terminal(surface); t != nil {
		return terminalDrawer{t}
	}
	return nil
}

// SendTerminalKey implements contracts.Frontend.
func (r *Root) SendTerminalKey(surface protocol.SurfaceID, key uv.KeyPressEvent) {
	if t := r.engine.Terminal(surface); t != nil {
		t.Key(key)
	}
}

// SendTerminalMouse implements contracts.Frontend.
func (r *Root) SendTerminalMouse(surface protocol.SurfaceID, event uv.MouseEvent) {
	if t := r.engine.Terminal(surface); t != nil {
		t.Mouse(event)
	}
}

// SendTerminalPaste implements contracts.Frontend.
func (r *Root) SendTerminalPaste(surface protocol.SurfaceID, text string) {
	if t := r.engine.Terminal(surface); t != nil {
		t.Paste(text)
	}
}
