package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// sgrRGB returns the 24-bit SGR parameter body for a "#RRGGBB" colour, with
// prefix 38 for foreground or 48 for background.
func sgrRGB(t *testing.T, prefix int, hex string) string {
	t.Helper()
	var c [3]int64
	for i := range c {
		v, err := strconv.ParseInt(hex[1+2*i:3+2*i], 16, 32)
		if err != nil {
			t.Fatalf("bad colour %q: %v", hex, err)
		}
		c[i] = v
	}
	return fmt.Sprintf("%d;2;%d;%d;%d", prefix, c[0], c[1], c[2])
}

// The chrome must paint with the palette's hex roles as truecolor SGR, not
// 256-colour or ANSI indices.
func TestChromeRendersPaletteAsTruecolor(t *testing.T) {
	m := pureViewModel("plugin", true)
	out := m.View().Content
	for name, want := range map[string]string{
		"surface background":  sgrRGB(t, 48, m.palette.Surface),
		"elevated background": sgrRGB(t, 48, m.palette.SurfaceElevated),
		"selected background": sgrRGB(t, 48, m.palette.SurfaceSelected),
		"ink foreground":      sgrRGB(t, 38, m.palette.Ink),
		"accent foreground":   sgrRGB(t, 38, m.palette.Accent),
		"muted foreground":    sgrRGB(t, 38, m.palette.InkMuted),
		"focused border":      sgrRGB(t, 38, m.palette.BorderFocused),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: frame has no SGR with %q", name, want)
		}
	}
}
