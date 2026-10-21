package theme

import "github.com/charmbracelet/lipgloss"

// Palette is a flat colour token set shared by the workbench and its plugins.
// All values are ANSI (0-15) or xterm-256 (16-255) colour codes so terminals
// render Husk with their own Charm default palette.
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

	SpaceGreen  string `json:"space_green"`
	SpaceCyan   string `json:"space_cyan"`
	SpaceIndigo string `json:"space_indigo"`
	SpaceYellow string `json:"space_yellow"`
	SpaceLime   string `json:"space_lime"`
	SpacePurple string `json:"space_purple"`
	SpacePink   string `json:"space_pink"`
	SpaceOrange string `json:"space_orange"`
}

// Default is Husk's single palette, built only from ANSI/256 colour codes.
func Default() Palette {
	return Palette{
		Name:            "Charm Default",
		Canvas:          "0",
		Surface:         "0",
		SurfaceElevated: "236",
		SurfaceSelected: "238",
		SurfaceHover:    "237",
		Border:          "241",
		BorderSubtle:    "238",
		BorderFocused:   "63",
		Ink:             "15",
		InkMuted:        "7",
		InkSubtle:       "245",
		Accent:          "63",
		AccentSecondary: "205",
		AccentSoft:      "61",
		Success:         "10",
		Warning:         "11",
		Danger:          "9",
		Info:            "14",

		SpaceGreen:  "10",
		SpaceCyan:   "14",
		SpaceIndigo: "63",
		SpaceYellow: "11",
		SpaceLime:   "120",
		SpacePurple: "135",
		SpacePink:   "205",
		SpaceOrange: "208",
	}
}

func (p Palette) SpaceColor(tokenKey string) lipgloss.Color {
	switch tokenKey {
	case "space_green":
		return lipgloss.Color(p.SpaceGreen)
	case "space_cyan":
		return lipgloss.Color(p.SpaceCyan)
	case "space_indigo":
		return lipgloss.Color(p.SpaceIndigo)
	case "space_yellow":
		return lipgloss.Color(p.SpaceYellow)
	case "space_lime":
		return lipgloss.Color(p.SpaceLime)
	case "space_purple":
		return lipgloss.Color(p.SpacePurple)
	case "space_pink":
		return lipgloss.Color(p.SpacePink)
	case "space_orange":
		return lipgloss.Color(p.SpaceOrange)
	default:
		return lipgloss.Color(p.Accent)
	}
}
