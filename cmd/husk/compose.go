package main

import (
	"image/color"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
)

// canvas is the cell grid one View call composes the frame into. Surfaces,
// bars and overlays are written in z order; each write parses its own string
// from a zero pen, so a style a fragment leaves open stays on the cells that
// fragment wrote. The canvas is serialized once, at the end of View, and is
// never stored on the model.
type canvas struct {
	buf *uv.Buffer
	// method measures graphemes the way the layout does (ansi.StringWidth is
	// grapheme based). Ultraviolet's screen buffers default to wcwidth, which
	// disagrees on emoji ZWJ sequences and flags.
	method ansi.Method
	w, h   int
	// shadowed records the popup rects whose shadow this frame already cast, so
	// drawing the same popup twice darkens the cells beneath once.
	shadowed map[uv.Rectangle]bool
}

// canvasBuffers recycles cell buffers between frames: a 144x40 buffer is
// about half a megabyte of cells, and allocating one per View call is most of
// the compositor's garbage. The pool is package state, not model state; a
// buffer is only reachable from the canvas that took it until release.
var canvasBuffers sync.Pool

func newCanvas(w, h int) *canvas {
	w, h = max(0, w), max(0, h)
	if b, ok := canvasBuffers.Get().(*uv.Buffer); ok && b.Width() == w && b.Height() == h {
		for _, line := range b.Lines {
			for i := range line {
				line[i] = uv.EmptyCell
			}
		}
		return &canvas{buf: b, method: ansi.GraphemeWidth, w: w, h: h}
	}
	return &canvas{buf: uv.NewBuffer(w, h), method: ansi.GraphemeWidth, w: w, h: h}
}

// release returns the canvas's buffer to the pool. The canvas must not be
// used afterwards.
func (c *canvas) release() {
	canvasBuffers.Put(c.buf)
	c.buf = nil
}

// clipScreen is the canvas seen through a clip rectangle. Ultraviolet's
// StyledString draws into it; writes outside clip are dropped, and a wide
// glyph that would cross the clip's right edge is not drawn (its cells are
// blanked), so no half glyph is ever left behind.
type clipScreen struct {
	c    *canvas
	clip uv.Rectangle
}

func (s clipScreen) Bounds() uv.Rectangle            { return s.clip }
func (s clipScreen) CellAt(x, y int) *uv.Cell        { return s.c.buf.CellAt(x, y) }
func (s clipScreen) WidthMethod() uv.WidthMethod     { return s.c.method }
func (s clipScreen) SetCell(x, y int, cell *uv.Cell) { s.c.setClipped(s.clip, x, y, cell) }

func (c *canvas) setClipped(clip uv.Rectangle, x, y int, cell *uv.Cell) {
	if !uv.Pos(x, y).In(clip) {
		return
	}
	if cell != nil && x+cell.Width > clip.Max.X {
		for i := x; i < clip.Max.X; i++ {
			c.buf.SetCell(i, y, nil)
		}
		return
	}
	c.buf.SetCell(x, y, cell)
}

// draw clears area and draws s into it from a zero pen, clipped to area and
// to the canvas. A newline moves to the start of the next row of area; a
// style left open carries across it, as it would on a terminal.
func (c *canvas) draw(area uv.Rectangle, s string) {
	clip := area.Intersect(c.buf.Bounds())
	if clip.Empty() {
		return
	}
	uv.NewStyledString(s).Draw(clipScreen{c: c, clip: clip}, area)
}

// blitBlock draws a multi-line string with its top-left cell at (x, y). The
// block is opaque: every cell of its bounding box (widest line by line count)
// is replaced, including cells a shorter line leaves unwritten.
//
// It returns the rectangle the block occupies (empty for an empty string), which
// callers pass to castShadow.
func (c *canvas) blitBlock(x, y int, s string) uv.Rectangle {
	if s == "" {
		return uv.Rectangle{}
	}
	w, h := 0, 0
	for line := range strings.SplitSeq(s, "\n") {
		w = max(w, c.method.StringWidth(line))
		h++
	}
	r := uv.Rect(x, y, w, h)
	c.draw(r, s)
	return r
}

// blitLines draws lines into the w x h rectangle at (x, y), one line at a
// time: each line is parsed from a zero pen and clipped to w, so a style a
// line leaves open stops at its last character. Rows without a line are
// cleared.
func (c *canvas) blitLines(x, y, w, h int, lines []string) {
	for i := 0; i < h; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		c.draw(uv.Rect(x, y+i, w, 1), line)
	}
}

// blitOverlay composites a whole-frame layer. Only unstyled, unlinked spaces
// in the occupant's own rows are transparent; published regions are opaque.
// Opaque cells inherit the background below unless they supply their own.
func (c *canvas) blitOverlay(l publishedLayout) {
	scratch := newCanvas(c.w, c.h)
	defer scratch.release()
	l.draw(scratch, 0, 0)
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			cell := scratch.buf.CellAt(x, y)
			if cell.Width == 0 {
				continue // continuation of a wide overlay glyph already copied
			}
			region := false
			for _, r := range l.Regions {
				if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
					region = true
					break
				}
			}
			if !region && cell.Content == " " && cell.Style.IsZero() && cell.Link.IsZero() {
				continue
			}
			next := *cell
			// Blank each underlying wide glyph before replacing any of its cells.
			// Keep only its background on the uncovered half, not the old
			// foreground, attributes or link. Also handle the far end of an
			// overlay-wide glyph crossing a second underlying wide glyph.
			for i := x; i < x+next.Width; i++ {
				lead := i
				for lead > 0 && c.buf.CellAt(lead, y).Width == 0 {
					lead--
				}
				under := *c.buf.CellAt(lead, y)
				if i == x && next.Style.Bg == nil {
					next.Style.Bg = under.Style.Bg
				}
				if under.Width > 1 {
					blank := uv.EmptyCell
					blank.Style.Bg = under.Style.Bg
					for j := lead; j < lead+under.Width; j++ {
						c.buf.SetCell(j, y, &blank)
					}
				}
			}
			c.buf.SetCell(x, y, &next)
		}
	}
}

