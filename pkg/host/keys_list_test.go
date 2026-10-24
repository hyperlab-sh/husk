package host

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestKeysListRuntimeView(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	h.loop(func() {
		if err := fe.RegisterHostAction("husk.test", func(map[string]any) (map[string]any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
		if err := fe.RegisterHostAction("alpha.panel", func(map[string]any) (map[string]any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
		h.root.store.Declare(store.DeclaredPlugins, "dead", store.Doc{"actions": []any{map[string]any{"id": "dead.run"}}})
		bindings := []any{
			map[string]any{"key": "a", "action": "alpha.open"},
			map[string]any{"key": "b", "action": "dead.run"},
			map[string]any{"key": "c", "action": "husk.missing"},
			map[string]any{"key": "d", "action": "husk.test", "plugin": "alpha", "focus": "sidebar-list"},
			map[string]any{"key": "e", "action": "husk.test", "plugin": "alpha", "focus": "main-stage"},
			map[string]any{"key": "f", "action": "alpha.panel"},
			map[string]any{"key": "g", "action": "alpha.open", "focus": "undeclared"},
			map[string]any{"key": "i", "action": "husk.test", "plugin": "dead", "focus": "sidebar-list"},
		}
		candidate, _, err := h.root.rt.Keybinding.Rebuild(t.Context(), services.ReloadInput{
			Layers: []store.LayerDoc{{Layer: store.LayerUser, Doc: store.Doc{"version": 1, "bind": bindings}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		candidate.Apply()
	})
	want := map[string]string{"a": "effective", "b": "unavailable", "c": "unknown_action", "d": "effective", "e": "unavailable", "f": "effective", "g": "effective", "i": "unavailable"}
	check := func(listing contracts.KeyListing) {
		t.Helper()
		got := map[string]string{}
		for _, entry := range listing.Entries {
			if entry.Source.Layer == "user" {
				got[entry.Keys[0]] = entry.State
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("states = %v, want %v", got, want)
		}
	}
	check(fe.KeyListing())                    // off-loop frontend call
	h.loop(func() { check(fe.KeyListing()) }) // on-loop must not post to itself
	id := protocol.NewStringID("keys")
	reply := h.root.mux.Serve(t.Context(), contracts.Caller{Kind: protocol.CallerCLI}, protocol.Message{ID: &id, Method: protocol.MethodKeysList}, nil)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var listing contracts.KeyListing
	if err := json.Unmarshal(reply.Result, &listing); err != nil {
		t.Fatal(err)
	}
	check(listing)
}
