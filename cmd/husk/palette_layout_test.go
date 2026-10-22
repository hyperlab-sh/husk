package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func paletteTestCmds() []paletteCommand {
	cmds := goldenModel().builtinCommands()
	for _, c := range []paletteCommand{
		{ID: "x.cjk", Title: "日本語のとても長いコマンド名をここに書きます日本語のとても長いコマンド名", Category: "プラグイン", Shortcut: "ctrl+日"},
		{ID: "x.long", Title: strings.Repeat("longword ", 30), Category: "A Very Long Category Name", Shortcut: "ctrl+shift+alt+super+x"},
		{ID: "x.nohint", Title: "No hint here", Category: "Misc"},
		{ID: "x.notag", Title: "No tag here", Shortcut: "z"},
	} {
		cmds = append(cmds, c)
	}
	return cmds
}

// Every row is exactly the inner width, at every width, with and without a
// selection; wide glyphs never push a row over.
func TestPaletteRowsFillWidthExactly(t *testing.T) {
	m := goldenModel()
	cmds := paletteTestCmds()
	for _, iw := range []int{20, 30, 36, 40, 56, 60, 78, 80, 120} {
		cols := paletteColumns(cmds, iw)
		for _, sel := range []bool{false, true} {
			for _, c := range cmds {
				row := m.paletteRow(c, sel, iw, cols)
				if got := ansi.StringWidth(row); got != iw {
					t.Errorf("iw=%d sel=%v %q: row width %d", iw, sel, c.ID, got)
				}
			}
		}
	}
}

// The hint column is never clipped: the full hint sits right-aligned at the
// row's end, and the title is the column that gives way. Below the threshold
// the hint column is dropped; narrower still the tag goes too.
func TestPaletteHintKeptTitleTruncated(t *testing.T) {
	m := goldenModel()
	cmds := goldenModel().builtinCommands()
	for _, iw := range []int{60, 80, 120} {
		cols := paletteColumns(cmds, iw)
		if cols.hint == 0 || cols.tag == 0 {
			t.Fatalf("iw=%d: hint/tag column dropped: %+v", iw, cols)
		}
		for _, c := range cmds {
			plain := ansi.Strip(m.paletteRow(c, false, iw, cols))
			if !strings.HasSuffix(strings.TrimSuffix(plain, " "), c.Shortcut) {
				t.Errorf("iw=%d %q: hint %q not at the right edge: %q", iw, c.ID, c.Shortcut, plain)
			}
			if !strings.HasPrefix(plain, " "+paletteTag(c)) {
				t.Errorf("iw=%d %q: tag %q not at the left edge: %q", iw, c.ID, paletteTag(c), plain)
			}
		}
	}
	for _, iw := range []int{40, 55} {
		cols := paletteColumns(cmds, iw)
		if cols.hint != 0 || cols.tag == 0 {
			t.Errorf("iw=%d: want hint dropped, tag kept: %+v", iw, cols)
		}
	}
	if cols := paletteColumns(cmds, 30); cols.hint != 0 || cols.tag != 0 || cols.title != 28 {
		t.Errorf("iw=30: want title only: %+v", cols)
	}
}

// Hints align on one column and titles start on one column.
func TestPaletteColumnsAlign(t *testing.T) {
	m := goldenModel()
	cmds := goldenModel().builtinCommands()
	iw := 80
	cols := paletteColumns(cmds, iw)
	for _, c := range cmds {
		plain := ansi.Strip(m.paletteRow(c, false, iw, cols))
		if ansi.Cut(plain, 1+cols.tag, 2+cols.tag) != " " || ansi.Cut(plain, iw-1, iw) != " " {
			t.Errorf("%q: gaps misplaced: %q", c.ID, plain)
		}
		hintStart := iw - 1 - cols.hint
		if got := strings.TrimSpace(ansi.Cut(plain, hintStart, iw)); got != c.Shortcut {
			t.Errorf("%q: hint column holds %q, want %q", c.ID, got, c.Shortcut)
		}
	}
}

// Long and CJK titles are cut on cell boundaries, never mid-glyph.
func TestPaletteWideTitlesTruncateOnCellBoundaries(t *testing.T) {
	m := goldenModel()
	cmds := paletteTestCmds()
	for _, iw := range []int{40, 60, 80, 120} {
		cols := paletteColumns(cmds, iw)
		for _, c := range cmds {
			plain := ansi.Strip(m.paletteRow(c, false, iw, cols))
			if strings.ContainsRune(plain, '�') {
				t.Errorf("iw=%d %q: broken glyph: %q", iw, c.ID, plain)
			}
			if ansi.StringWidth(plain) != iw {
				t.Errorf("iw=%d %q: width %d", iw, c.ID, ansi.StringWidth(plain))
			}
		}
	}
	cols := paletteColumns(cmds, 60)
	plain := ansi.Strip(m.paletteRow(cmds[len(cmds)-4], false, 60, cols))
	if !strings.Contains(plain, "…") {
		t.Errorf("long CJK title not ellipsised: %q", plain)
	}
}

// The background covers every cell of the row, and the selected row uses the
// selection colour end to end.
func TestPaletteRowBackgroundFullWidth(t *testing.T) {
	m := goldenModel()
	cmds := paletteTestCmds()
	for _, iw := range []int{40, 60, 80, 120} {
		cols := paletteColumns(cmds, iw)
		for _, sel := range []bool{false, true} {
			want := lipgloss.Color(m.palette.Surface)
			if sel {
				want = lipgloss.Color(m.palette.SurfaceSelected)
			}
			for _, c := range cmds {
				cells := frameCells(m.paletteRow(c, sel, iw, cols))
				if len(cells) != 1 || len(cells[0]) != iw {
					t.Fatalf("iw=%d %q: decoded %d cells", iw, c.ID, len(cells[0]))
				}
				for x, cell := range cells[0] {
					if cell.Content == "" { // continuation of a wide glyph
						continue
					}
					if !sameColor(cell.Style.Bg, want) {
						t.Fatalf("iw=%d sel=%v %q: cell %d background %v, want %v", iw, sel, c.ID, x, cell.Style.Bg, want)
					}
				}
			}
		}
	}
}

// The drawn palette box stays inside its width and keeps its rows aligned at
// the supported terminal widths.
func TestPaletteDrawnFrameRowsAligned(t *testing.T) {
	for _, w := range []int{84, 100, 144} {
		m := goldenModel()
		m.width = w
		m.recomputeLayout()
		m.showPalette = true
		c := newCanvas(m.width, m.height)
		m.drawCommandPalette(c)
		lines := strings.Split(ansi.Strip(c.render()), "\n")
		var box []string
		for _, l := range lines {
			if t := strings.TrimSpace(l); t != "" {
				box = append(box, t)
			}
		}
		if len(box) < 8 {
			t.Fatalf("w=%d: palette box has %d rows", w, len(box))
		}
		for i, l := range box {
			if ansi.StringWidth(l) != ansi.StringWidth(box[0]) {
				t.Errorf("w=%d row %d width %d != %d: %q", w, i, ansi.StringWidth(l), ansi.StringWidth(box[0]), l)
			}
		}
	}
}
