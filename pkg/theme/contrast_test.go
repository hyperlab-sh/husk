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
	luminance, err := RelativeLuminance(hex)
	if err != nil {
		t.Fatal(err)
	}
	return luminance
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	ratio, err := Contrast(a, b)
	if err != nil {
		t.Fatal(err)
	}
	return ratio
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
	for _, violation := range CheckContrast(Default()) {
		t.Errorf("%s on %s = %.2f:1, need %.1f:1", violation.Foreground, violation.Background, violation.Ratio, violation.Minimum)
	}
}

func TestDefaultPaletteIsHex(t *testing.T) {
	p := Default()
	vals := map[string]string{
		"Canvas": p.Canvas, "Slot": p.Slot, "Surface": p.Surface, "SurfaceElevated": p.SurfaceElevated,
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
