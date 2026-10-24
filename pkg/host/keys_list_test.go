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

func TestKeyViewLoopGone(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.shutdown()
	_, err = h.root.keyView(t.Context())
	if got := contracts.CodeOf(err); got != string(protocol.CodeUnavailable) {
		t.Fatalf("code = %q, want unavailable (error: %v)", got, err)
	}
}

func TestKeyViewDeclaredSurfacesCache(t *testing.T) {
	h, _, _ := bootFrontend(t, nil)
	h.loop(func() {
		first := h.root.readCatalog()
		if !first.surfaces["alpha"]["sidebar-list"] {
			t.Fatal("unframed surface missing from cached declarations")
		}
		view, err := h.root.keyView(t.Context())
		if err != nil || !view.Surfaces["alpha"]["sidebar-list"] {
			t.Fatalf("initial view = %+v, error: %v", view, err)
		}
		// Replacing a declaration advances DeclaredRevision. A new listing
		// sees the new surface without modifying a previously captured view.
		h.root.store.Declare(store.DeclaredPlugins, "alpha", store.Doc{"surfaces": []any{map[string]any{"id": "main-stage"}}})
		second := h.root.readCatalog()
		if second.surfaces["alpha"]["sidebar-list"] || !second.surfaces["alpha"]["main-stage"] {
			t.Fatalf("stale cached surfaces: %v", second.surfaces)
		}
		next, err := h.root.keyView(t.Context())
		if err != nil || next.Surfaces["alpha"]["sidebar-list"] || !next.Surfaces["alpha"]["main-stage"] {
			t.Fatalf("updated view = %+v, error: %v", next, err)
		}
		if !view.Surfaces["alpha"]["sidebar-list"] || view.Surfaces["alpha"]["main-stage"] {
			t.Fatal("captured view was mutated")
		}
	})
}

func TestKeysListRuntimeView(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	h.loop(func() {
		if err := fe.RegisterHostAction("husk.test", func(map[string]any) (map[string]any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
		if err := fe.RegisterHostAction("alpha.panel", func(map[string]any) (map[string]any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
		h.root.store.Declare(store.DeclaredPlugins, "dead", store.Doc{
			"id":         "dead",
			"actions":    []any{map[string]any{"id": "dead.run"}},
			"surfaces":   []any{map[string]any{"id": "sidebar-list"}},
			"rail_items": []any{map[string]any{"id": "dead.panel"}, map[string]any{"id": "dead.unregistered"}},
		})
		if err := fe.RegisterHostAction("dead.panel", func(map[string]any) (map[string]any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
		for plugin, keys := range map[string][]string{"alpha": {"j", "m"}, "dead": {"k", "l"}} {
			action := plugin + map[string]string{"alpha": ".open", "dead": ".run"}[plugin]
			h.root.store.Declare(store.DeclaredKeymap, plugin, store.Doc{"version": 1, "bind": []any{
				map[string]any{"key": keys[0], "action": "husk.test", "focus": "sidebar-list"},
				map[string]any{"key": keys[1], "action": action, "focus": "sidebar-list"},
			}})
		}
		bindings := []any{
			map[string]any{"key": "a", "action": "alpha.open"},
			map[string]any{"key": "b", "action": "dead.run"},
			map[string]any{"key": "c", "action": "husk.missing"},
			map[string]any{"key": "d", "action": "husk.test", "plugin": "alpha", "focus": "sidebar-list"},
			map[string]any{"key": "e", "action": "husk.test", "plugin": "alpha", "focus": "main-stage"},
			map[string]any{"key": "f", "action": "alpha.panel"},
			map[string]any{"key": "g", "action": "alpha.open", "focus": "undeclared"},
			map[string]any{"key": "i", "action": "husk.test", "plugin": "dead", "focus": "sidebar-list"},
			map[string]any{"key": "n", "action": "dead.panel"},
			map[string]any{"key": "o", "action": "dead.unregistered"},
		}
		candidate, _, err := h.root.rt.Keybinding.Rebuild(t.Context(), services.ReloadInput{
			Layers: []store.LayerDoc{{Layer: store.LayerUser, Doc: store.Doc{"version": 1, "bind": bindings}}},
			Declared: map[store.DocName]map[string]store.Doc{
				store.DeclaredKeymap:  h.root.store.Declared(store.DeclaredKeymap),
				store.DeclaredPlugins: h.root.store.Declared(store.DeclaredPlugins),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if report := candidate.Apply(); !report.Applied {
			t.Fatal("keymap candidate not applied")
		}
	})
	want := map[string]string{"a": "effective", "b": "unavailable", "c": "unknown_action", "d": "effective", "e": "unavailable", "f": "effective", "g": "effective", "i": "unavailable", "j": "effective", "k": "unavailable", "l": "unavailable", "m": "effective", "n": "effective", "o": "unknown_action"}
	check := func(listing contracts.KeyListing) {
		t.Helper()
		got := map[string]string{}
		for _, entry := range listing.Entries {
			if entry.Source.Layer == "user" || entry.Source.Layer == "focus-local" {
				got[entry.Keys[0]] = entry.State
				if entry.Source.Layer == "focus-local" && entry.Plugin != entry.Source.Plugin {
					t.Error("focus-local plugin condition lost")
				}
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
