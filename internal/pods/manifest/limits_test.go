package manifest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFocusBindingLimitPreservesFirst128AndReportsOneProblem(t *testing.T) {
	bindings := make([]FocusBinding, MaxFocusBindings+1)
	for i := range bindings {
		bindings[i] = FocusBinding{Key: fmt.Sprint(i), Action: "p.new", Focus: "sidebar-list"}
	}
	raw, err := json.Marshal(map[string]any{"manifest_version": 2, "protocol": 2, "id": "p", "name": "P", "version": "1", "command": []string{"plugin"}, "surfaces": []SurfaceDecl{{ID: "sidebar-list"}}, "actions": []ActionDecl{{ID: "p.new"}}, "keybindings": bindings})
	if err != nil {
		t.Fatal(err)
	}
	s, w, err := convertBytes(t, string(raw), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FocusBindings) != MaxFocusBindings || len(s.BindingProblems) != 1 || len(w) != 1 {
		t.Fatal(len(s.FocusBindings), s.BindingProblems, w)
	}
	for i, b := range s.FocusBindings {
		if b.Key != fmt.Sprint(i) {
			t.Fatal("document order changed", i, b)
		}
	}
	if p := s.BindingProblems[0]; p.Code != "too_many" || p.Index != MaxFocusBindings || w[0].Code != "too_many" {
		t.Fatal(p, w)
	}
}

func TestSettingLocalKeysCannotOverlapDottedPluginNamespaces(t *testing.T) {
	for _, tc := range []struct {
		id, key, want string
		bad           bool
	}{
		{"a", "c", "plugins.a.c", false},
		{"a.b", "c", "plugins.a.b.c", false},
		{"a", "b.c", "", true},
		{"a", "plugins.a.c", "", true},
		{"a.b", "plugins.a.b.c", "", true},
		{"Case.Owner", "local_key-1", "plugins.Case.Owner.local_key-1", false},
	} {
		t.Run(tc.id+"/"+tc.key, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"manifest_version": 2, "protocol": 2, "id": tc.id, "name": "P", "version": "1", "command": []string{"plugin"}, "settings": []SettingDecl{{Key: tc.key, Type: "bool", Default: json.RawMessage(`false`)}}})
			s, _, err := convertBytes(t, string(raw), true)
			if (err != nil) != tc.bad {
				t.Fatalf("spec=%+v err=%v", s, err)
			}
			if !tc.bad && s.SettingsDeclared[0].Key != tc.want {
				t.Fatal(s.SettingsDeclared)
			}
		})
	}
}

// focus_bindings is no longer an alias of keybindings: its entries bind
// nothing and the manifest says so.
func TestFocusBindingsSpellingIsNotAnAlias(t *testing.T) {
	raw := `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],
 "surfaces":[{"id":"sidebar-list"}],"actions":[{"id":"p.new"}],
 "focus_bindings":[{"key":"n","action":"p.new","focus":"sidebar-list"}]}`
	s, w, err := convertBytes(t, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FocusBindings) != 0 || len(s.BindingProblems) != 0 {
		t.Fatalf("focus_bindings still read: %+v %+v", s.FocusBindings, s.BindingProblems)
	}
	if len(w) != 1 || w[0].Code != "ignored_field" || w[0].Field != "focus_bindings" {
		t.Fatalf("warnings = %+v, want one ignored_field for focus_bindings", w)
	}
	raw = strings.Replace(raw, "focus_bindings", "keybindings", 1)
	if s, _, err = convertBytes(t, raw, true); err != nil || len(s.FocusBindings) != 1 {
		t.Fatalf("keybindings not read: %+v %v", s, err)
	}
}
