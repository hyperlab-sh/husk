package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// cellStyles emulates a terminal's SGR state over one rendered line and
// returns, for every printable cell, the SGR parameters active when that cell
// was written (joined with ";", "" for the default rendition). A full reset
// (ESC[m or a 0 parameter) clears the state; other parameters accumulate in
// order. Non-SGR escape sequences (OSC, other CSI) are skipped.
func cellStyles(t *testing.T, line string) []string {
	t.Helper()
	var active []string
	var cells []string
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) {
			switch line[i+1] {
			case '[':
				j := i + 2
				for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
					j++
				}
				if j >= len(line) {
					t.Fatalf("unterminated CSI in %q", line)
				}
				params := line[i+2 : j]
				if line[j] == 'm' && !strings.ContainsAny(params, "<=>?") {
					if params == "" {
						active = nil
					}
					toks := strings.Split(params, ";")
					for k := 0; params != "" && k < len(toks); k++ {
						switch toks[k] {
						case "", "0":
							active = nil
						case "38", "48", "58":
							n := 1
							if k+1 < len(toks) && toks[k+1] == "5" {
								n = 3
							} else if k+1 < len(toks) && toks[k+1] == "2" {
								n = 5
							}
							end := min(len(toks), k+n)
							active = append(active, strings.Join(toks[k:end], ";"))
							k = end - 1
						default:
							active = append(active, toks[k])
						}
					}
				}
				i = j + 1
			case ']':
				j := i + 2
				for j < len(line) && line[j] != 0x07 && !(line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\') {
					j++
				}
				if j < len(line) && line[j] == 0x07 {
					i = j + 1
				} else {
					i = j + 2
				}
			default:
				i += 2
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		for w := ansi.StringWidth(string(r)); w > 0; w-- {
			cells = append(cells, strings.Join(active, ";"))
		}
		i += size
	}
	return cells
}

func hasSGR(style, param string) bool {
	for _, p := range strings.Split(style, ";") {
		if p == param {
			return true
		}
	}
	return false
}

func TestSgrOpen(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"plain", false},
		{"\x1b[41mx", true},
		{"\x1b[41mx\x1b[m", false},
		{"\x1b[41mx\x1b[0m", false},
		{"\x1b[1;0mx", false},
		{"\x1b[0;1mx", true},
		{"\x1b[38;2;0;0;0mx", true},    // the 0 components are not a reset
		{"\x1b[48;5;0mx", true},        // nor is a 256-colour index 0
		{"\x1b[38;2;0;0;0;0mx", false}, // a trailing 0 after the colour is
		{"\x1b[38:2::0:0:0mx", true},
		{"\x1b[>4;1mx", false}, // XTMODKEYS, not SGR
		{"\x1b]8;;http://x\x1b\\x", false},
	} {
		if got := sgrOpen(tc.in); got != tc.want {
			t.Errorf("sgrOpen(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := closeSGR("\x1b[41mx\x1b[m"); got != "\x1b[41mx\x1b[m" {
		t.Errorf("closed fragment changed: %q", got)
	}
	if got := closeSGR("\x1b[41mx"); got != "\x1b[41mx\x1b[m" {
		t.Errorf("open fragment not closed: %q", got)
	}
}

// No graphic rendition may cross a splice boundary: overlay cells carry only
// the overlay's styles and base cells keep exactly the style they had.
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
			// ansi.Cut keeps escapes past the cut, so the left fragment used
			// to end with the style that opens at column 3.
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
			out := spliceOverlay("top\n"+tc.base+"\nbottom", tc.overlay, tc.x, 1)
			rows := strings.Split(out, "\n")
			if len(rows) != 3 || rows[0] != "top" || rows[2] != "bottom" {
				t.Fatalf("rows outside the overlay changed: %q", rows)
			}
			if got, want := ansi.Strip(rows[1]), ansi.Strip(tc.base)[:tc.x]+ansi.Strip(tc.overlay)+ansi.Strip(tc.base)[tc.x+ansi.StringWidth(tc.overlay):]; got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
			if got := cellStyles(t, rows[1]); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cell styles = %q, want %q\nline %q", got, tc.want, rows[1])
			}
		})
	}
}

