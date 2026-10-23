package main

import (
	"slices"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

// The no-host fallback ranks as the keybinding service does: scope first,
// then layer (Extra stands in for plugin-declared bindings, above the built-in
// layer), then file order.
func TestDefaultKeysRankScopeThenLayer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []defaultBinding
		focus string
		key   string
		want  string
	}{
		{"surface-scoped extra beats a global extra", []defaultBinding{{"r", "p.global", ""}, {"r", "p.surface", "main-stage"}}, "main-stage", "r", "p.surface"},
		{"surface-scoped built-in beats a global extra", []defaultBinding{{"esc", "p.global", ""}}, "modal-surface", "esc", "husk.modal.close"},
		{"extra beats the built-in layer in the same scope", []defaultBinding{{"q", "p.quit", ""}}, "main-stage", "q", "p.quit"},
		{"first extra wins a tie", []defaultBinding{{"r", "p.first", ""}, {"r", "p.second", ""}}, "main-stage", "r", "p.first"},
		{"surface-scoped extra is silent elsewhere", []defaultBinding{{"r", "p.surface", "main-stage"}, {"r", "p.global", ""}}, "sidebar-list", "r", "p.global"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := (defaultKeys{Extra: tc.extra}).Resolve(contracts.KeyContext{FocusSurface: tc.focus}, []string{tc.key})
			if res.Outcome != contracts.KeyMatch || res.Binding.Action != tc.want {
				t.Fatalf("Resolve = %+v, want %s", res, tc.want)
			}
		})
	}
}

// KeysFor lists global spellings first, then surface-scoped, strongest layer
// first within each group.
func TestDefaultKeysForOrder(t *testing.T) {
	d := defaultKeys{Extra: []defaultBinding{{"x", "husk.quit", ""}, {"y", "husk.quit", "main-stage"}}}
	if got, want := d.KeysFor("husk.quit"), []string{"x", "q", "ctrl+c", "y"}; !slices.Equal(got, want) {
		t.Fatalf("KeysFor = %v, want %v", got, want)
	}
}
