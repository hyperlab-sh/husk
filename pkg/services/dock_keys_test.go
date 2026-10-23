package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
)

// TestEmbeddedHuskLayerResolvesDockKeys pins the three host dock keys to the
// built-in husk layer: they come from the embedded document, not from a
// distribution fragment.
func TestEmbeddedHuskLayerResolvesDockKeys(t *testing.T) {
	kb := keybinding.New()
	listing := kb.Listing()
	for _, problem := range listing.Problems {
		t.Fatalf("embedded keymap problem: %+v", problem)
	}
	cases := []struct{ key, action string }{
		{"[", "husk.sidebar.toggle"},
		{"]", "husk.sidepanel.toggle"},
		{"/", "husk.docks.cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			r := kb.Resolve(keybinding.KeyContext{}, []string{tc.key})
			if r.Outcome != keybinding.Match || r.Binding == nil {
				t.Fatalf("%q resolved to outcome %v (binding %v), want Match %q", tc.key, r.Outcome, r.Binding, tc.action)
			}
			if r.Binding.Action != tc.action {
				t.Fatalf("%q resolved to %q, want %q", tc.key, r.Binding.Action, tc.action)
			}
			found := false
			for _, e := range listing.Entries {
				if e.Binding.Action == tc.action && len(e.Binding.Keys) == 1 && e.Binding.Keys[0] == tc.key {
					found = true
				}
			}
			if !found {
				t.Fatalf("%q -> %q missing from the effective listing", tc.key, tc.action)
			}
		})
	}
}
