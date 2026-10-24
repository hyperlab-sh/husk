package workbench

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

// paletteModel hosts a plugin with n actions ("Action 00".."Action nn") so the
// palette lists more results than it draws at once. Extra actions go first in
// the returned plugin list.
func paletteModel(t *testing.T, acts ...contracts.PluginAction) *WorkbenchModel {
	t.Helper()
	m := goldenModel()
	plugins := map[string]bool{}
	for _, a := range acts {
		plugins[a.Plugin] = true
	}
	for id := range plugins {
		hostPlugin(m, id)
	}
	withFE(m).Acts = acts
	m.openPalette()
	if n := len(m.filteredCommands()); n <= paletteVisibleRows {
		t.Fatalf("setup: only %d palette results", n)
	}
	return m
}

func manyActions(n int) []contracts.PluginAction {
	var acts []contracts.PluginAction
	for i := 0; i < n; i++ {
		acts = append(acts, contracts.PluginAction{Plugin: "p.acts", ID: fmt.Sprintf("p.act%02d", i), Title: fmt.Sprintf("Action %02d", i)})
	}
	return acts
}

// drawnPalette returns the plain text rows of the drawn palette box.
func drawnPalette(m *WorkbenchModel) []string {
	c := newCanvas(m.width, m.height)
	m.drawCommandPalette(c)
	var rows []string
	for _, l := range strings.Split(ansi.Strip(c.render()), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			rows = append(rows, l)
		}
	}
	return rows
}

// highlightedRows returns the plain text of the drawn palette rows painted
// with the selection background.
func highlightedRows(m *WorkbenchModel) []string {
	r, g, b, _ := m.palette.SurfaceSelected.RGBA()
	needle := fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
	c := newCanvas(m.width, m.height)
	m.drawCommandPalette(c)
	var rows []string
	for _, l := range strings.Split(c.render(), "\n") {
		if strings.Contains(l, needle) {
			rows = append(rows, strings.TrimSpace(ansi.Strip(l)))
		}
	}
	return rows
}

// The cursor wraps over every result, so the window must follow it: the
// highlighted command is always drawn, going down past the last row, wrapping
// to the top, and going up from the top to the bottom.
func TestPaletteWindowFollowsCursor(t *testing.T) {
	m := paletteModel(t, manyActions(26)...)
	cmds := m.filteredCommands()
	check := func(step string) {
		t.Helper()
		hl := highlightedRows(m)
		if len(hl) != 1 {
			t.Fatalf("%s: cursor %d: %d highlighted rows, want 1", step, m.paletteCursor, len(hl))
		}
		if want := cmds[m.paletteCursor]; want.PluginID == "p.acts" && !strings.Contains(hl[0], want.Title) {
			t.Fatalf("%s: highlighted %q, cursor on %q", step, hl[0], want.Title)
		}
		if n := len(drawnPalette(m)); n > 5+paletteVisibleRows+2 {
			t.Fatalf("%s: palette draws %d lines", step, n)
		}
	}
	check("open")
	for range cmds {
		press(m, "down")
		check("down")
	}
	if m.paletteCursor != 0 {
		t.Fatalf("a full lap left the cursor on %d", m.paletteCursor)
	}
	press(m, "up") // wraps to the last result
	if m.paletteCursor != len(cmds)-1 {
		t.Fatalf("up from the top = %d, want %d", m.paletteCursor, len(cmds)-1)
	}
	check("wrap up")
	for range cmds {
		press(m, "up")
		check("up")
	}
}

// The window scrolls by the least amount: one row at a time past the edge.
func TestPaletteWindowScrollsMinimally(t *testing.T) {
	m := paletteModel(t, manyActions(26)...)
	for range paletteVisibleRows - 1 {
		press(m, "down")
	}
	if m.paletteTop != 0 {
		t.Fatalf("top = %d with the cursor on the last drawn row", m.paletteTop)
	}
	press(m, "down")
	if m.paletteTop != 1 {
		t.Fatalf("top = %d after one row past the window, want 1", m.paletteTop)
	}
	press(m, "up")
	if m.paletteTop != 1 {
		t.Fatalf("top = %d after moving back inside the window, want 1", m.paletteTop)
	}
}

// Enter runs the highlighted command, also when it was scrolled into view.
func TestPaletteEnterRunsHighlightedCommandBeyondFirstWindow(t *testing.T) {
	m := paletteModel(t, manyActions(26)...)
	cmds := m.filteredCommands()
	const target = 17
	for range target {
		press(m, "down")
	}
	want := cmds[target]
	if hl := highlightedRows(m); len(hl) != 1 || !strings.Contains(hl[0], want.Title) {
		t.Fatalf("highlighted rows %q, want %q", hl, want.Title)
	}
	press(m, "enter")
	fe := withFE(m)
	if len(fe.Invoked) == 0 || fe.Invoked[len(fe.Invoked)-1].Action != want.ID {
		t.Fatalf("invoked %+v, want %q", fe.Invoked, want.ID)
	}
}

// Typing resets the window to the first results.
func TestPaletteTypingResetsWindow(t *testing.T) {
	m := paletteModel(t, manyActions(26)...)
	for range 20 {
		press(m, "down")
	}
	press(m, "A")
	if m.paletteCursor != 0 || m.paletteTop != 0 {
		t.Fatalf("cursor %d top %d after typing", m.paletteCursor, m.paletteTop)
	}
}

// Column widths come from every match, not the drawn rows: scrolling must not
// shift a row's columns. One row is visible in two windows; a wide tag sits
// only in the second.
func TestPaletteColumnsStableWhileScrolling(t *testing.T) {
	acts := manyActions(26)
	acts[15].Title = "Long " + strings.Repeat("w", 120)
	acts = append(acts, contracts.PluginAction{Plugin: "p.a-very-long-plugin-name", ID: "p.wide", Title: "Wide tag"})
	m := paletteModel(t, acts...)
	cmds := m.filteredCommands()
	long := -1
	for i, c := range cmds {
		if strings.HasPrefix(c.Title, "Long ") {
			long = i
		}
	}
	titleCol := func() int {
		for _, l := range drawnPalette(m) {
			if i := strings.Index(l, "Long "); i >= 0 {
				return ansi.StringWidth(l[:i])
			}
		}
		t.Fatalf("long row not drawn (cursor %d top %d)", m.paletteCursor, m.paletteTop)
		return -1
	}
	for range long {
		press(m, "down")
	}
	before := titleCol()
	// walk up past the top so the window wraps to the bottom
	for m.paletteCursor != len(cmds)-1 {
		press(m, "up")
	}
	if after := titleCol(); after != before {
		t.Fatalf("title column moved from %d to %d while scrolling", before, after)
	}
}

// A hint longer than the old 14-cell cap is drawn whole.
func TestPaletteLongHintDrawnWhole(t *testing.T) {
	m := goldenModel()
	hint := "ctrl+shift+alt+x"
	cmds := []paletteCommand{{ID: "x", Title: "Command", Category: "Misc", Shortcut: hint}}
	const iw = 78
	cols := paletteColumns(cmds, iw)
	if cols.hint != ansi.StringWidth(hint) {
		t.Fatalf("hint column %d wide, want %d", cols.hint, ansi.StringWidth(hint))
	}
	plain := ansi.Strip(m.paletteRow(cmds[0], false, iw, cols))
	if !strings.Contains(plain, hint) {
		t.Fatalf("hint clipped: %q", plain)
	}
}
