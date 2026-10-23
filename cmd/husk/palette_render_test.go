package main

import (
	"image/color"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// hasTruecolorCell reports whether some decoded cell carries want as a 24-bit
// colour on the channel pick selects. Only a direct RGB SGR decodes to
// color.RGBA; 256-colour and ANSI indices decode to other types.
func hasTruecolorCell(cells uv.Lines, want color.Color, pick func(uv.Style) color.Color) bool {
	for _, row := range cells {
		for _, c := range row {
			got, ok := pick(c.Style).(color.RGBA)
			if ok && sameColor(got, want) {
				return true
			}
		}
	}
	return false
}

// The chrome must paint with the palette's hex roles as truecolor SGR, not
// 256-colour or ANSI indices.
func TestChromeRendersPaletteAsTruecolor(t *testing.T) {
	m := pureViewModel("plugin", true)
	cells := frameCells(m.View().Content)
	fg := func(s uv.Style) color.Color { return s.Fg }
	bg := func(s uv.Style) color.Color { return s.Bg }
	for name, want := range map[string]struct {
		hex  color.Color
		pick func(uv.Style) color.Color
	}{
		"surface background":  {m.palette.Surface, bg},
		"elevated background": {m.palette.SurfaceElevated, bg},
		"selected background": {m.palette.SurfaceSelected, bg},
		"ink foreground":      {m.palette.Ink, fg},
		"accent foreground":   {m.palette.Accent, fg},
		"muted foreground":    {m.palette.InkMuted, fg},
		"focused border":      {m.palette.BorderFocused, fg},
	} {
		if !hasTruecolorCell(cells, want.hex, want.pick) {
			t.Errorf("%s: no cell painted with truecolor %v", name, want.hex)
		}
	}
}
