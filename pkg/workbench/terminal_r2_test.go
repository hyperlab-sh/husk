package workbench

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/terminals"
)

func TestTerminalUnfocusedWheelAndMouseRestore(t *testing.T) {
	m, fe, term, _ := terminalModel(t, false)
	term.Write([]byte(strings.Repeat("history\r\n", 10)))
	r := m.slotRects[protocol.SurfaceMainStage]
	m.setFocus(protocol.SurfaceSidebarList)
	m.Update(tea.MouseWheelMsg{X: r.InnerX + 2, Y: r.InnerY + 1, Button: tea.MouseWheelUp})
	if len(fe.TerminalMice) != 1 || term.Cursor().Visible || m.focusedSlot != protocol.SurfaceSidebarList {
		t.Fatal("unfocused wheel neither scrolled history nor retained focus")
	}
	m.setFocus(protocol.SurfaceMainStage)
	term.Write([]byte("\x1b[?1000h\x1b[?1049h\x1b[?2004h"))
	delete(fe.TerminalDrawers, protocol.SurfaceMainStage)
	fe.TerminalMice = nil
	m.Update(tea.MouseClickMsg{X: r.InnerX + 2, Y: r.InnerY + 1, Button: tea.MouseLeft})
	m.Update(tea.MouseWheelMsg{X: r.InnerX + 2, Y: r.InnerY + 1, Button: tea.MouseWheelUp})
	if len(fe.TerminalMice) != 0 || len(fe.Mice) == 0 || m.View().Cursor != nil {
		t.Fatal("host mouse or cursor not restored after detach")
	}
}

func TestTerminalDividerDragStaysInHost(t *testing.T) {
	m := borderedSidebarModel(t, 144, 40)
	fe := withFE(m)
	term := terminals.New(20, 5, 0, nil)
	t.Cleanup(term.Close)
	fe.TerminalDrawers = map[protocol.SurfaceID]contracts.TerminalDrawer{protocol.SurfaceSidebarList: testDrawer{term}}
	r := m.slotRects[protocol.SurfaceSidebarList]
	x, y := r.X+r.Width-1, r.Y+2
	before := m.sidebarWidth
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.activeDrag != "sidebar-width" {
		t.Fatal("terminal border did not start host drag")
	}
	m.Update(tea.MouseMotionMsg{X: x - 3, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x - 3, Y: y, Button: tea.MouseLeft})
	if m.sidebarWidth == before || m.activeDrag != "" || len(fe.TerminalMice) != 0 {
		t.Fatal("drag over terminal leaked to program or failed")
	}
}

func TestTerminalKeyStatePrivacyForEveryPlugin(t *testing.T) {
	m, fe, _, _ := terminalModel(t, false)
	m.broadcastKeyState(protocol.KeyPhaseDown, "x", true)
	if len(fe.KeyStates) != 0 {
		t.Fatal("focused terminal leaked key_state to another plugin")
	}
	fe.TerminalDrawers = nil
	m.broadcastKeyState(protocol.KeyPhaseDown, "x", true)
	if len(fe.KeyStates) < 2 {
		t.Fatal("key_state broadcast did not resume for all plugins")
	}
}

type releasePrefixKeys struct{ defaultKeys }

func (k releasePrefixKeys) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	if ctx.Capture == protocol.CaptureNone && len(keys) == 1 && keys[0] == "ctrl+k" {
		return contracts.KeyResolution{Outcome: contracts.KeyPrefix}
	}
	return k.defaultKeys.Resolve(ctx, keys)
}
func TestTerminalReleaseCoversExactlyOnePrefixKey(t *testing.T) {
	m, fe, _, input := terminalModel(t, false)
	fe.defaultKeys = defaultKeys{}
	m.keys = releasePrefixKeys{}
	m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if len(m.pendingKeys) != 1 || m.captureReleased {
		t.Fatal("release did not end at the prefix key")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if got := string(<-input); got != "p" {
		t.Fatalf("captured completion %q", got)
	}
}
