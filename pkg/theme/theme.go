package theme

import "github.com/charmbracelet/lipgloss"

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

var Themes = []Palette{
	{
		Name:            "Tokyo Night",
		Canvas:          "#16161e",
		Surface:         "#1a1b26",
		SurfaceElevated: "#24283b",
		SurfaceSelected: "#283457",
		SurfaceHover:    "#1f2335",
		Border:          "#3b4261",
		BorderSubtle:    "#292e42",
		BorderFocused:   "#7aa2f7",
		Ink:             "#c0caf5",
		InkMuted:        "#a9b1d6",
		InkSubtle:       "#565f89",
		Accent:          "#7aa2f7",
		AccentSecondary: "#bb9af7",
		AccentSoft:      "#3d59a1",
		Success:         "#9ece6a",
		Warning:         "#e0af68",
		Danger:          "#f7768e",
		Info:            "#7dcfff",
		SpaceGreen:      "#9ece6a",
		SpaceCyan:       "#7dcfff",
		SpaceIndigo:     "#7aa2f7",
		SpaceYellow:     "#e0af68",
		SpaceLime:       "#73daca",
		SpacePurple:     "#bb9af7",
		SpacePink:       "#f7768e",
		SpaceOrange:     "#ff9e64",
	},
	{
		Name:            "Catppuccin Mocha",
		Canvas:          "#181825",
		Surface:         "#1e1e2e",
		SurfaceElevated: "#313244",
		SurfaceSelected: "#45475a",
		SurfaceHover:    "#262637",
		Border:          "#45475a",
		BorderSubtle:    "#313244",
		BorderFocused:   "#cba6f7",
		Ink:             "#cdd6f4",
		InkMuted:        "#bac2de",
		InkSubtle:       "#6c7086",
		Accent:          "#cba6f7",
		AccentSecondary: "#89b4fa",
		AccentSoft:      "#585b70",
		Success:         "#a6e3a1",
		Warning:         "#f9e2af",
		Danger:          "#f38ba8",
		Info:            "#89dceb",
		SpaceGreen:      "#a6e3a1",
		SpaceCyan:       "#89dceb",
		SpaceIndigo:     "#89b4fa",
		SpaceYellow:     "#f9e2af",
		SpaceLime:       "#94e2d5",
		SpacePurple:     "#cba6f7",
		SpacePink:       "#f5c2e7",
		SpaceOrange:     "#fab387",
	},
	{
		Name:            "Rosé Pine",
		Canvas:          "#191724",
		Surface:         "#1f1d2e",
		SurfaceElevated: "#26233a",
		SurfaceSelected: "#403d52",
		SurfaceHover:    "#2a273f",
		Border:          "#403d52",
		BorderSubtle:    "#26233a",
		BorderFocused:   "#c4a7e7",
		Ink:             "#e0def4",
		InkMuted:        "#908caa",
		InkSubtle:       "#6e6a86",
		Accent:          "#c4a7e7",
		AccentSecondary: "#ebbcba",
		AccentSoft:      "#524f67",
		Success:         "#9ccfd8",
		Warning:         "#f6c177",
		Danger:          "#eb6f92",
		Info:            "#31748f",
		SpaceGreen:      "#9ccfd8",
		SpaceCyan:       "#9ccfd8",
		SpaceIndigo:     "#31748f",
		SpaceYellow:     "#f6c177",
		SpaceLime:       "#9ccfd8",
		SpacePurple:     "#c4a7e7",
		SpacePink:       "#ebbcba",
		SpaceOrange:     "#f6c177",
	},
	{
		Name:            "Kanagawa Wave",
		Canvas:          "#16161d",
		Surface:         "#1f1f28",
		SurfaceElevated: "#2a2a37",
		SurfaceSelected: "#2d4f67",
		SurfaceHover:    "#223249",
		Border:          "#54546d",
		BorderSubtle:    "#2a2a37",
		BorderFocused:   "#7e9cd8",
		Ink:             "#dcd7ba",
		InkMuted:        "#c8c093",
		InkSubtle:       "#727169",
		Accent:          "#7e9cd8",
		AccentSecondary: "#957fb8",
		AccentSoft:      "#223249",
		Success:         "#98bb6c",
		Warning:         "#e6c384",
		Danger:          "#e46876",
		Info:            "#7fb4ca",
		SpaceGreen:      "#98bb6c",
		SpaceCyan:       "#7fb4ca",
		SpaceIndigo:     "#7e9cd8",
		SpaceYellow:     "#e6c384",
		SpaceLime:       "#6a9589",
		SpacePurple:     "#957fb8",
		SpacePink:       "#d27e99",
		SpaceOrange:     "#ffa066",
	},
}

func ByName(name string) Palette {
	for _, t := range Themes {
		if t.Name == name {
			return t
		}
	}
	return Themes[0]
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
