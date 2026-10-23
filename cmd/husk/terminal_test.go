package main

import (
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/terminals"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"strings"
	"testing"
	"time"
)

type testDrawer struct{ *terminals.Terminal }

func (d testDrawer) Title() string { return d.Metadata().Title }
func (d testDrawer) TerminalCursor() contracts.TerminalCursor {
	c := d.Cursor()
	return contracts.TerminalCursor{X: c.X, Y: c.Y, Visible: c.Visible, Style: c.Style, Blink: c.Blink}
}

func terminalModel(t *testing.T, modal bool) (*WorkbenchModel, *fakeFE, *terminals.Terminal, <-chan []byte) {
	t.Helper()
	m, fe, _, _ := captureModel(t)
	id := protocol.SurfaceMainStage
	if modal {
		m.openModal("stage", "stage.open")
		id = protocol.SurfaceModal
	}
	m.catalog.Capture[id] = protocol.CaptureAll
	input := make(chan []byte, 128)
	term := terminals.New(20, 5, 10, func(b []byte) { input <- b })
	t.Cleanup(term.Close)
	fe.TerminalDrawers = map[protocol.SurfaceID]contracts.TerminalDrawer{id: testDrawer{term}}
	return m, fe, term, input
}

func TestTerminalModalFullCaptureAndRelease(t *testing.T) {
	m, fe, _, input := terminalModel(t, true)
	fe.defaultKeys.Extra = []defaultBinding{{"z", "husk.modal.close", "modal-surface"}}
	for _, key := range []tea.KeyPressMsg{{Code: 'z', Text: "z"}, {Code: tea.KeyEscape}, {Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyTab}, {Code: '1', Mod: tea.ModAlt}} {
		m.Update(key)
	}
	var bytes string
	for len(bytes) < 7 {
		bytes += string(<-input)
	}
	if bytes != "z\x1bq\x03\t\x1b1" {
		t.Fatalf("program input = %q", bytes)
	}
	if !m.modalOwner().Open() {
		t.Fatal("modal-close key escaped capture")
	}
	m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if !m.captureReleased {
		t.Fatal("release did not resolve through modal filter")
	}
	found := false
	for _, h := range m.footerHints() {
		found = found || h.desc == "release"
	}
	if !found {
		t.Fatal("release footer missing")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modalOwner().Open() || m.captureReleased {
		t.Fatal("released Escape did not close modal exactly once")
	}
	if len(fe.TerminalKeys) != 6 {
		t.Fatal("release or released Escape reached terminal")
	}
	if len(fe.Keys) != 0 || len(fe.KeyStates) != 0 {
		t.Fatal("structured input was sent to an attached terminal")
	}
}

func TestTerminalReleaseClockAndDoubleRelease(t *testing.T) {
	m, fe, _, input := terminalModel(t, false)
	var timeout tea.Msg
	m.after = func(d time.Duration, msg tea.Msg) tea.Cmd {
		if d != 1500*time.Millisecond {
			t.Fatalf("duration %v", d)
		}
		timeout = msg
		return nil
	}
	release := tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
	m.Update(release)
	m.Update(timeout)
	if m.captureReleased {
		t.Fatal("capture survived timeout")
	}
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if got := string(<-input); got != "q" {
		t.Fatalf("timeout key = %q", got)
	}
	m.Update(release)
	old := timeout
	m.Update(release)
	if m.captureReleased {
		t.Fatal("double release rearmed capture suspension")
	}
	if got := string(<-input); got != "\x1d" {
		t.Fatalf("double release bytes = %q", got)
	}
	m.Update(release)
	m.Update(old)
	if !m.captureReleased {
		t.Fatal("stale timer consumed newer release")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !m.showPalette {
		t.Fatal("released palette binding not dispatched")
	}
	before := len(fe.TerminalKeys)
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if len(fe.TerminalKeys) != before {
		t.Fatal("palette typing reached terminal")
	}
}

func TestTerminalSettingsOverlayOwnsInputAndCursor(t *testing.T) {
	m, fe, _, input := terminalModel(t, false)
	if m.View().Cursor == nil {
		t.Fatal("focused terminal cursor missing")
	}
	if _, err := m.openSettings(nil); err != nil {
		t.Fatal(err)
	}
	if m.View().Cursor != nil {
		t.Fatal("terminal cursor visible beneath Settings")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.Update(tea.PasteMsg{Content: "private overlay input"})
	r := m.slotRects[protocol.SurfaceMainStage]
	m.Update(tea.MouseClickMsg{X: r.InnerX, Y: r.InnerY, Button: tea.MouseLeft})
	if len(fe.TerminalKeys) != 0 || len(fe.TerminalPastes) != 0 || len(fe.TerminalMice) != 0 {
		t.Fatal("Settings input reached the terminal")
	}
	m.closeSettings()
	if m.View().Cursor == nil {
		t.Fatal("terminal cursor not restored after Settings")
	}
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if got := string(<-input); got != "y" {
		t.Fatalf("restored terminal input = %q", got)
	}
}

func TestTerminalCanvasCursorTitleAndMouse(t *testing.T) {
	m, fe, term, _ := terminalModel(t, false)
	term.Write([]byte("\x1b]2;Program\ahello"))
	r := m.slotRects[protocol.SurfaceMainStage]
	c := newCanvas(m.width, m.height)
	defer c.release()
	if !strings.Contains(m.View().Content, "hello") {
		t.Fatal("View did not compose terminal content")
	}
	if !m.drawTerminal(c, protocol.SurfaceMainStage, r) {
		t.Fatal("terminal not drawn")
	}
	if cell := c.buf.CellAt(r.InnerX, r.InnerY); cell == nil || cell.Content != "h" {
		t.Fatalf("content cell %+v", cell)
	}
	if cell := c.buf.CellAt(r.InnerX-1, r.InnerY); cell != nil && strings.TrimSpace(cell.Content) != "" {
		t.Fatal("content escaped inner rect")
	}
	if cur := m.terminalCursor(); cur == nil || cur.X != r.InnerX+5 || cur.Y != r.InnerY {
		t.Fatalf("cursor %+v", cur)
	}
	if got := m.terminalLabel(protocol.SurfaceMainStage, "Manifest"); got != "Program" {
		t.Fatalf("label %q", got)
	}
	term.Write([]byte("\x1b[?25l"))
	if m.terminalCursor() != nil {
		t.Fatal("hidden cursor drawn")
	}
	x, y := r.InnerX+2, r.InnerY+1
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight, Mod: tea.ModCtrl})
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseRight})
	m.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseRight})
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp})
	if len(fe.TerminalMice) != 4 {
		t.Fatalf("mouse events %d", len(fe.TerminalMice))
	}
	mouse := fe.TerminalMice[0].Mouse()
	if mouse.X != 2 || mouse.Y != 1 || mouse.Mod != uv.ModCtrl {
		t.Fatalf("relative mouse %+v", mouse)
	}
	m.setFocus(protocol.SurfaceSidebarList)
	before := len(fe.TerminalMice)
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.focusedSlot != protocol.SurfaceMainStage || len(fe.TerminalMice) != before {
		t.Fatal("focus click sent to program")
	}
	m.Update(tea.PasteMsg{Content: "text"})
	if len(fe.TerminalPastes) != 1 || fe.TerminalPastes[0] != "text" {
		t.Fatal("paste not routed whole")
	}
	term.Write([]byte("\x1b[2J\x1b[Habc界"))
	clip := r
	clip.InnerW = 4
	m.drawTerminal(c, protocol.SurfaceMainStage, clip)
	if cell := c.buf.CellAt(r.InnerX+3, r.InnerY); cell != nil && strings.TrimSpace(cell.Content) != "" {
		t.Fatal("wide glyph escaped right clip edge")
	}
}
