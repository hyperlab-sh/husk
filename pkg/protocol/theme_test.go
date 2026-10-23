package protocol

import (
	"encoding/json"
	"maps"
	"testing"
)

func TestANSIRolesValidate(t *testing.T) {
	allRoles := ANSIRoles{}
	for _, role := range []string{
		"canvas", "surface", "surface_elevated", "surface_selected", "surface_hover", "slot_background",
		"border", "border_subtle", "border_focused", "ink", "ink_muted", "ink_subtle",
		"accent", "accent_secondary", "accent_soft", "success", "warning", "danger", "info",
	} {
		allRoles[role] = 0
	}
	for _, tc := range []struct {
		name  string
		roles ANSIRoles
		valid bool
	}{
		{"absent", nil, true},
		{"empty", ANSIRoles{}, true},
		{"all roles", allRoles, true},
		{"endpoints", ANSIRoles{"canvas": 0, "ink": 15}, true},
		{"above range", ANSIRoles{"ink": 16}, false},
		{"below range", ANSIRoles{"ink": -1}, false},
		{"name key", ANSIRoles{"name": 0}, false},
		{"unknown key", ANSIRoles{"future_role": 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.roles.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v, want valid %v", err, tc.valid)
			}
		})
	}
}

func TestThemeChangedANSIRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		indices map[string]int
	}{
		{"nil", nil},
		{"empty", map[string]int{}},
		{"indices", map[string]int{"canvas": 0, "ink": 15}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ThemeChangedParams{Palette: Palette{Name: "sample", Canvas: "#000000", Ink: "#ffffff"}, ANSI: tc.indices}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["ansi"]; present != (len(tc.indices) > 0) {
				t.Fatalf("ansi presence: %s", raw)
			}
			if len(tc.indices) == 0 {
				legacy, err := json.Marshal(struct {
					Palette Palette `json:"palette"`
				}{p.Palette})
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != string(legacy) {
					t.Fatalf("hex payload changed: %s, want %s", raw, legacy)
				}
			}
			var got ThemeChangedParams
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Palette != p.Palette || !maps.Equal(got.ANSI, tc.indices) {
				t.Fatalf("round trip = %+v, want %+v", got, p)
			}
		})
	}
}
