package theme

// Palette is a flat colour token set shared by the workbench and its plugins.
// All values are ANSI (0-15) or xterm-256 (16-255) colour codes so terminals
// render Husk with their own Charm default palette. It carries only generic
// roles; a plugin that needs application-specific colours defines its own.
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
	}
}
