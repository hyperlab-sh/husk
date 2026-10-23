package services

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func codes(ps []protocol.Problem) map[string]protocol.Problem {
	out := map[string]protocol.Problem{}
	for _, p := range ps {
		out[p.Code] = p
	}
	return out
}

// keys.list carries the shared Problem unchanged: severity, code and source
// all serialize, and the source keeps its file, line-less shape.
func TestKeysListProblemIsTheSharedProblem(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"shift+1","action":"husk.home"}]}`)}}
	apply(t, kb, in)
	var got *protocol.Problem
	for _, p := range kb.Listing().Problems {
		if p.Code == "invalid_key" {
			p := p
			got = &p
		}
	}
	if got == nil || got.Severity != "warning" || got.Layer != "user" || got.Source == nil || got.Source.Layer != "user" {
		t.Fatalf("problem = %+v", got)
	}
	b, _ := json.Marshal(got)
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"severity", "code", "layer", "key", "message", "source"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("wire problem lacks %q: %s", k, b)
		}
	}
}

// Every problem a Service raises has a code.
func TestServiceProblemsCarryCodes(t *testing.T) {
	// settings: invalid value, unknown key.
	s := settings.New(settings.Options{})
	in := input(`{"theme":{"allow_low_contrast":"yes"},"nothing":{"here":1}}`)
	_, rep, _ := s.Rebuild(context.Background(), in)
	got := codes(rep.Problems)
	if got["invalid_value"].Severity != "error" {
		t.Fatalf("no invalid_value: %+v", rep.Problems)
	}
	if got["unknown_key"].Code == "" {
		t.Fatalf("no unknown_key: %+v", rep.Problems)
	}
	// settings: restart_required on a second load.
	s = settings.New(settings.Options{})
	in = catalog("new")
	in.Declared[store.DeclaredPlugins]["new"]["settings_declared"] = []any{map[string]any{"key": "plugins.new.enabled", "type": "bool", "default": false, "apply": "restart"}}
	apply(t, s, in)
	in.Doc.Doc = doc(`{"plugins":{"new":{"enabled":true}}}`)
	if _, rep, _ := s.Rebuild(context.Background(), in); codes(rep.Problems)["restart_required"].Severity != "warning" {
		t.Fatalf("no restart_required: %+v", rep.Problems)
	}
	// profile: unknown_profile.
	p, _ := profile.New(profile.Options{Store: &fakeStore{}, State: state.New(state.Options{}), Post: func(f func()) bool { f(); return true }})
	in = input(`{"profile":{"default":"ghost"}}`)
	if _, rep, _ := p.Rebuild(context.Background(), in); codes(rep.Problems)["unknown_profile"].Severity != "warning" {
		t.Fatalf("no unknown_profile: %+v", rep.Problems)
	}
}

// A malformed plugin catalog is invalid_plugin_catalog.
func TestInvalidPluginCatalogCode(t *testing.T) {
	in := input(`{}`)
	in.Declared[store.DeclaredPlugins] = map[string]store.Doc{"a": doc(`{"id":"b"}`)}
	_, rep, _ := keybinding.New().Rebuild(context.Background(), in)
	if p := codes(rep.Problems)["invalid_plugin_catalog"]; p.Layer != "plugin:a" {
		t.Fatalf("problems = %+v", rep.Problems)
	}
}
