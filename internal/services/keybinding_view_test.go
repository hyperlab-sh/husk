package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestListForPluginView(t *testing.T) {
	kb := keybinding.New()
	in := catalog("own")
	in.Declared[store.DeclaredPlugins]["other"] = doc(`{"id":"other","actions":[{"id":"other.run"}],"rail_items":[{"id":"other.panel","placement":"modal"}]}`)
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{
		"own":   doc(`{"version":1,"bind":[{"key":"x","focus":"main-stage","action":"own.run"}],"problems":[{"code":"foreign_action","index":0}]}`),
		"other": doc(`{"version":1,"bind":[{"key":"y","focus":"main-stage","action":"other.run"}],"problems":[{"code":"foreign_action","index":0}]}`),
	}
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[
  {"key":"1","action":"own.run"},
  {"key":"2","action":"husk.custom"},
  {"key":"q","disable":true},
  {"key":"3","action":"other.panel"},
  {"key":"4","action":"other.run"},
  {"key":"5","action":"other.panel"},
  {"key":"5","disable":true},
  {"key":"6","action":"husk.custom"},
  {"key":"6","action":"husk.custom"}
 ]}`)}, {Layer: store.LayerProfile, Doc: doc(`{"version":1,"bind":[{"key":"5","disable":true},{"key":"7","action":"own.run"},{"key":"7","action":"own.run"}]}`)}}
	apply(t, kb, in)
	full := kb.Listing()
	view, err := kb.ListFor(Caller{Kind: protocol.CallerPlugin, PluginID: "own"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, action, state string
		shown                    bool
	}{
		{"own declared", "x", "own.run", "effective", true},
		{"own configured", "1", "own.run", "effective", true},
		{"host effective", "2", "husk.custom", "effective", true},
		{"host shadowed", "q", "husk.quit", "shadowed", false},
		{"other tombstone", "q", "", "disabled", false},
		{"declared rail item", "3", "other.panel", "effective", true},
		{"other action", "4", "other.run", "effective", false},
		{"shadowed rail item", "5", "other.panel", "shadowed", false},
		{"rail tombstone", "5", "", "disabled", false},
		{"own shadowed", "7", "own.run", "shadowed", true},
		{"shadowed host", "6", "husk.custom", "shadowed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			find := func(entries []keybinding.Entry) bool {
				for _, e := range entries {
					if len(e.Keys) == 1 && e.Keys[0] == tc.key && e.Action == tc.action && e.State == tc.state {
						return true
					}
				}
				return false
			}
			if !find(full.Entries) {
				t.Fatalf("fixture missing %s/%s/%s", tc.key, tc.action, tc.state)
			}
			if got := find(view.Entries); got != tc.shown {
				t.Fatalf("shown=%v, want %v", got, tc.shown)
			}
		})
	}
	if len(view.Problems) != 1 || view.Problems[0].Source == nil || view.Problems[0].Source.Plugin != "own" {
		t.Fatalf("plugin problems=%+v", view.Problems)
	}
	for _, kind := range []protocol.CallerKind{protocol.CallerCLI, protocol.CallerHost} {
		got, err := kb.ListFor(Caller{Kind: kind})
		if err != nil || len(got.Entries) != len(full.Entries) || len(got.Problems) != len(full.Problems) {
			t.Fatalf("trusted listing filtered: %+v %v", got, err)
		}
	}
	// Detached results cannot mutate the published listing or rail-id index.
	view.Entries[0].Keys[0] = "changed"
	again, _ := kb.ListFor(Caller{Kind: protocol.CallerPlugin, PluginID: "own"})
	if again.Entries[0].Keys[0] == "changed" {
		t.Fatal("plugin listing shared mutable storage")
	}
}
