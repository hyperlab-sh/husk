package services

import (
	"context"
	"reflect"
	"testing"

	services "github.com/hyperlab-sh/husk/pkg/services"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

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
