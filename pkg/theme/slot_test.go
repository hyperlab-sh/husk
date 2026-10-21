package theme

import (
	"strings"
	"testing"

	"github.com/lucasb-eyer/go-colorful"
)

func labL(t *testing.T, hex string) float64 {
	t.Helper()
	c, err := colorful.Hex(hex)
	if err != nil {
		t.Fatalf("colour %q: %v", hex, err)
	}
	l, _, _ := c.Lab()
	return l
}

func TestSlotBackgroundDarkIsLighter(t *testing.T) {
	for _, in := range []string{"#201F26", "#000000", "#1A1B26", "#282C34", "#3A3943"} {
		got := SlotBackground(in)
		if !(labL(t, got) > labL(t, in)) {
			t.Errorf("SlotBackground(%s) = %s is not lighter", in, got)
		}
		if relLuminance(t, got) <= relLuminance(t, in) {
			t.Errorf("SlotBackground(%s) = %s has no more luminance", in, got)
		}
	}
}

func TestSlotBackgroundLightIsDarker(t *testing.T) {
	for _, in := range []string{"#FFFFFF", "#FAFAFA", "#EFF1F5", "#FDF6E3", "#C8C8D0"} {
		got := SlotBackground(in)
		if !(labL(t, got) < labL(t, in)) {
			t.Errorf("SlotBackground(%s) = %s is not darker", in, got)
		}
	}
}

// The step is small and fixed: about SlotShift of Lab lightness away from the
// app background, in the same hue family, in-gamut, and never a clamp artefact.
func TestSlotBackgroundShiftIsBounded(t *testing.T) {
	for _, in := range []string{"#201F26", "#1A1B26", "#FFFFFF", "#EFF1F5", "#808080", "#7F7F7F", "#FF0000", "#0000FF"} {
		got := SlotBackground(in)
		if len(got) != 7 || !strings.HasPrefix(got, "#") {
			t.Fatalf("SlotBackground(%s) = %q, want #RRGGBB", in, got)
		}
		d := labL(t, got) - labL(t, in)
		if d < 0 {
			d = -d
		}
		// gamut clamping and 8-bit rounding may eat into the step but never add to it
		if d > SlotShift+0.01 {
			t.Errorf("SlotBackground(%s) = %s moved L by %.3f, want <= %.2f", in, got, d, SlotShift)
		}
		if d == 0 {
			t.Errorf("SlotBackground(%s) = %s did not move", in, got)
		}
	}
}

// Black and white are the ends of the range: the shift must stay inside it.
func TestSlotBackgroundClampsAtExtremes(t *testing.T) {
	if got := SlotBackground("#000000"); got == "#000000" || labL(t, got) > SlotShift+0.01 {
		t.Errorf("black -> %s", got)
	}
	if got := SlotBackground("#FFFFFF"); got == "#FFFFFF" || labL(t, got) < 1-SlotShift-0.01 {
		t.Errorf("white -> %s", got)
	}
}

func TestSlotBackgroundInvalidInputPassesThrough(t *testing.T) {
	// alpha, palette indices, garbage and the empty string are rejected
	// explicitly: the caller gets its input back, meaning "no panel"
	for _, in := range []string{"", "63", "#12", "#GGGGGG", "red", "#201F26FF", "#201F2", "201F26"} {
		if got := SlotBackground(in); got != in {
			t.Errorf("SlotBackground(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestSlotBackgroundShortHexMatchesLongForm(t *testing.T) {
	for short, long := range map[string]string{"#fff": "#ffffff", "#000": "#000000", "#1AB": "#11aabb"} {
		if a, b := SlotBackground(short), SlotBackground(long); a != b || a == short {
			t.Errorf("SlotBackground(%q) = %q, long form gives %q", short, a, b)
		}
	}
}

// Pinned values: a Lab-math change that stays within the bounds tests still
// has to move these on purpose. Output is lower-case #rrggbb and case of the
// input does not matter.
func TestSlotBackgroundPinnedValues(t *testing.T) {
	for in, want := range map[string]string{
		"#201F26": "#27262d", "#201f26": "#27262d",
		"#1A1B26": "#21222d",
		"#FFFFFF": "#f5f5f5",
		"#EFF1F5": "#e5e7eb",
	} {
		if got := SlotBackground(in); got != want {
			t.Errorf("SlotBackground(%s) = %s, want %s", in, got, want)
		}
	}
}

// The panel ladder the chrome relies on: pills (Elevated), hover rows and
// selections must stay visibly distinct from the slot they sit on, and from
// each other where they overlap.
func TestSurfaceLadderSeparation(t *testing.T) {
	p := Default()
	const minStep = 1.08
	for _, c := range []struct{ name, a, b string }{
		{"Canvas/Slot", p.Canvas, p.Slot},
		{"Slot/Elevated", p.Slot, p.SurfaceElevated},
		{"Slot/Hover", p.Slot, p.SurfaceHover},
		{"Elevated/Selected", p.SurfaceElevated, p.SurfaceSelected},
		{"Slot/Selected", p.Slot, p.SurfaceSelected},
	} {
		if got := contrast(t, c.a, c.b); got < minStep {
			t.Errorf("%s (%s vs %s) = %.3f:1, want at least %.2f:1", c.name, c.a, c.b, got, minStep)
		}
	}
	if !(relLuminance(t, p.Slot) < relLuminance(t, p.SurfaceElevated)) {
		t.Error("Elevated must be lighter than the slot on the dark palette")
	}
}

// A dark canvas gets a lighter slot and a light canvas a darker one, whichever
// way the palette goes.
func TestSlotDirectionFollowsCanvas(t *testing.T) {
	if d := Default(); relLuminance(t, d.Slot) <= relLuminance(t, d.Canvas) {
		t.Errorf("dark canvas %s: slot %s is not lighter", d.Canvas, d.Slot)
	}
	light := "#F5F5F7"
	if relLuminance(t, SlotBackground(light)) >= relLuminance(t, light) {
		t.Errorf("light canvas %s: slot %s is not darker", light, SlotBackground(light))
	}
}

// Contrast sanity: the slot reads as a different panel than the app
// background (above 1.05:1) without becoming a contrast jump (below 1.6:1),
// on dark and light backgrounds alike. Ink on the slot keeps AA on a dark
// background; on a light one the dark ink gets darker still relative to bg
// so we check a representative ink.
func TestSlotBackgroundContrastSanity(t *testing.T) {
	for _, in := range []string{"#201F26", "#1A1B26", "#FFFFFF", "#EFF1F5"} {
		got := SlotBackground(in)
		c := contrast(t, in, got)
		t.Logf("%s -> %s (%.3f:1)", in, got, c)
		if c < 1.05 || c > 1.6 {
			t.Errorf("%s vs slot %s = %.3f:1, want within 1.05..1.6", in, got, c)
		}
	}
	slot := SlotBackground("#FFFFFF")
	if c := contrast(t, "#201F26", slot); c < textMin {
		t.Errorf("dark ink on light slot %s = %.2f", slot, c)
	}
}

func TestDefaultPaletteSlot(t *testing.T) {
	p := Default()
	if p.Slot != SlotBackground(p.Canvas) {
		t.Fatalf("Slot = %s, want derived from Canvas %s = %s", p.Slot, p.Canvas, SlotBackground(p.Canvas))
	}
	if p.Slot == p.Canvas {
		t.Fatal("Slot equals Canvas")
	}
}
