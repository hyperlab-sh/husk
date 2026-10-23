package guide

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func number(n int) string {
	if n >= 1 && n <= 20 {
		return string(rune('①' + n - 1))
	}
	return strconv.Itoa(n)
}

type cell struct {
	text, fg, bg string
	width        int
}
type canvas struct {
	w, h  int
	cells [][]cell
}

func newCanvas(w, h int) canvas {
	c := canvas{w: w, h: h, cells: make([][]cell, h)}
	for y := range c.cells {
		c.cells[y] = make([]cell, w)
		for x := range c.cells[y] {
			c.cells[y][x] = cell{text: " ", width: 1}
		}
	}
	return c
}

// Wire ids are ASCII; keys may also contain printable Unicode. Preserve
// combining marks and account for full-width text when clipping to cell bounds.
func glyphs(s string) []cell {
	var out []cell
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			if len(out) > 0 {
				out[len(out)-1].text += string(r)
			}
			continue
		}
		w := 1
		if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff01 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300 && r <= 0x1faff || r >= 0x20000) {
			w = 2
		}
		out = append(out, cell{text: string(r), width: w})
	}
	return out
}
func textWidth(s string) int {
	n := 0
	for _, g := range glyphs(s) {
		n += g.width
	}
	return n
}
func truncate(s string, w int) string {
	if w < 1 {
		return ""
	}
	if textWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, g := range glyphs(s) {
		if used+g.width > w-1 {
			break
		}
		b.WriteString(g.text)
		used += g.width
	}
	b.WriteString("…")
	return b.String()
}
func (c canvas) put(x, y int, g cell) {
	if x < 0 || y < 0 || y >= c.h || x+g.width > c.w {
		return
	}
	// Clear an earlier wide glyph if either of its cells is overwritten.
	if c.cells[y][x].width == 0 && x > 0 {
		c.cells[y][x-1] = cell{text: " ", width: 1, fg: c.cells[y][x-1].fg, bg: c.cells[y][x-1].bg}
	}
	if c.cells[y][x].width == 2 {
		c.cells[y][x+1] = cell{text: " ", width: 1, fg: c.cells[y][x+1].fg, bg: c.cells[y][x+1].bg}
	}
	c.cells[y][x] = g
	if g.width == 2 {
		if c.cells[y][x+1].width == 2 {
			old := c.cells[y][x+2]
			c.cells[y][x+2] = cell{text: " ", width: 1, fg: old.fg, bg: old.bg}
		}
		c.cells[y][x+1] = cell{fg: g.fg, bg: g.bg}
	}
}
func (c canvas) text(x, y, w int, s, fg, bg string) {
	end := x + w
	for _, g := range glyphs(s) {
		if x+g.width > end {
			break
		}
		g.fg, g.bg = fg, bg
		c.put(x, y, g)
		x += g.width
	}
}
func (c canvas) box(r rect, focused bool, fg, bg string) {
	chars := []rune("┌┐└┘─│")
	if focused {
		chars = []rune("╔╗╚╝═║")
	}
	for x := r.X; x < r.X+r.Width; x++ {
		c.text(x, r.Y, 1, string(chars[4]), fg, bg)
		c.text(x, r.Y+r.Height-1, 1, string(chars[4]), fg, bg)
	}
	for y := r.Y; y < r.Y+r.Height; y++ {
		c.text(r.X, y, 1, string(chars[5]), fg, bg)
		c.text(r.X+r.Width-1, y, 1, string(chars[5]), fg, bg)
	}
	for i, p := range [][2]int{{r.X, r.Y}, {r.X + r.Width - 1, r.Y}, {r.X, r.Y + r.Height - 1}, {r.X + r.Width - 1, r.Y + r.Height - 1}} {
		c.text(p[0], p[1], 1, string(chars[i]), fg, bg)
	}
}

