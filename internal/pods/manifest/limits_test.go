package manifest

import (
	"encoding/json"
	"fmt"
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
