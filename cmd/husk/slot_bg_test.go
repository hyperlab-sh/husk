package main

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// Cells with the default background take the slot colour; cells a plugin gave
// a background keep it, whatever it is; every other attribute is untouched.
func TestFillDefaultBgKeepsExplicitBackgrounds(t *testing.T) {
	slot := lipgloss.Color("#2B2A33")
	c := newCanvas(12, 2)
	c.blitBlock(0, 0, "\x1b[41;1mred\x1b[m plain \x1b[48;2;43;42;51mx\x1b[m")
	c.blitBlock(0, 1, "\x1b[7mrev\x1b[m")
	c.fillDefaultBg(uv.Rect(0, 0, 12, 2), slot)

	// the red cells keep their ANSI red and bold; plain and blank cells take the slot
	for x := 0; x < 3; x++ {
		cell := c.buf.CellAt(x, 0)
		if !sameColor(cell.Style.Bg, lipgloss.Color("1")) || cell.Style.Attrs&uv.AttrBold == 0 {
			t.Errorf("explicit-bg cell %d was changed: %+v", x, cell.Style)
		}
	}
	for _, x := range []int{3, 4, 8, 11} {
		if got := c.buf.CellAt(x, 0).Style.Bg; !sameColor(got, slot) {
			t.Errorf("default-bg cell %d bg = %v, want slot", x, got)
		}
	}
	// an explicit background that equals the slot colour is still "explicit"
	if !sameColor(c.buf.CellAt(9, 0).Style.Bg, slot) {
		t.Error("explicit bg equal to the slot colour was lost")
	}
	// reverse video cells are not repainted; blank cells on that row are
	if c.buf.CellAt(0, 1).Style.Bg != nil {
		t.Error("reverse-video cell was given a background")
	}
	if !sameColor(c.buf.CellAt(5, 1).Style.Bg, slot) {
		t.Error("blank cell next to a reverse-video run was not filled")
	}
}

func TestFillDefaultBgClipsToAreaAndCanvas(t *testing.T) {
	slot := lipgloss.Color("#2B2A33")
	c := newCanvas(6, 3)
	c.fillDefaultBg(uv.Rect(2, 1, 100, 100), slot) // extends past the canvas
	c.fillDefaultBg(uv.Rect(-5, -5, 2, 2), slot)   // wholly outside
	c.fillDefaultBg(uv.Rect(0, 0, 0, 0), slot)     // empty
	c.fillDefaultBg(uv.Rect(0, 0, 6, 3), nil)      // no colour
	for y := 0; y < 3; y++ {
		for x := 0; x < 6; x++ {
			in := x >= 2 && y >= 1
			if got := sameColor(c.buf.CellAt(x, y).Style.Bg, slot); got != in {
				t.Errorf("cell (%d,%d) filled = %v, want %v", x, y, got, in)
			}
		}
	}
}

// End to end through View: a slot's content area gets the token, the plugin's
// own background survives, and the bars, the gutter row above the stage and
// the cells outside the slot keep the styling they had.
func TestViewPaintsSlotBackgroundUnderPluginContent(t *testing.T) {
	m := goldenModel()
	goldenPlugin(m, "p.one", map[protocol.SurfaceID][]string{
		protocol.SurfaceMainStage: {"\x1b[41mred\x1b[m plain \x1b[48;2;1;2;3mrgb\x1b[m"},
	})
	rect := m.slotRects[protocol.SurfaceMainStage]
	if rect.InnerW < 20 || rect.InnerH < 3 {
		t.Fatalf("main stage inner rect too small: %+v", rect)
	}
	slot := m.palette.Slot
	if !sameColor(m.palette.Slot, lipgloss.Color(theme.Default().Slot)) || m.palette.Slot == nil {
		t.Fatalf("model palette slot = %v", m.palette.Slot)
	}
	frame := m.View().Content
	requireFrameGeometry(t, frame)
	rows := frameCells(frame)
	at := func(x, y int) uv.Cell { return rows[y][x] }

	y := rect.InnerY
	if got := at(rect.InnerX, y).Style.Bg; !sameColor(got, lipgloss.Color("1")) {
		t.Errorf("plugin red background replaced: %v", got)
	}
	if got := at(rect.InnerX+4, y).Style.Bg; !sameColor(got, slot) {
		t.Errorf("plain text cell bg = %v, want slot", got)
	}
	if got := at(rect.InnerX+10, y).Style.Bg; !sameColor(got, color.RGBA{1, 2, 3, 255}) {
		t.Errorf("plugin rgb background replaced: %v", got)
	}
	// blank cells: end of the first row and a whole empty row
	for _, p := range [][2]int{{rect.InnerX + rect.InnerW - 1, y}, {rect.InnerX, y + 2}, {rect.InnerX + rect.InnerW - 1, y + rect.InnerH - 1}} {
		if got := at(p[0], p[1]).Style.Bg; !sameColor(got, slot) {
			t.Errorf("blank cell %v bg = %v, want slot", p, got)
		}
	}
	// the bottom keybinding bar keeps its Surface background
	if got := at(1, goldenH-1).Style.Bg; !sameColor(got, m.palette.Surface) {
		t.Errorf("bottom bar bg = %v, want surface", got)
	}
	// the serialized frame must not leak the slot colour as an open style past
	// the content: every row still ends with a reset or a plain cell
	for i, line := range strings.Split(frame, "\n") {
		if strings.HasSuffix(line, "\x1b[48;2") {
			t.Errorf("row %d ends inside an SGR sequence", i)
		}
	}
}

