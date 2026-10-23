package guide

import (
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The expected legend geometry and column widths follow the spec independently
// of the renderer. Include missing surfaces as unnumbered rows.
func legendSurfaces() []protocol.SurfaceID {
	return []protocol.SurfaceID{protocol.SurfaceActivityRail, protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList, protocol.SurfaceThreadRowStatus, protocol.SurfaceSidebarFooter, protocol.SurfaceMainHeader, protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceModal, protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage}
}
func legendBounds(l layout) rect {
	rows := len(l.Geometry.Rects)
	if _, ok := l.Geometry.Rects[protocol.SurfaceAppOverlay]; ok {
		rows--
	}
	for _, id := range legendSurfaces() {
		if _, ok := l.Geometry.Rects[id]; !ok {
			rows++
		}
	}
	w, h := l.Geometry.Terminal.Width, l.Geometry.Terminal.Height
	width, height := min(64, w-4), rows+3
	return rect{w - 2 - width, h - 1 - height, width, height}
}
func inside(r rect, x, y int) bool {
	return x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}
func assertTextAt(t *testing.T, grid [][]cell, x, y int, text, fg string) {
	t.Helper()
	for i, r := range []rune(text) {
		if y < 0 || y >= len(grid) || x+i < 0 || x+i >= len(grid[y]) {
			t.Fatalf("text %q out of frame at %d,%d", text, x+i, y)
		}
		got := grid[y][x+i]
		if got.text != string(r) || got.fg != fg {
			t.Fatalf("text %q at %d,%d: %+v, want %q with %s", text, x+i, y, got, string(r), fg)
		}
	}
}
func assertLegendRow(t *testing.T, l layout, grid [][]cell, id protocol.SurfaceID, owner, key string) {
	t.Helper()
	var widths [4]int
	for _, surface := range legendSurfaces() {
		value := l.Surfaces.Occupants[surface].Plugin
		n := ""
		if _, visible := l.Geometry.Rects[surface]; visible {
			n = "①"
			if value == "" {
				value = "empty"
			}
		} else if surface == protocol.SurfaceThreadRowStatus {
			value = "not placed"
		} else {
			value = "hidden"
		}
		fields := []string{n, surface.Humanised(), string(surface), value}
		for i, f := range fields {
			widths[i] = max(widths[i], len([]rune(f)))
		}
	}
	legend := legendBounds(l)
	idX := legend.X + 1 + widths[0] + widths[1] + 4
	ownerX := idX + widths[2] + 2
	for y := legend.Y + 1; y < legend.Y+legend.Height-2; y++ {
		var row strings.Builder
		for _, c := range grid[y] {
			row.WriteString(c.text)
		}
		if !strings.Contains(row.String(), string(id)) {
			continue
		}
		assertTextAt(t, grid, idX, y, string(id), "38;2;51;68;85")
		assertTextAt(t, grid, ownerX, y, owner, "38;2;68;85;102")
		assertTextAt(t, grid, ownerX-1, y, " ", "38;2;51;68;85")
		assertTextAt(t, grid, ownerX+len([]rune(owner)), y, " ", "38;2;51;68;85")
		if key != "" {
			assertTextAt(t, grid, ownerX+widths[3]+2, y, key, "38;2;51;68;85")
		}
		return
	}
	t.Fatalf("missing legend row %s", id)
}

func TestCanvasWideOverlap(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		first, second, cleared int
		want                   string
	}{
		{"second cell hits wide start", 2, 1, 3, " 好  \n"},
		{"first cell hits continuation", 1, 2, 1, "  好 \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanvas(5, 1)
			c.put(tc.first, 0, cell{text: "界", width: 2, fg: "#112233", bg: "#556677"})
			c.put(tc.second, 0, cell{text: "好", width: 2, fg: "#223344", bg: "#445566"})
			if got := c.cells[0][tc.cleared]; got.text != " " || got.width != 1 || got.fg != "#112233" || got.bg != "#556677" {
				t.Fatalf("cleared half lost width or style: %+v", got)
			}
			if got := plain(c.lines()); got != tc.want {
				t.Fatalf("serialized row %q, want %q", got, tc.want)
			}
			width := 0
			for _, g := range c.cells[0] {
				width += g.width
			}
			if width != 5 {
				t.Fatalf("row width %d, want 5", width)
			}
		})
	}
}
