package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestDividerOnlyOnBorderedEdges(t *testing.T) {
	m := borderedSidebarModel(t, 120, 40)
	sb := m.slotRects[protocol.SurfaceSidebarList]
	y := sb.Y + 2
	right := sb.X + sb.Width - 1

	if got := m.detectDividerAt(right, y); got != "sidebar-width" {
		t.Fatalf("bordered right edge = %q, want sidebar-width", got)
	}
	// The cell beside the edge belongs to the neighbour; with no bordered
	// neighbour there it is content, not a divider.
	if got := m.detectDividerAt(right+1, y); got != "" {
		t.Fatalf("unbordered neighbour column = %q, want no divider", got)
	}
	// An interior cell of the sidebar is never a divider.
	if got := m.detectDividerAt(sb.X+3, y); got != "" {
		t.Fatalf("interior cell = %q, want no divider", got)
	}
}

func TestDividerSkippedWhenSurfaceHasNoBorder(t *testing.T) {
	m := sizedModel(t, 120, 40)
	addTestPlugin(m, "p", map[protocol.SurfaceID]frame.Spec{protocol.SurfaceSidebarList: {Border: false, Label: "x"}})
	m.catalog.Occupants[protocol.SurfaceSidebarList] = "p"
	m.recomputeLayout()
	sb := m.slotRects[protocol.SurfaceSidebarList]
	for dx := -1; dx <= 1; dx++ {
		if got := m.detectDividerAt(sb.X+sb.Width-1+dx, sb.Y+2); got != "" {
			t.Fatalf("unbordered sidebar edge dx=%d = %q, want no divider", dx, got)
		}
	}
	m.Update(tea.MouseClickMsg{X: sb.X + sb.Width - 1, Y: sb.Y + 2, Button: tea.MouseLeft})
	if m.activeDrag != "" {
		t.Fatalf("click on unbordered edge started drag %q", m.activeDrag)
	}
}

func TestMainHeaderDividerOnItsOwnBottomBorderOnly(t *testing.T) {
	m := sizedModel(t, 120, 40)
	addTestPlugin(m, "p", map[protocol.SurfaceID]frame.Spec{protocol.SurfaceMainHeader: {Border: true}})
	m.catalog.Occupants[protocol.SurfaceMainHeader] = "p"
	m.recomputeLayout()
	h, ok := m.slotRects[protocol.SurfaceMainHeader]
	if !ok {
		t.Fatal("no main header rect")
	}
	x := h.X + h.Width/2
	bottom := h.Y + h.Height - 1
	if got := m.detectDividerAt(x, bottom); got != "main-header-height" {
		t.Fatalf("header bottom border = %q, want main-header-height", got)
	}
	if got := m.detectDividerAt(x, bottom+1); got != "" {
		t.Fatalf("first row of unbordered stage below header = %q, want no divider", got)
	}
}

