package workbench

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalLinkCoordinates(t *testing.T) {
	m, _, term, input := terminalModel(t, false)
	term.Write([]byte("\x1b[?1000h\x1b[?1006h\r\n  https://example"))
	var got protocol.TerminalLinkParams
	term.SetLinkHandler(func(url string, row, col int) { got = protocol.TerminalLinkParams{URL: url, Row: row, Col: col} })
	r := m.slotRects[protocol.SurfaceMainStage]
	if r.InnerX == 0 || r.InnerY == 0 {
		t.Fatal("fixture must offset the emulator from the terminal origin")
	}
	m.setFocus(protocol.SurfaceSidebarList)
	m.Update(tea.MouseClickMsg{X: r.InnerX + 5, Y: r.InnerY + 1, Button: tea.MouseLeft, Mod: tea.ModCtrl})
	m.Update(tea.MouseReleaseMsg{X: r.InnerX + 5, Y: r.InnerY + 1, Button: tea.MouseLeft})
	term.Close()
	if got != (protocol.TerminalLinkParams{URL: "https://example", Row: 1, Col: 5}) {
		t.Fatalf("link payload = %+v", got)
	}
	if len(input) != 0 {
		t.Fatalf("link gesture encoded %q", <-input)
	}
}
