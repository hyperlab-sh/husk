package main

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/lucasb-eyer/go-colorful"
)

var (
	shInk    = color.RGBA{R: 200, G: 200, B: 200, A: 255}
	shCanvas = color.RGBA{R: 20, G: 20, B: 40, A: 255}
)

func shadowTestCanvas(w, h int, st uv.Style, glyph string) *canvas {
	c := newCanvas(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c.buf.SetCell(x, y, &uv.Cell{Content: glyph, Width: 1, Style: st})
		}
	}
	return c
}

func shadowed(c *canvas, x, y int) bool {
	cell := c.buf.CellAt(x, y)
	return cell != nil && cell.Style.Bg != nil && !sameColor(cell.Style.Bg, shCanvas) &&
		!sameColor(cell.Style.Bg, color.RGBA{R: 100, G: 150, B: 200, A: 255})
}

func TestShadowDarkensFgBgKeepsGlyphAndAttrs(t *testing.T) {
	bg := color.RGBA{R: 100, G: 150, B: 200, A: 255}
	fg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	c := shadowTestCanvas(20, 10, uv.Style{Fg: fg, Bg: bg, Attrs: uv.AttrBold}, "x")
	c.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)

	// first shadow row (y=3) right strip x in [8,10); bottom row y=6 spans [4,10)
	for _, p := range [][2]int{{8, 3}, {9, 3}, {8, 5}, {9, 5}, {4, 6}, {9, 6}} {
		cell := c.buf.CellAt(p[0], p[1])
		if cell.Content != "x" || cell.Style.Attrs != uv.AttrBold {
			t.Errorf("(%d,%d) glyph/attrs changed: %q %v", p[0], p[1], cell.Content, cell.Style.Attrs)
		}
		wantBg, _ := colorful.MakeColor(bg)
		wantBg = wantBg.BlendRgb(colorful.Color{}, popupShadowStrength)
		gotBg, _ := colorful.MakeColor(cell.Style.Bg)
		if gotBg.DistanceRgb(wantBg) > 0.01 {
			t.Errorf("(%d,%d) bg = %v, want %v", p[0], p[1], gotBg, wantBg)
		}
		r, _, _, _ := cell.Style.Fg.RGBA()
		if r>>8 >= 255/2 {
			t.Errorf("(%d,%d) fg not darkened: %v", p[0], p[1], cell.Style.Fg)
		}
	}
	// untouched: popup interior, left of the shadow, above, and below the shadow
	for _, p := range [][2]int{{2, 2}, {7, 5}, {3, 6}, {3, 3}, {8, 2}, {5, 7}, {10, 3}} {
		cell := c.buf.CellAt(p[0], p[1])
		if !sameColor(cell.Style.Bg, bg) || !sameColor(cell.Style.Fg, fg) {
			t.Errorf("(%d,%d) should be untouched, got %+v", p[0], p[1], cell.Style)
		}
	}
}

func TestShadowResolvesDefaultColours(t *testing.T) {
	c := shadowTestCanvas(20, 10, uv.Style{}, "·")
	c.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)
	cell := c.buf.CellAt(9, 4)
	if cell.Style.Fg == nil || cell.Style.Bg == nil {
		t.Fatalf("default colours not resolved: %+v", cell.Style)
	}
	if want := towardBlack(shCanvas); !sameColor(cell.Style.Bg, want) {
		t.Errorf("bg = %v, want %v", cell.Style.Bg, want)
	}
	if want := towardBlack(shInk); !sameColor(cell.Style.Fg, want) {
		t.Errorf("fg = %v, want %v", cell.Style.Fg, want)
	}
	if outside := c.buf.CellAt(12, 4); outside.Style.Bg != nil || outside.Style.Fg != nil {
		t.Errorf("cell outside the shadow was resolved: %+v", outside.Style)
	}
}

func TestShadowClipsAtScreenEdges(t *testing.T) {
	// popup touching the right and bottom edges: shadow falls off-screen, no panic
	c := shadowTestCanvas(10, 6, uv.Style{}, " ")
	c.castShadow(uv.Rect(4, 2, 6, 4), shInk, shCanvas)
	for y := 0; y < 6; y++ {
		for x := 0; x < 10; x++ {
			if shadowed(c, x, y) {
				t.Errorf("(%d,%d) darkened though the shadow is off-screen", x, y)
			}
		}
	}
	// popup one cell from the edges: partial shadow only inside the screen
	c = shadowTestCanvas(10, 6, uv.Style{}, " ")
	c.castShadow(uv.Rect(2, 1, 7, 4), shInk, shCanvas)
	if !shadowed(c, 9, 2) || !shadowed(c, 4, 5) || !shadowed(c, 9, 5) {
		t.Error("expected clipped shadow strip inside the screen")
	}
	// popup larger than the screen, negative origin, zero-size: no panic
	c = shadowTestCanvas(10, 6, uv.Style{}, " ")
	c.castShadow(uv.Rect(-5, -5, 40, 40), shInk, shCanvas)
	c.castShadow(uv.Rect(3, 3, 0, 0), shInk, shCanvas)
	c.castShadow(uv.Rect(3, 3, 4, 0), shInk, shCanvas)
	c.castShadow(uv.Rect(3, 3, 0, 4), shInk, shCanvas)
	for y := 0; y < 6; y++ {
		for x := 0; x < 10; x++ {
			if shadowed(c, x, y) {
				t.Errorf("(%d,%d) darkened by an oversized or empty popup", x, y)
			}
		}
	}
}

