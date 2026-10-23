package settings

import (
	"reflect"
	"slices"
	"testing"
)

func TestBuiltinsExactLaunchSchema(t *testing.T) {
	want := []string{"profile.default", "theme.name", "theme.allow_low_contrast", "husk.plugins.paths", "husk.plugins.dirs"}
	for _, role := range []string{"accent", "accent_secondary", "accent_soft", "border", "border_focused", "border_subtle", "canvas", "danger", "info", "ink", "ink_muted", "ink_subtle", "slot_background", "success", "surface", "surface_elevated", "surface_hover", "surface_selected", "warning"} {
		want = append(want, "theme.palette."+role)
	}
	slices.Sort(want)
	var got []string
	for _, entry := range Builtins() {
		got = append(got, entry.Key)
		if entry.Public || entry.Owner != "husk" {
			t.Fatalf("host entry exposure: %+v", entry)
		}
		if entry.Key == "husk.plugins.paths" || entry.Key == "husk.plugins.dirs" {
			if entry.Doc == "" || entry.Apply != "restart" || entry.Type != TypeStrings || !reflect.DeepEqual(entry.Default, []string{}) {
				t.Fatalf("launch setting: %+v", entry)
			}
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys=%v want=%v", got, want)
	}
}
