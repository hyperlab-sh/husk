package workbench

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func (m *workbenchModel) terminalView(id protocol.SurfaceID) contracts.TerminalDrawer {
	if m.fe == nil {
		return nil
	}
	return m.fe.TerminalView(id)
}

func (m *workbenchModel) terminalKey(id protocol.SurfaceID, msg tea.KeyPressMsg) bool {
	if m.terminalView(id) == nil {
		return false
	}
	m.fe.SendTerminalKey(id, uv.KeyPressEvent(msg))
	return true
}

func (m *workbenchModel) drawTerminal(c *canvas, id protocol.SurfaceID, r slotRect) bool {
	t := m.terminalView(id)
	if t == nil {
		return false
	}
	area := uv.Rect(r.InnerX, r.InnerY, r.InnerW, r.InnerH)
	t.Draw(clipScreen{c: c, clip: area.Intersect(uv.Rect(0, 0, c.w, c.h))}, area)
	return true
}

func (m *workbenchModel) terminalLabel(id protocol.SurfaceID, fallback string) string {
	if t := m.terminalView(id); t != nil && t.Title() != "" {
		text := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, t.Title())
		runes := []rune(text)
		return string(runes[:min(80, len(runes))])
	}
	return fallback
}

func (m *workbenchModel) terminalCursor() *tea.Cursor {
	if m.showPalette || m.settings != nil || m.secretPrompt != nil || m.blurred {
		return nil
	}
	id := m.focusedSlot
	r := m.slotRects[id]
	if m.modalOwner().Open() {
		id = m.modalSurface()
		r = m.modalRect
	}
	t := m.terminalView(id)
	if t == nil {
		return nil
	}
	cur := t.TerminalCursor()
	if !cur.Visible || cur.X < 0 || cur.Y < 0 || cur.X >= r.InnerW || cur.Y >= r.InnerH {
		return nil
	}
	c := tea.NewCursor(r.InnerX+cur.X, r.InnerY+cur.Y)
	c.Shape = tea.CursorShape(cur.Style)
	c.Blink = cur.Blink
	return c
}

// routeTerminalMouse runs after active divider dragging but before the line
// surface's narrower mouse filter. An unfocused content click only focuses,
// except ctrl+left presses, which also resolve terminal links.
func (m *workbenchModel) routeTerminalMouse(event tea.MouseMsg) bool {
	mouse := event.Mouse()
	if _, ok := event.(tea.MouseReleaseMsg); ok && m.terminalFocusClick && (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone) {
		m.terminalFocusClick = false
		return true
	}
	if _, ok := event.(tea.MouseMotionMsg); ok && m.terminalFocusClick && mouse.Button == tea.MouseLeft {
		return true
	}
	if _, ok := event.(tea.MouseClickMsg); ok && mouse.Button == tea.MouseLeft {
		m.terminalFocusClick = false
	}
	if click, ok := event.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && m.detectDividerAt(mouse.X, mouse.Y) != "" {
		return false
	}
	type target struct {
		id protocol.SurfaceID
		r  slotRect
	}
	var targets []target
	if m.modalOwner().Open() {
		targets = []target{{m.modalSurface(), m.modalRect}}
	} else {
		for _, hit := range m.hitOrder() {
			targets = append(targets, target{hit.ID, hit.Rect})
		}
	}
	for _, hit := range targets {
		r := hit.r
		if mouse.X < r.X || mouse.Y < r.Y || mouse.X >= r.X+r.Width || mouse.Y >= r.Y+r.Height {
			continue
		}
		if m.terminalView(hit.id) == nil {
			return false
		}
		x, y := mouse.X-r.InnerX, mouse.Y-r.InnerY
		if x < 0 || y < 0 || x >= r.InnerW || y >= r.InnerH {
			return false
		}
		rel := uv.Mouse{X: x, Y: y, Button: mouse.Button, Mod: uv.KeyMod(mouse.Mod)}
		_, wheel := event.(tea.MouseWheelMsg)
		if !wheel && !m.modalOwner().Open() && m.focusedSlot != hit.id {
			if _, ok := event.(tea.MouseClickMsg); ok {
				m.setFocus(hit.id)
				// Resolve links without encoding the focus click's fallback.
				if mouse.Button == tea.MouseLeft {
					m.terminalFocusClick = !m.fe.ResolveTerminalLink(hit.id, uv.MouseClickEvent(rel))
				}
			}
			return true
		}
		var encoded uv.MouseEvent
		switch event.(type) {
		case tea.MouseClickMsg:
			encoded = uv.MouseClickEvent(rel)
		case tea.MouseReleaseMsg:
			encoded = uv.MouseReleaseEvent(rel)
		case tea.MouseMotionMsg:
			encoded = uv.MouseMotionEvent(rel)
		case tea.MouseWheelMsg:
			encoded = uv.MouseWheelEvent(rel)
		}
		if encoded != nil {
			m.fe.SendTerminalMouse(hit.id, encoded)
		}
		return true
	}
	return false
}
