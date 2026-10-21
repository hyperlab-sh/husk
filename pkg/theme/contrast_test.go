package theme

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// relLuminance is the WCAG 2.x relative luminance of a "#RRGGBB" colour.
func relLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("colour %q is not #RRGGBB", hex)
	}
	var c [3]float64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("colour %q: %v", hex, err)
		}
		s := float64(v) / 255
		if s <= 0.03928 {
			c[i] = s / 12.92
		} else {
			c[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := relLuminance(t, a), relLuminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestContrastKnownValues(t *testing.T) {
	if got := contrast(t, "#000000", "#FFFFFF"); math.Abs(got-21) > 1e-9 {
		t.Fatalf("black on white = %v, want 21", got)
	}
	if got := contrast(t, "#777777", "#777777"); math.Abs(got-1) > 1e-9 {
		t.Fatalf("identical colours = %v, want 1", got)
	}
	// #767676 on white is the classic 4.54:1 AA boundary colour.
	if got := contrast(t, "#767676", "#FFFFFF"); got < 4.5 || got > 4.6 {
		t.Fatalf("#767676 on white = %v, want about 4.54", got)
	}
}

const (
	textMin = 4.5 // WCAG AA body text
	uiMin   = 3.0 // WCAG non-text contrast (borders, focus, icons)
)

// TestDefaultPaletteContrast checks every foreground/background pairing the
// host chrome draws (see cmd/husk/render.go) against its WCAG threshold.
func TestDefaultPaletteContrast(t *testing.T) {
	p := Default()
	fg := map[string]string{
		"Ink": p.Ink, "InkMuted": p.InkMuted, "InkSubtle": p.InkSubtle,
		"Accent": p.Accent, "AccentSecondary": p.AccentSecondary,
		"Success": p.Success, "Warning": p.Warning, "Danger": p.Danger, "Info": p.Info,
		"Canvas": p.Canvas, "Border": p.Border, "BorderFocused": p.BorderFocused,
	}
	bg := map[string]string{
		"Canvas": p.Canvas, "Surface": p.Surface, "SurfaceElevated": p.SurfaceElevated,
		"SurfaceSelected": p.SurfaceSelected, "SurfaceHover": p.SurfaceHover,
		"Accent": p.Accent, "Success": p.Success, "Warning": p.Warning,
	}
	type pair struct {
		fg, bg string
		min    float64
	}
	var pairs []pair
	add := func(min float64, fgs []string, bgs []string) {
		for _, f := range fgs {
			for _, b := range bgs {
				pairs = append(pairs, pair{f, b, min})
			}
		}
	}
	// Text and glyph roles on every surface they can land on. Selected rows
	// and the active rail pill sit on SurfaceSelected, the lightest surface,
	// so every role the chrome draws there is held to 4.5:1 on it too.
	add(textMin, []string{"Ink", "InkMuted", "Accent", "AccentSecondary", "Success", "Warning"},
		[]string{"Canvas", "Surface", "SurfaceElevated", "SurfaceSelected", "SurfaceHover"})
	// InkSubtle, Danger and Info are never drawn on SurfaceSelected: the
	// selected explorer row uses InkMuted, and Danger/Info only appear as toast
	// edges and the footer hint on the base surfaces and Elevated. Hover is not
	// drawn by the host, and equals Elevated (BBQ).
	add(textMin, []string{"InkSubtle", "Danger", "Info"},
		[]string{"Canvas", "Surface", "SurfaceElevated"})
	// Dark text on the filled badges and the active rail pill.
	add(textMin, []string{"Canvas"}, []string{"Accent", "Success", "Warning"})
	// Borders, focus ring and the toast edge colours need 3:1 on the panels
	// they enclose; the chrome draws them over Canvas, Surface and Elevated.
	add(uiMin, []string{"Border", "BorderFocused", "Danger", "Info", "Success", "Warning"},
		[]string{"Canvas", "Surface", "SurfaceElevated"})

	for _, c := range pairs {
		c := c
		t.Run(c.fg+"_on_"+c.bg, func(t *testing.T) {
			got := contrast(t, fg[c.fg], bg[c.bg])
			if got < c.min {
				t.Errorf("%s (%s) on %s (%s) = %.2f:1, need %.1f:1",
					c.fg, fg[c.fg], c.bg, bg[c.bg], got, c.min)
			}
		})
	}
}

func TestDefaultPaletteIsHex(t *testing.T) {
	p := Default()
	vals := map[string]string{
		"Canvas": p.Canvas, "Surface": p.Surface, "SurfaceElevated": p.SurfaceElevated,
		"SurfaceSelected": p.SurfaceSelected, "SurfaceHover": p.SurfaceHover,
		"Border": p.Border, "BorderSubtle": p.BorderSubtle, "BorderFocused": p.BorderFocused,
		"Ink": p.Ink, "InkMuted": p.InkMuted, "InkSubtle": p.InkSubtle,
		"Accent": p.Accent, "AccentSecondary": p.AccentSecondary, "AccentSoft": p.AccentSoft,
		"Success": p.Success, "Warning": p.Warning, "Danger": p.Danger, "Info": p.Info,
	}
	for name, v := range vals {
		if len(v) != 7 || !strings.HasPrefix(v, "#") {
			t.Errorf("%s = %q, want #RRGGBB", name, v)
			continue
		}
		if _, err := strconv.ParseUint(v[1:], 16, 32); err != nil {
			t.Errorf("%s = %q: %v", name, v, err)
		}
	}
	if p.Name == "" {
		t.Error("palette name is empty")
	}
}

// Raised surfaces must read lighter than the base so panels look layered;
// Canvas and Surface may be the same colour.
func TestDefaultPaletteSurfacesAreOrdered(t *testing.T) {
	p := Default()
	lc, ls := relLuminance(t, p.Canvas), relLuminance(t, p.Surface)
	le, lsel := relLuminance(t, p.SurfaceElevated), relLuminance(t, p.SurfaceSelected)
	if !(lc <= ls && ls < le && le < lsel) {
		t.Fatalf("surface luminance not ordered: canvas %.4f surface %.4f elevated %.4f selected %.4f", lc, ls, le, lsel)
	}
}
