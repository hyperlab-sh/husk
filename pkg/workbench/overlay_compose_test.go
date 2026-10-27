package workbench

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestBlitOverlay(t *testing.T) {
	for _, tc := range []struct {
		name, base, line, want string
		regions                []regionDraw
		check                  func(*testing.T, *canvas)
	}{
		{name: "plain spaces and omitted rows", base: "abcdef", line: " X", want: "aXcdef", check: func(t *testing.T, c *canvas) {
			if c.buf.CellAt(0, 1).Content != "b" {
				t.Fatal("missing row not transparent")
			}
		}},
		{name: "inherited background and replaced style", base: "\x1b[1;41mabcdef", line: " \x1b[32mX", want: "aXcdef", check: func(t *testing.T, c *canvas) {
			cell := c.buf.CellAt(1, 0)
			if !sameColor(cell.Style.Bg, ansi.Red) || !sameColor(cell.Style.Fg, ansi.Green) || cell.Style.Attrs != 0 {
				t.Fatalf("replacement style = %+v", cell.Style)
			}
		}},
		{name: "explicit background space", base: "abcdef", line: " \x1b[41m ", want: "a cdef", check: func(t *testing.T, c *canvas) {
			if !sameColor(c.buf.CellAt(1, 0).Style.Bg, ansi.Red) {
				t.Fatal("explicit background lost")
			}
		}},
		{name: "attribute space", base: "abcdef", line: " \x1b[1m ", want: "a cdef"},
		{name: "foreground space", base: "abcdef", line: " \x1b[32m ", want: "a cdef"},
		{name: "underline space", base: "abcdef", line: " \x1b[4m ", want: "a cdef"},
		{name: "linked space", base: "abcdef", line: " \x1b]8;;https://example.test\x1b\\ \x1b]8;;\x1b\\", want: "a cdef"},
		{name: "reset space", base: "abcdef", line: " \x1b[0m ", want: "abcdef"},
		{name: "transparent trailing half", base: "a界bcd", line: "  ", want: "a界bcd"},
		{name: "replace wide trailing half", base: "a\x1b[41m界\x1b[0mbcd", line: "  X", want: "a Xbcd", check: func(t *testing.T, c *canvas) {
			for _, x := range []int{1, 2} {
				if !sameColor(c.buf.CellAt(x, 0).Style.Bg, ansi.Red) {
					t.Fatal("split glyph background lost")
				}
			}
		}},
		{name: "replace wide leading half", base: "a\x1b[41m界\x1b[0mbcd", line: " X", want: "aX bcd", check: func(t *testing.T, c *canvas) {
			if !sameColor(c.buf.CellAt(2, 0).Style.Bg, ansi.Red) {
				t.Fatal("uncovered half background lost")
			}
		}},
		{name: "wide replaces two glyphs", base: "abcdef", line: " 界", want: "a界def"},
		{name: "wide clips at right edge", base: "abcdef", line: "     界", want: "abcdef"},
		{name: "wide crosses underlying wide", base: "a界界b", line: "  界", want: "a 界 b"},
		{name: "region clears blank cells", base: "abcdef", line: "", regions: []regionDraw{{X: 1, Y: 0, W: 3, H: 1, Lines: []string{"R"}}}, want: "aR  ef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanvas(6, 2)
			defer c.release()
			c.blitLines(0, 0, 6, 2, []string{tc.base, "bbbbbb"})
			c.blitOverlay(publishedLayout{Width: 6, Height: 2, Rows: []string{tc.line}, Regions: tc.regions})
			if got := uv.Lines(c.buf.Lines).String(); got != tc.want+"\nbbbbbb" {
				t.Fatalf("got %q, want %q", got, tc.want+"\nbbbbbb")
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}
