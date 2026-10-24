package workbench

import (
	"reflect"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// cellSGR is the SGR parameter string that sets a decoded cell's style from
// the default rendition ("" for the default itself), e.g. "41" or "1".
func cellSGR(c uv.Cell) string {
	seq := uv.StyleDiff(&uv.Style{}, &c.Style)
	return strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
}

// rowText and rowSGR read one decoded row's text and per-cell styles.
func rowText(row uv.Line) string {
	var b strings.Builder
	for _, c := range row {
		b.WriteString(c.Content)
	}
	return b.String()
}

func rowSGR(row uv.Line) []string {
	out := make([]string, len(row))
	for i, c := range row {
		out[i] = cellSGR(c)
	}
	return out
}

// No graphic rendition may cross an overlay boundary: overlay cells carry
// only the overlay's styles and base cells keep exactly the style they had.
func TestSpliceOverlayNoSGRBleed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    string
		overlay string
		x       int
		want    []string
	}{
		{
			name: "base style left open", base: "\x1b[41mabcdefghij", overlay: "XY", x: 3,
			want: []string{"41", "41", "41", "", "", "41", "41", "41", "41", "41"},
		},
		{
			// A string splice cut with ansi.Cut kept escapes past the cut;
			// the canvas never sees them, but the case stays pinned.
			name: "style opening after the cut", base: "abc\x1b[44mdefghij", overlay: "XY", x: 1,
			want: []string{"", "", "", "44", "44", "44", "44", "44", "44", "44"},
		},
		{
			name: "overlay left open", base: "abcdefghij", overlay: "\x1b[42mXY", x: 3,
			want: []string{"", "", "", "42", "42", "", "", "", "", ""},
		},
		{
			name: "styled overlay over styled base", base: "\x1b[41mabcdefghij\x1b[m", overlay: "\x1b[1mXY\x1b[m", x: 3,
			want: []string{"41", "41", "41", "1", "1", "41", "41", "41", "41", "41"},
		},
		{
			name: "style changes inside the overlay span", base: "\x1b[41mabcd\x1b[m\x1b[44mefghij", overlay: "XYZ", x: 3,
			want: []string{"41", "41", "41", "", "", "", "44", "44", "44", "44"},
		},
		{
			name: "overlay at column 0", base: "\x1b[45mabcdef", overlay: "XY", x: 0,
			want: []string{"", "", "45", "45", "45", "45"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ansi.StringWidth(tc.base)
			c := newCanvas(w, 3)
			c.blitBlock(0, 0, "top")
			c.blitBlock(0, 1, tc.base)
			c.blitBlock(0, 2, "bottom")
			c.blitBlock(tc.x, 1, tc.overlay)
			rows := frameCells(c.render())
			if len(rows) != 3 || strings.TrimRight(rowText(rows[0]), " ") != "top" || strings.TrimRight(rowText(rows[2]), " ") != "bottom" {
				t.Fatalf("rows outside the overlay changed: %q / %q", rowText(rows[0]), rowText(rows[2]))
			}
			if got, want := rowText(rows[1]), ansi.Strip(tc.base)[:tc.x]+ansi.Strip(tc.overlay)+ansi.Strip(tc.base)[tc.x+ansi.StringWidth(tc.overlay):]; got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
			if got := rowSGR(rows[1]); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cell styles = %q, want %q", got, tc.want)
			}
		})
	}
}

// Well-formed fragments (closed styles) compose to exactly the cells the old
// byte splice produced: the overlay's own styles inside its span, the base's
// styles outside it.
func TestSpliceOverlayWellFormedBytesUnchanged(t *testing.T) {
	base := "\x1b[41mabcdef\x1b[m" + "ghij"
	ov := "\x1b[1mXY\x1b[m"
	want := ansi.Cut(base, 0, 3) + ov + ansi.Cut(base, 5, 10)
	c := newCanvas(10, 1)
	c.blitBlock(0, 0, base)
	c.blitBlock(3, 0, ov)
	got := c.render()
	requireSameCells(t, want, got)
	if s := rowSGR(frameCells(got)[0]); !reflect.DeepEqual(s, []string{"41", "41", "41", "1", "1", "41", "", "", "", ""}) {
		t.Fatalf("cell styles = %q", s)
	}
}

func TestRegionCompositorNoSGRBleed(t *testing.T) {
	r := newPublicationRegistry()
	if err := r.claimSurface("a", protocol.SurfaceMainStage); err != nil {
		t.Fatal(err)
	}
	if err := r.declareSlot("a", slotDecl{ID: "a.region", Kind: "region", Rect: &contracts.PublishedRect{Surface: protocol.SurfaceMainStage, X: 2, Y: 0, Width: 4, Height: 2}}); err != nil {
		t.Fatal(err)
	}
	// row 0: the region's text leaves green open; row 1: region text is plain
	mustContribute(t, r, "b", "a.region", "b.region", "\x1b[42mRR\nPP")
	source := []string{"\x1b[41mabcdefghij", "abc\x1b[44mdefghij"}
	rows, _ := r.renderPublished(protocol.SurfaceMainStage, "a", source, 10, 3)
	want := [][]string{
		{"41", "41", "42", "42", "", "", "41", "41", "41", "41"},
		{"", "", "", "", "", "", "44", "44", "44", "44"},
		{"", "", "", "", "", "", "", "", "", ""},
	}
	cells := frameCells(strings.Join(rows, "\n"))
	if len(cells) != 3 {
		t.Fatalf("rows = %d, want 3", len(cells))
	}
	for i, row := range cells {
		if len(row) != 10 {
			t.Fatalf("row %d width = %d, want 10", i, len(row))
		}
		if got := rowSGR(row); !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("row %d cell styles = %q, want %q\nline %q", i, got, want[i], rows[i])
		}
	}
	if got := rowText(cells[0]); got != "abRR  ghij" {
		t.Fatalf("row 0 text = %q", got)
	}
}