// Well-formed fragments (closed styles) splice byte-for-byte as before.
func TestSpliceOverlayWellFormedBytesUnchanged(t *testing.T) {
	base := "\x1b[41mabcdef\x1b[m" + "ghij"
	ov := "\x1b[1mXY\x1b[m"
	want := ansi.Cut(base, 0, 3) + ov + ansi.Cut(base, 5, 10)
	if got := spliceOverlay(base, ov, 3, 0); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRegionCompositorNoSGRBleed(t *testing.T) {
	r := newPublicationRegistry()
	if err := r.claimSurface("a", huskipc.SlotMainStage); err != nil {
		t.Fatal(err)
	}
	if err := r.declareSlot("a", huskipc.SlotDeclarePayload{ID: "a.region", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, X: 2, Y: 0, Width: 4, Height: 2}}); err != nil {
		t.Fatal(err)
	}
	// row 0: the region's text leaves green open; row 1: region text is plain
	mustContribute(t, r, "b", "a.region", "b.region", "\x1b[42mRR\nPP")
	source := []string{"\x1b[41mabcdefghij", "abc\x1b[44mdefghij"}
	rows, _ := r.renderPublished(huskipc.SlotMainStage, "a", source, 10, 3)
	want := [][]string{
		{"41", "41", "42", "42", "", "", "41", "41", "41", "41"},
		{"", "", "", "", "", "", "44", "44", "44", "44"},
		{"", "", "", "", "", "", "", "", "", ""},
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != 10 {
			t.Fatalf("row %d width = %d, want 10", i, w)
		}
		if got := cellStyles(t, row); !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("row %d cell styles = %q, want %q\nline %q", i, got, want[i], row)
		}
	}
	if got := ansi.Strip(rows[0]); got != "abRR  ghij" {
		t.Fatalf("row 0 text = %q", got)
	}
}

// A plugin line that leaves a style open must not paint the padding after it.
func TestPublishedLineOpenStyleStopsAtContent(t *testing.T) {
	r := newPublicationRegistry()
	rows, _ := r.renderPublished(huskipc.SlotMainStage, "a", []string{"\x1b[41mhi", "\x1b[41mtoo-long-line"}, 6, 2)
	if got, want := cellStyles(t, rows[0]), []string{"41", "41", "", "", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("padded row styles = %q, want %q", got, want)
	}
	if sgrOpen(rows[1]) {
		t.Fatalf("truncated row leaves a style open: %q", rows[1])
	}
	m := newWorkbenchModel("", nil, "")
	lines := m.surfaceContentLines(&huskipc.SlotUpdatePayload{Lines: []string{"\x1b[41mhi"}}, 4, 1)
	if got, want := cellStyles(t, lines[0]), []string{"41", "41", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surfaceContentLines styles = %q, want %q", got, want)
	}
}

// surfaceContentLines clips: lines past the height are dropped and wide lines
// are truncated to the width; nothing wraps or scrolls.
func TestSurfaceContentLinesClips(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	lines := m.surfaceContentLines(&huskipc.SlotUpdatePayload{Lines: []string{"one", "two-is-long", "three"}}, 5, 2)
	if want := []string{"one  ", "two-…"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %q, want %q", lines, want)
	}
}

// End to end: a main-stage plugin whose lines leave a red background open
// must not tint the command palette drawn over it, the frame edges or the
// side panel. Red may appear only on stage content cells the palette leaves
// visible.
func TestCommandPaletteOverStyledStageNoBleed(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	m.width, m.height = 144, 40
	m.plugins["p.stage"] = &ConnectedPlugin{Info: huskipc.HelloPayload{PluginID: "p.stage"}}
	m.slotOwners[huskipc.SlotMainStage] = "p.stage"
	m.recomputeLayout()
	stage := m.slotRects[huskipc.SlotMainStage]
	var lines []string
	for i := 0; i < stage.InnerH; i++ {
		lines = append(lines, "\x1b[41m"+strings.Repeat("s", stage.InnerW+20))
	}
	m.slotUpdates[huskipc.SlotMainStage] = &huskipc.SlotUpdatePayload{PluginID: "p.stage", Slot: huskipc.SlotMainStage, Lines: lines}
	m.showPalette = true

	// Locate the palette box: splice it over an empty base and measure.
	blank := strings.Split(m.overlayCommandPalette(strings.Repeat("\n", m.height-1)), "\n")
	boxX, boxY := max(2, (m.width-min(82, m.width-8))/2), max(3, m.height/6)
	boxW, boxH := ansi.StringWidth(blank[boxY]), 0
	for y := boxY; y < len(blank) && blank[y] != ""; y++ {
		boxH++
	}
	if boxW == 0 || boxH == 0 {
		t.Fatal("could not locate the palette box")
	}

	rows := strings.Split(m.View().Content, "\n")
	red := 0
	for y, row := range rows {
		for x, style := range cellStyles(t, row) {
			if !hasSGR(style, "41") {
				continue
			}
			inStage := x >= stage.InnerX && x < stage.InnerX+stage.InnerW && y >= stage.InnerY && y < stage.InnerY+stage.InnerH
			inBox := x >= boxX && x < boxX+boxW && y >= boxY && y < boxY+boxH
			if !inStage || inBox {
				t.Fatalf("red background bled to cell (%d,%d) (stage %+v, palette at %d,%d %dx%d)\nrow %q", x, y, stage, boxX, boxY, boxW, boxH, row)
			}
			red++
		}
	}
	if red == 0 {
		t.Fatal("stage content was not rendered with its own style")
	}
}
