package workbench

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalUnfocusedPlainCtrlClickOnlyFocuses(t *testing.T) {
	m, fe, term, input := terminalModel(t, false)
	term.Write([]byte("\x1b[?1000h\x1b[?1006hplain text"))
	links := 0
	term.SetLinkHandler(func(string, int, int) { links++ })
	r := m.slotRects[protocol.SurfaceMainStage]
	m.setFocus(protocol.SurfaceSidebarList)
	m.Update(tea.MouseClickMsg{X: r.InnerX + 3, Y: r.InnerY, Button: tea.MouseLeft, Mod: tea.ModCtrl})
	m.Update(tea.MouseReleaseMsg{X: r.InnerX + 3, Y: r.InnerY, Button: tea.MouseLeft})
	term.Close()
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatal("click did not focus the terminal")
	}
	if links != 0 {
		t.Fatalf("plain text produced %d links", links)
	}
	if len(fe.TerminalMice) != 0 {
		t.Fatalf("focus-only gesture forwarded %d mouse events", len(fe.TerminalMice))
	}
	if len(input) != 0 {
		t.Fatalf("focus-only gesture encoded %q", <-input)
	}
}

func TestRuntimeTerminalLinkCoordinates(t *testing.T) {
	for _, placement := range []protocol.SurfacePlacement{protocol.SurfacePlacementModal, protocol.SurfacePlacementCanvas} {
		t.Run(string(placement), func(t *testing.T) {
			m, fe, term, input := terminalModel(t, false)
			const id protocol.SurfaceID = "p.screen"
			openRuntimeFixture(m, fe, id, placement)
			fe.TerminalDrawers[id] = fe.TerminalDrawers[protocol.SurfaceMainStage]
			delete(fe.TerminalDrawers, protocol.SurfaceMainStage)
			term.Write([]byte("\x1b[?1000h\x1b[?1006h\r\n  https://example"))
			var links []protocol.TerminalLinkParams
			term.SetLinkHandler(func(url string, row, col int) {
				links = append(links, protocol.TerminalLinkParams{Surface: id, URL: url, Row: row, Col: col})
			})
			r := m.slotRects[id]
			if placement == protocol.SurfacePlacementModal {
				r = m.modalRect
			}
			if r.InnerX == 0 || r.InnerY == 0 {
				t.Fatal("runtime fixture must offset the emulator from the terminal origin")
			}
			m.Update(tea.MouseClickMsg{X: r.InnerX + 5, Y: r.InnerY + 1, Button: tea.MouseLeft, Mod: tea.ModCtrl})
			m.Update(tea.MouseReleaseMsg{X: r.InnerX + 5, Y: r.InnerY + 1, Button: tea.MouseLeft})
			term.Close()
			want := protocol.TerminalLinkParams{Surface: id, URL: "https://example", Row: 1, Col: 5}
			if len(links) != 1 || links[0] != want {
				t.Fatalf("runtime links = %+v, want %+v", links, want)
			}
			if len(input) != 0 {
				t.Fatalf("runtime link gesture encoded %q", <-input)
			}
		})
	}
}

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
