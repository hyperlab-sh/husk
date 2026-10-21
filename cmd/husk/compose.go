package main

import (
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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
func (c *canvas) blitBlock(x, y int, s string) {
	if s == "" {
		return
	}
	w, h := 0, 0
	for line := range strings.SplitSeq(s, "\n") {
		w = max(w, c.method.StringWidth(line))
		h++
	}
	c.draw(uv.Rect(x, y, w, h), s)
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

// render serializes every cell: each line is exactly the canvas width and
// the frame is exactly its height. Unlike lipgloss.Canvas it does not trim
// trailing blanks.
func (c *canvas) render() string {
	return uv.Lines(c.buf.Lines).Render()
}
