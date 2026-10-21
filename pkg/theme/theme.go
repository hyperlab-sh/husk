package theme

import "github.com/charmbracelet/x/exp/charmtone"

// Palette is a flat colour token set shared by the workbench and its plugins.
// Values are "#RRGGBB" strings taken from the CharmTone palette, so Husk looks
// the same in every terminal that supports truecolor (lipgloss downsamples on
// terminals that do not). It carries only generic roles; a plugin that needs
// application-specific colours defines its own.
type Palette struct {
	Name            string `json:"name"`
	Canvas          string `json:"canvas"`
	Surface         string `json:"surface"`
	SurfaceElevated string `json:"surface_elevated"`
	SurfaceSelected string `json:"surface_selected"`
	SurfaceHover    string `json:"surface_hover"`
	Border          string `json:"border"`
	BorderSubtle    string `json:"border_subtle"`
	BorderFocused   string `json:"border_focused"`
	Ink             string `json:"ink"`
	InkMuted        string `json:"ink_muted"`
	InkSubtle       string `json:"ink_subtle"`
	Accent          string `json:"accent"`
	AccentSecondary string `json:"accent_secondary"`
	AccentSoft      string `json:"accent_soft"`
	Success         string `json:"success"`
	Warning         string `json:"warning"`
	Danger          string `json:"danger"`
	Info            string `json:"info"`
}

// Default is Husk's single palette. Every text and border pairing the chrome
// draws is contrast-checked by TestDefaultPaletteContrast (text 4.5:1,
// borders and other non-text 3:1); change a role only with that test green.
func Default() Palette {
	return Palette{
		Name:            "Charm Default",
		Canvas:          charmtone.Pepper.Hex(),
		Surface:         charmtone.Pepper.Hex(),
		SurfaceElevated: charmtone.BBQ.Hex(),
		SurfaceSelected: charmtone.Char.Hex(),
		SurfaceHover:    charmtone.BBQ.Hex(),
		Border:          charmtone.Squid.Hex(),
		BorderSubtle:    charmtone.Iron.Hex(),
		BorderFocused:   charmtone.Hazy.Hex(),
		Ink:             charmtone.Sash.Hex(),
		InkMuted:        charmtone.Smoke.Hex(),
		InkSubtle:       charmtone.Steam.Hex(),
		Accent:          charmtone.Lilac.Hex(),
		AccentSecondary: charmtone.Sardine.Hex(),
		AccentSoft:      charmtone.Hazy.Hex(),
		Success:         charmtone.Julep.Hex(),
		Warning:         charmtone.Mustard.Hex(),
		Danger:          charmtone.Coral.Hex(),
		Info:            charmtone.Malibu.Hex(),
	}
}