func muted(value string) bool { return value == "empty" || value == "hidden" || value == "not placed" }
func (l layout) draw(k keyListing, p protocol.Palette) canvas {
	w, h := l.Geometry.Terminal.Width, l.Geometry.Terminal.Height
	c := newCanvas(w, h)
	entries := l.annotations(k)
	for _, a := range entries {
		r := a.Rect
		focused := l.Focus.Surface == a.ID
		fg := p.Accent
		if focused {
			fg = p.BorderFocused
		}
		x, limit := r.X, r.Width
		if r.Width >= 2 && r.Height >= 2 {
			c.box(r, focused, fg, "")
			x++
			limit -= 2
		}
		fields := a.Fields[:]
		if fields[4] == "" {
			fields = fields[:4]
		}
		for len(fields) > 1 && textWidth(fields[0]+" "+strings.Join(fields[1:], " · ")) > limit {
			fields = fields[:len(fields)-1]
		}
		for i, f := range fields {
			if i > 0 {
				separator := " · "
				if i == 1 {
					separator = " "
				}
				c.text(x, r.Y, limit, separator, p.Accent, "")
				x += textWidth(separator)
				limit -= textWidth(separator)
			}
			color := p.Accent
			if i == 3 && muted(f) {
				color = p.InkMuted
			}
			c.text(x, r.Y, limit, f, color, "")
			x += textWidth(f)
			limit -= textWidth(f)
		}
	}
	rows := make([][5]string, 0, len(entries)+12)
	for _, a := range entries {
		rows = append(rows, a.Fields)
	}
	for _, id := range surfaceTable() {
		if id == protocol.SurfaceAppOverlay {
			continue
		}
		if _, ok := l.Geometry.Rects[id]; ok {
			continue
		}
		owner, key := "hidden", l.surfaceKey(k, id)
		if id == protocol.SurfaceThreadRowStatus {
			owner, key = "not placed", ""
		}
		rows = append(rows, [5]string{"", id.Humanised(), string(id), owner, key})
	}
	legend := rect{X: w - 2 - min(64, w-4), Y: h - 1 - (len(rows) + 3), Width: min(64, w-4), Height: len(rows) + 3}
	for y := legend.Y; y < legend.Y+legend.Height; y++ {
		c.text(legend.X, y, legend.Width, strings.Repeat(" ", legend.Width), p.Ink, p.SurfaceElevated)
	}
	c.box(legend, false, p.Ink, p.SurfaceElevated)
	var widths [5]int
	for _, row := range rows {
		for j, f := range row {
			widths[j] = max(widths[j], textWidth(f))
		}
	}
	for i, row := range rows {
		var line strings.Builder
		for j, f := range row {
			line.WriteString(f)
			if j < 4 {
				line.WriteString(strings.Repeat(" ", widths[j]-textWidth(f)+2))
			}
		}
		text := truncate(line.String(), legend.Width-2)
		x := legend.X + 1
		// Colour the occupant column without changing the padded column geometry.
		occupantStart := widths[0] + widths[1] + widths[2] + 6
		for col, g := range glyphs(text) {
			fg := p.Ink
			if muted(row[3]) && col >= occupantStart && col < occupantStart+textWidth(row[3]) {
				fg = p.InkMuted
			}
			g.fg, g.bg = fg, p.SurfaceElevated
			c.put(x, legend.Y+1+i, g)
			x += g.width
		}
	}
	help := k.effective("husk.focus.next", l.Focus.Surface, "palette") + " next focus · " + k.effective(toggleAction, protocol.SurfaceAppOverlay, "palette") + " hide guide"
	c.text(legend.X+1, legend.Y+legend.Height-2, legend.Width-2, truncate(help, legend.Width-2), p.Ink, p.SurfaceElevated)
	return c
}

func colorSGR(s string, bg bool) string {
	if len(s) != 7 || s[0] != '#' {
		return ""
	}
	n, err := strconv.ParseUint(s[1:], 16, 24)
	if err != nil {
		return ""
	}
	base := 38
	if bg {
		base = 48
	}
	return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", base, n>>16, (n>>8)&255, n&255)
}
func (c canvas) lines() []string {
	lines := make([]string, c.h)
	for y, row := range c.cells {
		var b strings.Builder
		fg, bg := "", ""
		for _, g := range row {
			if g.width == 0 {
				continue
			}
			if g.fg != fg || g.bg != bg {
				if fg != "" || bg != "" {
					b.WriteString("\x1b[0m")
				}
				b.WriteString(colorSGR(g.fg, false))
				b.WriteString(colorSGR(g.bg, true))
				fg, bg = g.fg, g.bg
			}
			b.WriteString(g.text)
		}
		if fg != "" || bg != "" {
			b.WriteString("\x1b[0m")
		}
		lines[y] = b.String()
	}
	return lines
}
