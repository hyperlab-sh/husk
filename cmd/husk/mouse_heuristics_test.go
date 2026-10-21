package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func TestDividerOnlyOnBorderedEdges(t *testing.T) {
	m := borderedSidebarModel(t, 120, 40)
	sb := m.slotRects[huskipc.SlotSidebarList]
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
	addTestPlugin(m, "p", map[huskipc.SlotID]frame.Spec{huskipc.SlotSidebarList: {Border: false, Label: "x"}})
	m.registry.Occupants[huskipc.SlotSidebarList] = "p"
	m.recomputeLayout()
	sb := m.slotRects[huskipc.SlotSidebarList]
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
	addTestPlugin(m, "p", map[huskipc.SlotID]frame.Spec{huskipc.SlotMainHeader: {Border: true}})
	m.registry.Occupants[huskipc.SlotMainHeader] = "p"
	m.recomputeLayout()
	h, ok := m.slotRects[huskipc.SlotMainHeader]
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
	m := newWorkbenchModel("", nil, t.TempDir())
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
		m := newWorkbenchModel("", nil, t.TempDir())
		m.showPalette = true
		m.paletteQuery = "x"
		m.Update(msg)
		if m.paletteQuery != "x" {
			t.Errorf("%+v changed palette query to %q", msg, m.paletteQuery)
		}
	}
	// Shift alone is still text.
	m := newWorkbenchModel("", nil, t.TempDir())
	m.showPalette = true
	m.Update(tea.KeyPressMsg{Code: 'ö', ShiftedCode: 'Ö', Text: "Ö", Mod: tea.ModShift})
	if m.paletteQuery != "Ö" {
		t.Errorf("shifted unicode query = %q, want Ö", m.paletteQuery)
	}
}

func TestPaletteUnicodeFilterMatches(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
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
