package workbench

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// frameCells decodes a rendered frame the way a terminal would: SGR state
// carries from one line to the next, and every cell, including the trailing
// columns a wide glyph covers (as zero-width placeholders), has its own index.
// frameCells(s)[y][x] is the cell at column x of row y.
func frameCells(s string) uv.Lines {
	parsed := uv.NewStyledString(s).Lines(ansi.GraphemeWidth)
	n := strings.Count(s, "\n") + 1
	out := make(uv.Lines, n)
	for y := 0; y < n; y++ {
		if y >= len(parsed) {
			continue
		}
		for _, c := range parsed[y] {
			out[y] = append(out[y], c)
			for i := 1; i < c.Width; i++ {
				out[y] = append(out[y], uv.Cell{})
			}
		}
	}
	return out
}

// sameColor reports whether two colours are both absent or render the same.
func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// requireSameCells fails when two frames decode to different cells.
func requireSameCells(t *testing.T, want, got string) {
	t.Helper()
	wc, gc := frameCells(want), frameCells(got)
	if len(wc) != len(gc) {
		t.Fatalf("row count: want %d, got %d", len(wc), len(gc))
	}
	for y := range wc {
		if len(wc[y]) != len(gc[y]) {
			t.Fatalf("row %d: want %d cells, got %d", y, len(wc[y]), len(gc[y]))
		}
		for x := range wc[y] {
			if !wc[y][x].Equal(&gc[y][x]) {
				t.Fatalf("cell (%d,%d): want %q %+v, got %q %+v", x, y, wc[y][x].Content, wc[y][x].Style, gc[y][x].Content, gc[y][x].Style)
			}
		}
	}
}

// Each golden scenario's current frame decodes to the same cells as its
// committed golden. The goldens were regenerated when the cell serializer
// landed, so this compares today's output with today's goldens (a decode-level
// check next to the byte-exact golden tests), not with the pre-canvas
// string compositor's output.
func TestGoldenFramesCellEquivalent(t *testing.T) {
	for name, build := range goldenFrames {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			requireSameCells(t, string(want), build(t))
		})
	}
}

// The canvas measures graphemes like ansi.StringWidth, which the layout uses:
// after a ZWJ emoji and a flag, the next glyph lands where the layout expects.
func TestCanvasWidthMethodMatchesLayout(t *testing.T) {
	for _, prefix := range []string{"👩‍💻", "🇧🇷", "👩‍💻🇧🇷", "a界"} {
		t.Run(prefix, func(t *testing.T) {
			c := newCanvas(12, 1)
			c.blitBlock(0, 0, prefix+"x")
			want := ansi.StringWidth(prefix)
			if got := c.buf.CellAt(want, 0); got == nil || got.Content != "x" {
				t.Fatalf("cell %d = %+v, want x (layout width of %q is %d)", want, got, prefix, want)
			}
			if w := ansi.StringWidth(c.render()); w != 12 {
				t.Fatalf("rendered width = %d, want 12", w)
			}
		})
	}
}

// render emits every cell: exactly h lines of exactly w cells, blanks
// included, with no style left open at the end of a line.
func TestCanvasRenderExactGeometry(t *testing.T) {
	c := newCanvas(10, 3)
	c.blitBlock(2, 1, "\x1b[41mab")
	out := c.render()
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %q", len(rows), out)
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != 10 {
			t.Fatalf("row %d width = %d, want 10: %q", i, w, r)
		}
	}
	if !strings.HasSuffix(rows[1], ansi.ResetStyle+"      ") {
		t.Fatalf("row 1 does not reset before the blanks: %q", rows[1])
	}
	cells := frameCells(out)
	for x := 0; x < 10; x++ {
		red := sameColor(cells[1][x].Style.Bg, ansi.Red)
		if red != (x == 2 || x == 3) {
			t.Fatalf("cell (%d,1) red = %v", x, red)
		}
	}
}

