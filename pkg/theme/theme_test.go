package theme

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// The palette travels in welcome/theme_changed. It must carry generic roles
// only: application colour sets (such as per-space colours) belong to the
// plugin that needs them.
func TestDefaultPaletteWireKeysAreGenericRoles(t *testing.T) {
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{
		"accent", "accent_secondary", "accent_soft", "border", "border_focused",
		"border_subtle", "canvas", "danger", "info", "ink", "ink_muted",
		"ink_subtle", "name", "slot_background", "success", "surface", "surface_elevated",
		"surface_hover", "surface_selected", "warning",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("palette wire keys = %v, want %v", keys, want)
	}
	for k, v := range got {
		if v == "" {
			t.Fatalf("default palette role %q is empty", k)
		}
	}
}

// An older palette JSON with extra keys must still decode: removed keys are
// ignored, so plugins that still send or cache them keep working.
func TestPaletteDecodesLegacyExtraKeys(t *testing.T) {
	var p Palette
	if err := json.Unmarshal([]byte(`{"name":"x","accent":"63","space_green":"10"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "x" || p.Accent != "63" {
		t.Fatalf("decoded palette = %+v", p)
	}
}
