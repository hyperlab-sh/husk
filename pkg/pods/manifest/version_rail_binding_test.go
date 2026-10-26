package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsupportedManifestVersionIsRefusedNamingIt(t *testing.T) {
	const rest = `"id":"p","name":"P","version":"1","command":["./plugin"]`
	for _, tc := range []struct{ name, raw, want string }{
		{"three", `{"manifest_version":3,"protocol":2,` + rest + `}`, "3"},
		{"string", `{"manifest_version":"2","protocol":2,` + rest + `}`, `"2"`},
		{"one", `{"manifest_version":1,` + rest + `}`, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s, _, err := NewRegistry().LoadPath(dir)
			if err == nil || !strings.Contains(err.Error(), "unsupported manifest_version "+tc.want) {
				t.Fatalf("spec=%v err=%v", s, err)
			}
		})
	}
}

func TestAbsentOrNullManifestVersionStillLoadsAsV1(t *testing.T) {
	for _, raw := range []string{
		`{"id":"p","name":"P","version":"1","command":["./plugin"]}`,
		`{"manifest_version":null,"id":"p","name":"P","version":"1","command":["./plugin"]}`,
	} {
		if s, _, err := convertBytes(t, raw, false); err != nil || s.ID != "p" {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestKeybindingMayTargetOwnRailItemButNotAnothersPlugins(t *testing.T) {
	raw := `{` + railBase + `,"surfaces":[{"id":"sidebar-list"}],
	  "rail_items":[{"id":"p.panel","icon":"P","label":"Panel","placement":"modal"}],
	  "keybindings":[
	    {"key":"a","action":"p.panel","focus":"sidebar-list"},
	    {"key":"b","action":"other.panel","focus":"sidebar-list"}]}`
	s, _, err := convertBytes(t, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Keybindings) != 1 || s.Keybindings[0].Action != "p.panel" {
		t.Fatalf("keybindings = %+v", s.Keybindings)
	}
	if len(s.BindingProblems) != 1 || s.BindingProblems[0].Index != 1 || s.BindingProblems[0].Code != "foreign_action" {
		t.Fatalf("problems = %+v", s.BindingProblems)
	}
}