// A plugin line that leaves a style open must not paint the padding after it.
func TestPublishedLineOpenStyleStopsAtContent(t *testing.T) {
	r := newPublicationRegistry()
	rows, _ := r.renderPublished(protocol.SurfaceMainStage, "a", []string{"\x1b[41mhi", "\x1b[41mtoo-long-line"}, 6, 2)
	cells := frameCells(strings.Join(rows, "\n"))
	if got, want := rowSGR(cells[0]), []string{"41", "41", "", "", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("padded row styles = %q, want %q", got, want)
	}
	if got, want := rowText(cells[1]), "too-l…"; got != want {
		t.Fatalf("truncated row = %q, want %q", got, want)
	}

	// Slot content keeps an open style in the line string; blitting it stops
	// the style at the last content cell, so the rest of the row stays
	// unstyled.
	m := newWorkbenchModel()
	lines := m.surfaceContentLines(&protocol.SurfaceUpdateParams{Lines: []string{"\x1b[41mhi"}}, 4, 1)
	c := newCanvas(6, 1)
	c.blitLines(1, 0, 4, 1, lines)
	got := rowSGR(frameCells(c.render())[0])
	c.release()
	if want := []string{"", "41", "41", "", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("blitted surfaceContentLines styles = %q, want %q", got, want)
	}
}

// surfaceContentLines clips: lines past the height are dropped and wide lines
// are truncated to the width; nothing wraps or scrolls.
func TestSurfaceContentLinesClips(t *testing.T) {
	m := newWorkbenchModel()
	lines := m.surfaceContentLines(&protocol.SurfaceUpdateParams{Lines: []string{"one", "two-is-long", "three"}}, 5, 2)
	if want := []string{"one", "two-…"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %q, want %q", lines, want)
	}
}

// End to end: a main-stage plugin whose lines leave a red background open
// must not tint the command palette drawn over it, the frame edges or the
// side panel. Red may appear only on stage content cells the palette leaves
// visible.
func TestCommandPaletteOverStyledStageNoBleed(t *testing.T) {
	m := newWorkbenchModel()
	m.width, m.height = 144, 40
	m.plugins["p.stage"] = true
	m.catalog.Occupants[protocol.SurfaceMainStage] = "p.stage"
	m.recomputeLayout()
	stage := m.slotRects[protocol.SurfaceMainStage]
	var lines []string
	for i := 0; i < stage.InnerH; i++ {
		lines = append(lines, "\x1b[41m"+strings.Repeat("s", stage.InnerW+20))
	}
	m.slotUpdates[protocol.SurfaceMainStage] = &protocol.SurfaceUpdateParams{PluginID: "p.stage", Surface: protocol.SurfaceMainStage, Lines: lines}
	m.showPalette = true

	// Locate the palette box: draw it on an empty canvas and measure the
	// cells it wrote.
	pc := newCanvas(m.width, m.height)
	m.drawCommandPalette(pc)
	blank := frameCells(pc.render())
	boxX, boxY := max(2, (m.width-min(82, m.width-8))/2), max(3, m.height/6)
	boxW, boxH := ansi.StringWidth(strings.TrimRight(rowText(blank[boxY]), " "))-boxX, 0
	for y := boxY; y < len(blank) && strings.TrimSpace(rowText(blank[y])) != ""; y++ {
		boxH++
	}
	if boxW == 0 || boxH == 0 {
		t.Fatal("could not locate the palette box")
	}

	// Decode the frame into cells: the serializer writes style transitions as
	// off codes and replacement colours, which a parameter accumulator would
	// misread, so the check runs on each cell's own decoded background.
	cells := frameCells(m.View().Content)
	red := 0
	for y, row := range cells {
		for x, cell := range row {
			if !sameColor(cell.Style.Bg, ansi.Red) {
				continue
			}
			inStage := x >= stage.InnerX && x < stage.InnerX+stage.InnerW && y >= stage.InnerY && y < stage.InnerY+stage.InnerH
			inBox := x >= boxX && x < boxX+boxW && y >= boxY && y < boxY+boxH
			if !inStage || inBox {
				t.Fatalf("red background bled to cell (%d,%d) (stage %+v, palette at %d,%d %dx%d)", x, y, stage, boxX, boxY, boxW, boxH)
			}
			red++
		}
	}
	if red == 0 {
		t.Fatal("stage content was not rendered with its own style")
	}
}