// blitLines parses each line from a zero pen and clears the rest of its row:
// an open style stops at the line's last glyph, never reaches the padding or
// the next line, and a missing line leaves a blank row.
func TestBlitLinesOpenStyleStopsAtContent(t *testing.T) {
	c := newCanvas(8, 4)
	c.blitBlock(0, 0, strings.Repeat("########\n", 3)+"########")
	c.blitLines(1, 0, 6, 3, []string{"\x1b[41mhi", "plain"})
	cells := frameCells(c.render())
	wantText := []string{"#hi    #", "#plain #", "#      #", "########"}
	for y, want := range wantText {
		var got strings.Builder
		for _, cell := range cells[y] {
			got.WriteString(cell.Content)
		}
		if got.String() != want {
			t.Fatalf("row %d text = %q, want %q", y, got.String(), want)
		}
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 8; x++ {
			red := sameColor(cells[y][x].Style.Bg, ansi.Red)
			if want := y == 0 && (x == 1 || x == 2); red != want {
				t.Fatalf("cell (%d,%d) red = %v, want %v", x, y, red, want)
			}
		}
	}
}

// A wide glyph that does not fit before a clip edge is dropped, not split:
// the clipped cell is blank and the cell past the edge keeps what was there.
// Covering half of a wide glyph that is already drawn blanks the whole glyph.
func TestCanvasWideGlyphAtClipEdge(t *testing.T) {
	t.Run("blit straddling the right edge", func(t *testing.T) {
		c := newCanvas(6, 1)
		c.blitBlock(0, 0, "......")
		c.blitLines(0, 0, 3, 1, []string{"a界界"})
		got := uv.Lines(c.buf.Lines).String()
		if got != "a界..." {
			t.Fatalf("row = %q, want %q", got, "a界...")
		}
	})
	t.Run("overlay over the trailing half", func(t *testing.T) {
		c := newCanvas(6, 1)
		c.blitBlock(0, 0, "a界bcd")
		c.blitBlock(2, 0, "X")
		if got := uv.Lines(c.buf.Lines).String(); got != "a Xbcd" {
			t.Fatalf("row = %q, want %q", got, "a Xbcd")
		}
	})
	t.Run("overlay over the leading half", func(t *testing.T) {
		c := newCanvas(6, 1)
		c.blitBlock(0, 0, "ab界cd")
		c.blitBlock(1, 0, "XY")
		if got := uv.Lines(c.buf.Lines).String(); got != "aXY cd" {
			t.Fatalf("row = %q, want %q", got, "aXY cd")
		}
	})
	t.Run("blit starting left of the canvas", func(t *testing.T) {
		c := newCanvas(4, 1)
		c.blitBlock(-1, 0, "界xyz")
		if got := uv.Lines(c.buf.Lines).String(); got != " xyz" {
			t.Fatalf("row = %q, want %q", got, " xyz")
		}
	})
}

// Blocks are opaque over their bounding box, and writes outside the canvas
// are dropped rather than wrapped.
func TestBlitBlockOpaqueAndClipped(t *testing.T) {
	c := newCanvas(5, 3)
	c.blitBlock(0, 0, "#####\n#####\n#####")
	c.blitBlock(3, 1, "abc\nd")
	got := uv.Lines(c.buf.Lines).String()
	if want := "#####\n###ab\n###d"; got != want {
		t.Fatalf("canvas = %q, want %q", got, want)
	}
	c.blitBlock(0, 5, "zz") // below the canvas
	if got2 := uv.Lines(c.buf.Lines).String(); got2 != got {
		t.Fatalf("write below the canvas changed it: %q", got2)
	}
}

// drawnRect runs draw on a canvas larger than rect, pre-filled with a
// sentinel, fails if draw wrote any cell outside rect, and returns rect's rows
// serialized on their own (each exactly rect.Width cells).
func drawnRect(t *testing.T, rect SlotRect, draw func(c *canvas)) []string {
	t.Helper()
	const margin = 4
	w, h := rect.X+rect.Width+margin, rect.Y+rect.Height+margin
	c := newCanvas(w, h)
	c.blitBlock(0, 0, strings.TrimSuffix(strings.Repeat(strings.Repeat("·", w)+"\n", h), "\n"))
	draw(c)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= rect.X && x < rect.X+rect.Width && y >= rect.Y && y < rect.Y+rect.Height {
				continue
			}
			if cell := c.buf.CellAt(x, y); cell == nil || cell.Content != "·" {
				t.Fatalf("draw wrote cell (%d,%d) outside rect %+v: %+v", x, y, rect, cell)
			}
		}
	}
	rows := make([]string, rect.Height)
	for y := range rows {
		rows[y] = uv.Line(c.buf.Lines[rect.Y+y][rect.X : rect.X+rect.Width]).Render()
	}
	return rows
}