// An unset served token must not install a non-nil noColor background: slot
// cells stay default until View applies Canvas, and explicit backgrounds survive.
func TestViewUnsetSlotBackgroundKeepsDefaultCells(t *testing.T) {
	m := goldenModel()
	p := protocol.Palette(theme.Default())
	p.Slot = ""
	withFE(m).PaletteFn = func() (protocol.Palette, map[string]int) { return p, nil }
	m.refresh()
	goldenPlugin(m, "p.one", map[protocol.SurfaceID][]string{
		protocol.SurfaceMainStage: {"\x1b[41mred\x1b[m plain"},
	})
	rect := m.slotRects[protocol.SurfaceMainStage]
	if rect.InnerW < 12 || rect.InnerH < 2 {
		t.Fatalf("main stage inner rect too small: %+v", rect)
	}
	c := newCanvas(m.width, m.height)
	defer c.release()
	m.drawSurface(c, protocol.SurfaceMainStage, "", "")
	for y := rect.InnerY; y < rect.InnerY+rect.InnerH; y++ {
		for x := rect.InnerX; x < rect.InnerX+rect.InnerW; x++ {
			bg := c.buf.CellAt(x, y).Style.Bg
			if y == rect.InnerY && x < rect.InnerX+3 {
				if !sameColor(bg, lipgloss.Color("1")) {
					t.Fatalf("plugin background at %d,%d changed: %v", x, y, bg)
				}
			} else if bg != nil {
				t.Fatalf("default slot cell %d,%d painted with %T(%v), want nil", x, y, bg, bg)
			}
		}
	}

	// The full frame's final canvas pass must still reach plain and blank cells.
	rows := frameCells(m.View().Content)
	for _, xy := range [][2]int{{rect.InnerX + 4, rect.InnerY}, {rect.InnerX, rect.InnerY + 1}} {
		if bg := rows[xy[1]][xy[0]].Style.Bg; !sameColor(bg, m.palette.Canvas) {
			t.Errorf("default slot cell %v bg = %v, want canvas %v", xy, bg, m.palette.Canvas)
		}
	}
}

// A bordered frame keeps its ring styling: only the content area is painted.
func TestViewSlotBackgroundSkipsBorderRing(t *testing.T) {
	m := goldenModel()
	m.palette.Slot = lipgloss.Color("#102030")
	goldenPlugin(m, "p.one", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"x"}})
	m.catalog.Frames["p.one"] = map[protocol.SurfaceID]protocol.FrameSpec{
		protocol.SurfaceMainStage: {Border: true, Label: "Stage"},
	}
	m.recomputeLayout()
	rect := m.slotRects[protocol.SurfaceMainStage]
	if !m.frameFor(protocol.SurfaceMainStage).Border {
		t.Fatal("main stage frame has no border")
	}
	slot := lipgloss.Color("#102030")
	rows := frameCells(m.View().Content)
	for _, p := range [][2]int{{rect.X, rect.Y}, {rect.X + 3, rect.Y}, {rect.X, rect.InnerY}, {rect.InnerX + rect.InnerW, rect.InnerY}} {
		if got := rows[p[1]][p[0]].Style.Bg; sameColor(got, slot) {
			t.Errorf("border cell %v took the slot background", p)
		}
	}
	if got := rows[rect.InnerY+1][rect.InnerX].Style.Bg; !sameColor(got, slot) {
		t.Errorf("content cell bg = %v, want slot", got)
	}
}
