package services

import (
	"context"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	services "github.com/hyperlab-sh/husk/pkg/services"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestKeysListViewErrorsAreCoded(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code protocol.Code
	}{
		{"deadline", context.DeadlineExceeded, protocol.CodeTimeout},
		{"cancelled", context.Canceled, protocol.CodeCancelled},
		{"loop gone", contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil), protocol.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := Keys{Svc: keybinding.New(), View: func(context.Context) (KeyView, error) { return KeyView{}, tc.err }}
			_, err := port.List(t.Context(), userCaller)
			if got := contracts.CodeOf(err); got != string(tc.code) {
				t.Fatalf("code = %q, want %q (error: %v)", got, tc.code, err)
			}
		})
	}
}

func TestKeysListScopedRuntimeView(t *testing.T) {
	svc := keybinding.New()
	applyTo(t, svc, services.ReloadInput{
		Layers: []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(t, `{"version":1,"bind":[{"key":"x","action":"files.run"},{"key":"y","action":"other.run"}]}`)}},
		Declared: map[store.DocName]map[string]store.Doc{store.DeclaredPlugins: {
			"files": doc(t, `{"id":"files","actions":[{"id":"files.run"}]}`),
			"other": doc(t, `{"id":"other","actions":[{"id":"other.run"}]}`),
		}},
	})
	for _, caller := range []contracts.Caller{userCaller, filesCaller} {
		for _, nilView := range []bool{false, true} {
			name := string(caller.Kind) + map[bool]string{false: "/runtime", true: "/nil"}[nilView]
			t.Run(name, func(t *testing.T) {
				port := Keys{Svc: svc}
				want := "unknown_action"
				if !nilView {
					port.View = func(context.Context) (KeyView, error) {
						return KeyView{Actions: map[string]bool{"files.run": false}}, nil
					}
					want = "unavailable"
				}
				listing, err := port.List(t.Context(), caller)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range listing.Entries {
					if caller.Kind == protocol.CallerPlugin && entry.Action == "other.run" {
						t.Error("foreign action leaked")
					}
					if nilView && entry.State == "effective" {
						t.Error("empty catalog left an effective entry")
					}
					if entry.Action == "files.run" {
						found = true
						if entry.State != want {
							t.Errorf("state = %q, want %q", entry.State, want)
						}
					}
				}
				if !found {
					t.Fatal("own action missing")
				}
			})
		}
	}
}

func TestKeysListRuntimeStates(t *testing.T) {
	for _, tc := range []struct {
		name, action, plugin, focus, want                          string
		known, actionLive, pluginLive, surface, shadowed, disabled bool
	}{
		{name: "unknown action", action: "files.run", want: "unknown_action"},
		{name: "live action", action: "files.run", known: true, actionLive: true, want: "effective"},
		{name: "dead action owner", action: "files.run", known: true, want: "unavailable"},
		{name: "conditioned live surface", action: "files.run", known: true, actionLive: true, plugin: "other", focus: "sidebar-list", pluginLive: true, surface: true, want: "effective"},
		{name: "conditioned dead plugin", action: "files.run", known: true, actionLive: true, plugin: "other", focus: "sidebar-list", surface: true, want: "unavailable"},
		{name: "conditioned missing surface", action: "files.run", known: true, actionLive: true, plugin: "other", focus: "sidebar-list", pluginLive: true, want: "unavailable"},
		{name: "unknown precedes unavailable", action: "files.run", plugin: "other", focus: "sidebar-list", want: "unknown_action"},
		{name: "action owner independent of condition", action: "files.run", known: true, plugin: "other", focus: "sidebar-list", pluginLive: true, surface: true, want: "unavailable"},
		{name: "unconditioned arbitrary surface", action: "files.run", known: true, actionLive: true, focus: "undeclared", want: "effective"},
		{name: "host action", action: "husk.home", known: true, actionLive: true, want: "effective"},
		{name: "host action with dead condition", action: "husk.home", known: true, actionLive: true, plugin: "other", focus: "sidebar-list", want: "unavailable"},
		{name: "unknown host action", action: "husk.missing", want: "unknown_action"},
		{name: "shadowed precedes unknown and unavailable", action: "files.run", plugin: "other", focus: "sidebar-list", shadowed: true, want: "shadowed"},
		{name: "shadowed precedes unavailable", action: "files.run", known: true, plugin: "other", focus: "sidebar-list", shadowed: true, want: "shadowed"},
		{name: "disabled precedes unavailable", plugin: "other", focus: "sidebar-list", disabled: true, want: "disabled"},
		{name: "shadowed tombstone", plugin: "other", focus: "sidebar-list", disabled: true, shadowed: true, want: "shadowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := map[string]any{"key": "x"}
			if tc.disabled {
				binding["disable"] = true
			} else {
				binding["action"] = tc.action
			}
			if tc.focus != "" {
				binding["focus"] = tc.focus
			}
			if tc.plugin != "" {
				binding["plugin"] = tc.plugin
			}
			layers := []store.LayerDoc{{Layer: store.LayerUser, Doc: store.Doc{"version": 1, "bind": []any{binding}}}}
			if tc.shadowed {
				override := map[string]any{"key": "x", "disable": true}
				if tc.focus != "" {
					override["focus"] = tc.focus
				}
				layers = append(layers, store.LayerDoc{Layer: store.LayerProfile, Doc: store.Doc{"version": 1, "bind": []any{override}}})
			}
			svc := keybinding.New()
			applyTo(t, svc, services.ReloadInput{Layers: layers})
			before := svc.Listing()
			view := KeyView{Actions: map[string]bool{}, Surfaces: map[string]map[string]bool{}}
			if tc.known {
				view.Actions[tc.action] = tc.actionLive
			}
			if tc.pluginLive {
				view.Surfaces[tc.plugin] = map[string]bool{tc.focus: tc.surface}
			}
			port := Keys{Svc: svc, View: func(context.Context) (KeyView, error) { return view, nil }}
			listing, err := port.List(t.Context(), userCaller)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range listing.Entries {
				if entry.Source.Layer != "user" {
					continue
				}
				found = true
				if entry.State != tc.want {
					t.Errorf("state = %q, want %q", entry.State, tc.want)
				}
				if tc.shadowed && (entry.ShadowedBy == nil || entry.ShadowedBy.Layer != "profile") {
					t.Error("lost shadow source")
				}
			}
			if !found {
				t.Fatal("missing binding")
			}
			if !reflect.DeepEqual(before, svc.Listing()) {
				t.Error("runtime join mutated static listing")
			}
		})
	}
}