func TestShadowTinyPopup(t *testing.T) {
	// narrower than the offset: shadow rows begin right of the popup
	c := shadowTestCanvas(10, 6, uv.Style{}, " ")
	c.castShadow(uv.Rect(2, 1, 1, 2), shInk, shCanvas)
	if shadowed(c, 2, 2) {
		t.Error("popup cell darkened")
	}
	// the shadow is the popup shifted by (2,1): column 4 only, rows 2 and 3
	if shadowed(c, 3, 2) || !shadowed(c, 4, 2) {
		t.Error("strip right of a 1-wide popup wrong")
	}
	if shadowed(c, 3, 3) || !shadowed(c, 4, 3) || shadowed(c, 5, 3) {
		t.Error("bottom row of a 1-wide popup wrong")
	}
}

func wideCell(content string, st uv.Style) *uv.Cell {
	return &uv.Cell{Content: content, Width: 2, Style: st}
}

func TestShadowDoesNotSplitWideGlyphs(t *testing.T) {
	st := uv.Style{Fg: shInk, Bg: shCanvas}
	// popup x 2..7 (Max.X=8); shadow strip is x 8,9 on popup rows, x 4..9 on y=6.
	// Wide glyph at (7,..) is inside the popup so not our case; use the bottom
	// row: lead at x=3 (outside the shadow, continuation at 4 inside).
	c := shadowTestCanvas(20, 10, st, " ")
	c.buf.SetCell(3, 6, wideCell("世", st))
	// Wide glyph whose lead is the last shadow column on row 6: lead at 9,
	// continuation at 10 (outside the shadow).
	c.buf.SetCell(9, 6, wideCell("界", st))
	c.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)

	dark := towardBlack(shCanvas)
	for _, x := range []int{3, 4, 9, 10} {
		if cell := c.buf.CellAt(x, 6); !sameColor(cell.Style.Bg, dark) {
			t.Errorf("(%d,6) half of a wide glyph not darkened: %+v", x, cell.Style)
		}
	}
	if cell := c.buf.CellAt(3, 6); cell.Content != "世" || cell.Width != 2 {
		t.Errorf("lead cell changed: %+v", cell)
	}
	if cell := c.buf.CellAt(2, 6); !sameColor(cell.Style.Bg, shCanvas) {
		t.Error("cell left of the wide glyph was darkened")
	}
	if cell := c.buf.CellAt(11, 6); !sameColor(cell.Style.Bg, shCanvas) {
		t.Error("cell right of the wide glyph was darkened")
	}
}

func TestShadowSameRectTwiceDarkensOnce(t *testing.T) {
	bg := color.RGBA{R: 100, G: 150, B: 200, A: 255}
	once := shadowTestCanvas(20, 10, uv.Style{Bg: bg}, " ")
	once.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)
	twice := shadowTestCanvas(20, 10, uv.Style{Bg: bg}, " ")
	twice.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)
	twice.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)
	if a, b := once.render(), twice.render(); a != b {
		t.Error("casting the same popup twice darkened twice")
	}
}

func TestShadowNestedPopupsStack(t *testing.T) {
	bg := color.RGBA{R: 100, G: 150, B: 200, A: 255}
	c := shadowTestCanvas(30, 12, uv.Style{Bg: bg}, " ")
	c.castShadow(uv.Rect(2, 2, 10, 6), shInk, shCanvas)
	// (12,3) is in the first shadow strip; the second popup's shadow covers it too.
	first := c.buf.CellAt(12, 3).Style.Bg
	c.castShadow(uv.Rect(6, 1, 6, 4), shInk, shCanvas)
	second := c.buf.CellAt(12, 3).Style.Bg
	r1, _, _, _ := first.RGBA()
	r2, _, _, _ := second.RGBA()
	if r2 >= r1 {
		t.Errorf("nested shadow did not darken further: %v -> %v", first, second)
	}
}

func TestShadowNoSGRBleed(t *testing.T) {
	// the serialized frame resets styling per cell: every darkened cell carries
	// explicit colours, and the first unshadowed cell on the row is untouched.
	c := shadowTestCanvas(20, 10, uv.Style{}, " ")
	c.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)
	rows := frameCells(c.render())
	if got := rows[3][10].Style; got.Fg != nil || got.Bg != nil {
		t.Errorf("style bled past the shadow: %+v", got)
	}
	if got := rows[3][9].Style; got.Bg == nil {
		t.Error("shadow cell lost its colours on serialization")
	}
}

func TestPopupsCastShadowInView(t *testing.T) {
	m := goldenModel()
	m.showPalette = true
	rows := frameCells(m.View().Content)
	// the palette box is drawn at x=(w-palW)/2, y=max(3,h/6); find its shadow by
	// checking the row below the box is darker than the canvas.
	r, g, b, _ := towardBlack(lipgloss.Color(m.palette.Canvas)).RGBA()
	found := false
	for _, row := range rows {
		for _, cell := range row {
			if cell.Style.Bg == nil {
				continue
			}
			cr, cg, cb, _ := cell.Style.Bg.RGBA()
			// the serialized frame is 8-bit per channel
			if cr>>8 == r>>8 && cg>>8 == g>>8 && cb>>8 == b>>8 {
				found = true
			}
		}
	}
	if !found {
		t.Error("no shadow cell found with the darkened canvas colour")
	}
}