func TestPaletteAcceptsUnicodeQuery(t *testing.T) {
	m := newWorkbenchModel()
	m.showPalette = true
	for _, r := range "aé日🙂" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.paletteQuery != "aé日🙂" {
		t.Fatalf("palette query = %q, want aé日🙂", m.paletteQuery)
	}
	// Backspace removes one rune, never half of a multi-byte sequence.
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.paletteQuery != "aé日" {
		t.Fatalf("after backspace = %q, want aé日", m.paletteQuery)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.paletteQuery != "a" {
		t.Fatalf("after 3 backspaces = %q, want a", m.paletteQuery)
	}
}

func TestPaletteIgnoresNamedKeysAndChords(t *testing.T) {
	for _, key := range []string{"", "up", "f5", "alt+x", "ctrl+a", "home", "\x1b", "\t", "alt+é", "alt+ö", "ctrl+日", "meta+ß", "super+🙂", "shift+é"} {
		if isPaletteText(key) {
			t.Errorf("isPaletteText(%q) = true, want false", key)
		}
	}
	for _, key := range []string{"a", "Z", " ", "~", "é", "ß", "日", "🙂", "é"} {
		if !isPaletteText(key) {
			t.Errorf("isPaletteText(%q) = false, want true", key)
		}
	}
}

func TestPaletteRejectsNonASCIIChords(t *testing.T) {
	for _, msg := range []tea.KeyPressMsg{
		{Code: 'ö', Text: "ö", Mod: tea.ModAlt},
		{Code: 'é', Mod: tea.ModAlt},
		{Code: '日', Mod: tea.ModCtrl},
		{Code: 'ö', Text: "ö", Mod: tea.ModSuper},
	} {
		m := newWorkbenchModel()
		m.showPalette = true
		m.paletteQuery = "x"
		m.Update(msg)
		if m.paletteQuery != "x" {
			t.Errorf("%+v changed palette query to %q", msg, m.paletteQuery)
		}
	}
	// Shift alone is still text.
	m := newWorkbenchModel()
	m.showPalette = true
	m.Update(tea.KeyPressMsg{Code: 'ö', ShiftedCode: 'Ö', Text: "Ö", Mod: tea.ModShift})
	if m.paletteQuery != "Ö" {
		t.Errorf("shifted unicode query = %q, want Ö", m.paletteQuery)
	}
}

func TestPaletteUnicodeFilterMatches(t *testing.T) {
	m := newWorkbenchModel()
	// Positive: a registered title contains non-ASCII text.
	for _, q := range []string{"🏠", "⓪"} {
		m.paletteQuery = q
		if got := m.filteredCommands(); len(got) == 0 {
			t.Errorf("unicode query %q matched no commands, want at least one", q)
		}
	}
	m.paletteQuery = "ÉÉÉ-no-such-command"
	if got := m.filteredCommands(); len(got) != 0 {
		t.Fatalf("unmatched unicode query matched %d commands, want 0", len(got))
	}
}

// sidePanelModel lays out a 144x40 workbench with the side panel occupied and
// the given border declarations for the centre column and the panel.
func sidePanelModel(t *testing.T, centreBorder, panelBorder bool) *WorkbenchModel {
	t.Helper()
	m := sizedModel(t, 144, 40)
	addTestPlugin(m, "p", map[protocol.SurfaceID]frame.Spec{
		protocol.SurfaceMainHeader: {Border: centreBorder},
		protocol.SurfaceMainStage:  {Border: centreBorder},
		protocol.SurfaceSidePanel:  {Border: panelBorder},
	})
	for _, s := range []protocol.SurfaceID{protocol.SurfaceMainHeader, protocol.SurfaceMainStage, protocol.SurfaceSidePanel} {
		m.catalog.Occupants[s] = "p"
	}
	m.recomputeLayout()
	if _, ok := m.slotRects[protocol.SurfaceSidePanel]; !ok {
		t.Fatal("side panel not laid out")
	}
	return m
}

func TestSidePanelDividerAcrossBorderCombinations(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		centreBorder, panelBd bool
	}{
		{"both bordered", true, true},
		{"centre bordered, panel plain", true, false},
		{"centre plain, panel bordered", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sidePanelModel(t, tc.centreBorder, tc.panelBd)
			side := m.slotRects[protocol.SurfaceSidePanel]
			stage := m.slotRects[protocol.SurfaceMainStage]
			y := stage.Y + 3
			stageRight := stage.X + stage.Width - 1
			if side.X-stageRight != surfaceGutter+1 {
				t.Fatalf("stage right edge %d, panel left %d: gutter not %d", stageRight, side.X, surfaceGutter)
			}
			if tc.centreBorder {
				if got := m.detectDividerAt(stageRight, y); got != "sidepanel-width" {
					t.Fatalf("centre right border = %q, want sidepanel-width", got)
				}
				m.Update(tea.MouseClickMsg{X: stageRight, Y: y, Button: tea.MouseLeft})
				if m.activeDrag != "sidepanel-width" {
					t.Fatalf("click on centre border: activeDrag = %q", m.activeDrag)
				}
			}
			if tc.panelBd {
				if got := m.detectDividerAt(side.X, y); got != "sidepanel-width" {
					t.Fatalf("panel left border = %q, want sidepanel-width", got)
				}
				m.activeDrag = ""
				m.Update(tea.MouseClickMsg{X: side.X, Y: y, Button: tea.MouseLeft})
				if m.activeDrag != "sidepanel-width" {
					t.Fatalf("click on panel border: activeDrag = %q", m.activeDrag)
				}
			}
		})
	}
}

// The gutter belongs to no slot rect and is never a drawn border, so it is
// inert: no divider, no drag, and no surface under it.
func TestSidePanelGutterIsInert(t *testing.T) {
	m := sidePanelModel(t, true, true)
	side := m.slotRects[protocol.SurfaceSidePanel]
	gx := side.X - surfaceGutter
	for y := 1; y < m.height-1; y += 7 {
		for _, h := range m.hitOrder() {
			r := h.Rect
			if gx >= r.X && gx < r.X+r.Width && y >= r.Y && y < r.Y+r.Height && h.ID != protocol.SurfaceActivityRail {
				t.Fatalf("gutter (%d,%d) is inside slot %s", gx, y, h.ID)
			}
		}
		if got := m.detectDividerAt(gx, y); got != "" {
			t.Fatalf("gutter (%d,%d) = %q, want no divider", gx, y, got)
		}
		m.Update(tea.MouseClickMsg{X: gx, Y: y, Button: tea.MouseLeft})
		if m.activeDrag != "" {
			t.Fatalf("click on gutter started drag %q", m.activeDrag)
		}
	}
}
