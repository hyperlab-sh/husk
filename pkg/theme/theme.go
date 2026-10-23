package theme

import (
	"encoding/json"
	"sync"
)

// Palette is a flat colour token set shared by the workbench and its plugins.
// Values are lowercase "#rrggbb" strings resolved from the built-in themes
// layer or user configuration. ANSI indices are kept beside this comparable
// value, never inside it. It carries only generic roles; a plugin that needs
// application-specific colours defines its own.
type Palette struct {
	Name            string `json:"name"`
	Canvas          string `json:"canvas"`
	Surface         string `json:"surface"`
	SurfaceElevated string `json:"surface_elevated"`
	SurfaceSelected string `json:"surface_selected"`
	SurfaceHover    string `json:"surface_hover"`
	Slot            string `json:"slot_background,omitempty"`
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

var defaultPalette = sync.OnceValue(func() Palette {
	entry := BuiltIn()["themes"].(map[string]any)["charm"].(map[string]any)["palette"]
	b, _ := json.Marshal(entry)
	var p Palette
	json.Unmarshal(b, &p)
	p.Name = "charm"
	return p
})

// Default returns a value copy of charm, decoded once from the built-in themes
// layer. Every text and border pairing is contrast-checked (text 4.5:1, borders
// and other non-text 3:1).
func Default() Palette { return defaultPalette() }
