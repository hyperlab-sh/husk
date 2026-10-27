package workbench

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// drawPalette resolves the serving palette and its index map once per change.
// Keeping role names explicit makes the chrome's drawing discipline auditable.
// The comparable hex palette stays in model.served, not here.
type drawPalette struct {
	Name            string
	Canvas          color.Color
	Surface         color.Color
	SurfaceElevated color.Color
	SurfaceSelected color.Color
	SurfaceHover    color.Color
	Slot            color.Color
	Border          color.Color
	BorderSubtle    color.Color
	BorderFocused   color.Color
	Ink             color.Color
	InkMuted        color.Color
	InkSubtle       color.Color
	Accent          color.Color
	AccentSecondary color.Color
	AccentSoft      color.Color
	Success         color.Color
	Warning         color.Color
	Danger          color.Color
	Info            color.Color
}

func newDrawPalette(p theme.Palette, indices map[string]int) drawPalette {
	role := func(name, hex string) color.Color {
		if n, ok := indices[name]; ok {
			return ansi.BasicColor(n)
		}
		if hex == "" {
			return nil
		}
		return lipgloss.Color(hex)
	}
	return drawPalette{
		Name:            p.Name,
		Canvas:          role("canvas", p.Canvas),
		Surface:         role("surface", p.Surface),
		SurfaceElevated: role("surface_elevated", p.SurfaceElevated),
		SurfaceSelected: role("surface_selected", p.SurfaceSelected),
		SurfaceHover:    role("surface_hover", p.SurfaceHover),
		Slot:            role("slot_background", p.Slot),
		Border:          role("border", p.Border),
		BorderSubtle:    role("border_subtle", p.BorderSubtle),
		BorderFocused:   role("border_focused", p.BorderFocused),
		Ink:             role("ink", p.Ink),
		InkMuted:        role("ink_muted", p.InkMuted),
		InkSubtle:       role("ink_subtle", p.InkSubtle),
		Accent:          role("accent", p.Accent),
		AccentSecondary: role("accent_secondary", p.AccentSecondary),
		AccentSoft:      role("accent_soft", p.AccentSoft),
		Success:         role("success", p.Success),
		Warning:         role("warning", p.Warning),
		Danger:          role("danger", p.Danger),
		Info:            role("info", p.Info),
	}
}