// render serializes every cell: each line is exactly the canvas width and
// the frame is exactly its height. Unlike lipgloss.Canvas it does not trim
// trailing blanks.
func (c *canvas) render() string {
	return uv.Lines(c.buf.Lines).Render()
}

// A popup casts a shadow offset popupShadowDX columns right and popupShadowDY
// rows down. Cells under it keep their glyphs and attributes; their colours are
// blended popupShadowStrength of the way to black (0 leaves them, 1 is black),
// so a rule passing under the shadow turns dark instead of being erased. Black
// is the right target on light themes too: a shadow darkens.
const (
	popupShadowDX       = 2
	popupShadowDY       = 1
	popupShadowStrength = 0.65
)

// castShadow darkens the cells of popup's shadow: popup shifted by
// (popupShadowDX, popupShadowDY), minus popup itself, clipped to the canvas.
// A cell with no foreground or background of its own is resolved against ink
// and canvas first, so default-colour plugin cells darken like painted ones.
// A wide glyph is darkened whole when the shadow covers either half. Casting
// the same popup rect again in one frame is a no-op; a different rect (a nested
// popup) darkens further. Call it right after the popup is drawn.
func (c *canvas) castShadow(popup uv.Rectangle, ink, canvas color.Color) {
	if popup.Empty() || c.shadowed[popup] {
		return
	}
	if c.shadowed == nil {
		c.shadowed = map[uv.Rectangle]bool{}
	}
	c.shadowed[popup] = true
	bounds := c.buf.Bounds()
	for y := popup.Min.Y + popupShadowDY; y < popup.Max.Y+popupShadowDY; y++ {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		lo, hi := popup.Min.X+popupShadowDX, popup.Max.X+popupShadowDX
		inPopup := y < popup.Max.Y
		if inPopup {
			lo = max(lo, popup.Max.X) // the popup's own cells are not shadowed
		}
		lo, hi = max(lo, bounds.Min.X), min(hi, bounds.Max.X)
		if lo >= hi {
			continue
		}
		// Do not split a wide glyph: take in the lead cell when the span starts
		// on its continuation, and the continuation when it ends on a lead.
		if cell := c.buf.CellAt(lo, y); cell != nil && cell.Width == 0 && lo > bounds.Min.X && !(inPopup && lo-1 < popup.Max.X) {
			lo--
		}
		if cell := c.buf.CellAt(hi-1, y); cell != nil && cell.Width > 1 {
			hi = min(hi-1+cell.Width, bounds.Max.X)
		}
		for x := lo; x < hi; x++ {
			if cell := c.buf.CellAt(x, y); cell != nil {
				darken(cell, ink, canvas)
			}
		}
	}
}

// darken blends a cell's resolved foreground and background (and underline
// colour, when it has one) toward black. If any resolved colour is indexed,
// the whole cell is left untouched: only the terminal knows its actual colour.
func darken(cell *uv.Cell, ink, canvas color.Color) {
	fg, bg := cell.Style.Fg, cell.Style.Bg
	if fg == nil {
		fg = ink
	}
	if bg == nil {
		bg = canvas
	}
	if indexedColor(fg) || indexedColor(bg) || indexedColor(cell.Style.UnderlineColor) {
		return
	}
	cell.Style.Fg = towardBlack(fg)
	cell.Style.Bg = towardBlack(bg)
	if cell.Style.UnderlineColor != nil {
		cell.Style.UnderlineColor = towardBlack(cell.Style.UnderlineColor)
	}
}

func indexedColor(c color.Color) bool {
	switch c.(type) {
	case ansi.BasicColor, ansi.IndexedColor:
		return true
	default:
		return false
	}
}

func towardBlack(c color.Color) color.Color {
	cc, ok := colorful.MakeColor(c)
	if !ok {
		return c
	}
	return cc.BlendRgb(colorful.Color{}, popupShadowStrength).Clamped()
}

// fillDefaultBg gives every cell of area that has no background of its own the
// background bg. A cell a plugin drew with an explicit background (any colour,
// including one that happens to equal bg) is left exactly as it is, as is a
// reverse-video cell (its background is the terminal's to swap). Only
// the Bg field changes, so no other attribute of a cell moves and no
// rendition can reach a neighbouring cell. Cells outside the canvas are
// ignored.
func (c *canvas) fillDefaultBg(area uv.Rectangle, bg color.Color) {
	area = area.Intersect(c.buf.Bounds())
	if area.Empty() || bg == nil {
		return
	}
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			cell := c.buf.CellAt(x, y)
			if cell == nil || cell.Style.Bg != nil || cell.Style.Attrs&uv.AttrReverse != 0 {
				continue
			}
			cell.Style.Bg = bg
		}
	}
}
