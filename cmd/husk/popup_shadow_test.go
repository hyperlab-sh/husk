package main

import (
	"fmt"
	"image/color"
	"reflect"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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

func TestShadowKeepsIndexedCellsAndBlendsHexNeighbour(t *testing.T) {
	for _, indexed := range []color.Color{ansi.BasicColor(5), ansi.IndexedColor(123)} {
		for _, channel := range []string{"foreground", "background", "underline"} {
			t.Run(fmt.Sprintf("%T/%s", indexed, channel), func(t *testing.T) {
				st := uv.Style{Fg: shInk, Bg: shCanvas, UnderlineColor: shInk, Attrs: uv.AttrBold}
				switch channel {
				case "foreground":
					st.Fg = indexed
				case "background":
					st.Bg = indexed
				case "underline":
					st.UnderlineColor = indexed
				}
				c := shadowTestCanvas(20, 10, uv.Style{Fg: shInk, Bg: shCanvas, UnderlineColor: shInk}, "x")
				defer c.release()
				cell := uv.Cell{Content: "i", Width: 1, Style: st, Link: uv.Link{URL: "https://example.invalid"}}
				c.buf.SetCell(8, 3, &cell)
				c.castShadow(uv.Rect(2, 2, 6, 4), shInk, shCanvas)
				if got := c.buf.CellAt(8, 3); !reflect.DeepEqual(*got, cell) {
					t.Fatalf("indexed cell changed: %+v -> %+v", cell, *got)
				}
				neighbour := c.buf.CellAt(9, 3)
				if !sameColor(neighbour.Style.Fg, towardBlack(shInk)) || !sameColor(neighbour.Style.Bg, towardBlack(shCanvas)) || !sameColor(neighbour.Style.UnderlineColor, towardBlack(shInk)) {
					t.Fatalf("truecolor neighbour did not blend: %+v", neighbour.Style)
				}
			})
		}
	}
}

func TestShadowKeepsUnstyledCellsUnderIndexedDefaults(t *testing.T) {
	for _, defaults := range [][2]color.Color{{ansi.BasicColor(15), shCanvas}, {shInk, ansi.BasicColor(0)}, {ansi.BasicColor(15), ansi.BasicColor(0)}} {
		c := shadowTestCanvas(20, 10, uv.Style{}, "·")
		before := *c.buf.CellAt(9, 4)
		c.castShadow(uv.Rect(2, 2, 6, 4), defaults[0], defaults[1])
		if got := c.buf.CellAt(9, 4); !reflect.DeepEqual(*got, before) {
			t.Errorf("unstyled cell changed under %T/%T defaults: %+v", defaults[0], defaults[1], *got)
		}
		c.release()
	}
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

// shadowViewModel is a plugin-stage workbench at 144x40 with an optional modal
// and an optional palette, and no toast.
func shadowViewModel(modal, palette bool) *WorkbenchModel {
	m := pureViewModel("plugin", false)
	if modal {
		m.plugins["p.modal"] = true
		m.railItems = append(m.railItems, contracts.RailItem{ID: "r.modal", Plugin: "p.modal", Icon: "m", Label: "modal", Placement: contracts.PlacementModal})
		m.openModal("p.modal", "r.modal")
		m.slotUpdates[protocol.SurfaceModal] = &protocol.SurfaceUpdateParams{PluginID: "p.modal", Surface: protocol.SurfaceModal, Lines: []string{"modal"}}
	}
	m.showPalette = palette
	m.paletteQuery = "a"
	m.recomputeLayout()
	return m
}

func modalPopupRect(m *WorkbenchModel) uv.Rectangle {
	r := m.modalRect
	return uv.Rect(r.X, r.Y, r.Width, r.Height)
}

func palettePopupRect(m *WorkbenchModel) uv.Rectangle {
	c := newCanvas(m.width, m.height)
	defer c.release()
	return m.drawCommandPalette(c)
}

func inShadow(popup uv.Rectangle, x, y int) bool {
	shifted := uv.Rect(popup.Min.X+popupShadowDX, popup.Min.Y+popupShadowDY, popup.Dx(), popup.Dy())
	return uv.Pos(x, y).In(shifted) && !uv.Pos(x, y).In(popup)
}

func bg8(cell uv.Cell) [3]int {
	if cell.Style.Bg == nil {
		return [3]int{-1, -1, -1}
	}
	r, g, b, _ := cell.Style.Bg.RGBA()
	return [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
}

func near(got, want [3]int) bool {
	for i := range got {
		if d := got[i] - want[i]; d < -2 || d > 2 {
			return false
		}
	}
	return true
}

// requireShadowAt asserts, against base (the same frame without this popup's
// shadow), that every cell of the shadow area of popup is darkened to the
// popupShadowStrength blend, the cells of the one-cell ring just outside the
// shadow (right of it and below it) are untouched, and probe, a cell inside
// the popup, is not darkened.
func requireShadowAt(t *testing.T, base, got uv.Lines, popup uv.Rectangle, probe [2]int, probeWant [3]int) {
	t.Helper()
	darkened := 0
	for y := range got {
		for x := range got[y] {
			if y >= len(base) || x >= len(base[y]) {
				continue
			}
			b, g := bg8(base[y][x]), bg8(got[y][x])
			var want [3]int
			for i := range b {
				want[i] = int(float64(b[i])*(1-popupShadowStrength) + 0.5)
			}
			switch {
			case inShadow(popup, x, y):
				st := base[y][x].Style
				if indexedColor(st.Fg) || indexedColor(st.Bg) || indexedColor(st.UnderlineColor) {
					if !got[y][x].Equal(&base[y][x]) {
						t.Fatalf("indexed shadow cell (%d,%d) changed", x, y)
					}
					continue
				}
				darkened++
				if !near(g, want) {
					t.Fatalf("shadow cell (%d,%d) bg = %v, want about %v (base %v)", x, y, g, want, b)
				}
				if got[y][x].Content != base[y][x].Content {
					t.Fatalf("shadow cell (%d,%d) glyph changed: %q -> %q", x, y, base[y][x].Content, got[y][x].Content)
				}
			case y > popup.Min.Y && y <= popup.Max.Y+popupShadowDY && x == popup.Max.X+popupShadowDX,
				y == popup.Max.Y+popupShadowDY && x >= popup.Min.X+popupShadowDX && x <= popup.Max.X+popupShadowDX:
				if !near(g, b) {
					t.Fatalf("cell (%d,%d) just outside the shadow was changed: %v -> %v", x, y, b, g)
				}
			}
		}
	}
	if darkened == 0 {
		t.Fatal("no shadow cells on screen")
	}
	if g := bg8(got[probe[1]][probe[0]]); !near(g, probeWant) {
		t.Fatalf("popup cell %v bg = %v, want undarkened %v", probe, g, probeWant)
	}
}

func rgb8(m *WorkbenchModel, c color.Color) [3]int {
	r, g, b, _ := c.RGBA()
	return [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
}

func TestViewModalCastsShadow(t *testing.T) {
	m := shadowViewModel(true, false)
	base := frameCells(shadowViewModel(false, false).View().Content)
	got := frameCells(m.View().Content)
	popup := modalPopupRect(m)
	r := m.modalRect
	requireShadowAt(t, base, got, popup, [2]int{r.InnerX + 1, r.InnerY + r.InnerH - 1}, rgb8(m, m.palette.Slot))
}

func TestViewPaletteCastsShadow(t *testing.T) {
	m := shadowViewModel(false, true)
	base := frameCells(shadowViewModel(false, false).View().Content)
	got := frameCells(m.View().Content)
	popup := palettePopupRect(m)
	requireShadowAt(t, base, got, popup, [2]int{popup.Min.X + 2, popup.Min.Y + 1}, rgb8(m, m.palette.Surface))
}

// A palette over a modal: the palette shadow darkens the modal (and the
// modal's own shadow) beneath it, so the overlap is darkened twice.
func TestViewPaletteOverModalStacksShadows(t *testing.T) {
	m := shadowViewModel(true, true)
	modalOnly := shadowViewModel(true, false)
	base := frameCells(modalOnly.View().Content)
	got := frameCells(m.View().Content)
	pal := palettePopupRect(m)
	mod := modalPopupRect(modalOnly)
	overlap := 0
	for y := range got {
		for x := range got[y] {
			if inShadow(pal, x, y) && (uv.Pos(x, y).In(mod) || inShadow(mod, x, y)) {
				overlap++
			}
		}
	}
	if overlap == 0 {
		t.Fatal("test setup: palette shadow does not overlap the modal shadow")
	}
	// the palette shadow darkens whatever the modal-only frame had there,
	// including cells already inside the modal's own shadow
	requireShadowAt(t, base, got, pal, [2]int{pal.Min.X + 2, pal.Min.Y + 1}, rgb8(m, m.palette.Surface))
}

func TestViewToastCastsNoShadow(t *testing.T) {
	m := shadowViewModel(false, false)
	base := frameCells(m.View().Content)
	m.activeToast = &contracts.Toast{Title: "t", Message: "m", Level: "warn"}
	got := frameCells(m.View().Content)
	toastW := min(48, m.width-6)
	x0, y0 := max(2, m.width-toastW-4), 2
	// the row below the toast box and the strip right of it are unchanged
	for _, p := range [][2]int{{x0 + toastW + 1, y0 + 2}, {x0 + 3, y0 + 4}, {x0 + toastW, y0 + 4}} {
		if !near(bg8(got[p[1]][p[0]]), bg8(base[p[1]][p[0]])) {
			t.Errorf("cell %v under a toast changed: %v -> %v", p, bg8(base[p[1]][p[0]]), bg8(got[p[1]][p[0]]))
		}
	}
}
