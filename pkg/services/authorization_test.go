package services

import (
	"context"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/services/settings"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestPluginSettingsExposureUsesExactDeclarationsNotPrefixes(t *testing.T) {
	s := newSet(settings.Options{Schema: []settings.Entry{{Key: "dist.theme", Type: "string", Default: "Public", Owner: "dist", Apply: "live", Public: true}}})
	in := catalog("a")
	in.Declared[store.DeclaredPlugins]["a.b"] = catalog("a.b").Declared[store.DeclaredPlugins]["a.b"]
	in.Declared[store.DeclaredPlugins]["b"] = catalog("b").Declared[store.DeclaredPlugins]["b"]
	in.Declared[store.DeclaredPlugins]["b"]["settings_declared"] = []any{map[string]any{"key": "plugins.b.y", "type": "bool", "default": true}}
	in.Doc.Doc = doc(`{"plugins":{"a":{"enabled":true},"a.b":{"enabled":false},"b":{"y":true}}}`)
	apply(t, s, in)
	a := Caller{Kind: "plugin", PluginID: "a"}
	ab := Caller{Kind: "plugin", PluginID: "a.b"}
	for _, caller := range []Caller{a, ab} {
		own := "plugins." + caller.PluginID + ".enabled"
		if _, _, err := s.Get(caller, own); err != nil {
			t.Fatal("own declaration refused", err)
		}
		foreign := "plugins.a.b.enabled"
		if caller.PluginID == "a.b" {
			foreign = "plugins.a.enabled"
		}
		if _, _, err := s.Get(caller, foreign); err == nil {
			t.Fatal("dotted-id settings leak", caller, foreign)
		}
		view := s.SnapshotFor(caller)
		if _, ok := view.Raw(own); !ok {
			t.Fatal("own snapshot key lost")
		}
		if _, ok := view.Raw(foreign); ok {
			t.Fatal("foreign raw snapshot key exposed")
		}
		if view.Bool(foreign) {
			t.Fatal("foreign typed snapshot key exposed")
		}
		if view.String("dist.theme") != "Public" {
			t.Fatal("public host snapshot key lost")
		}
		if _, ok := view.Raw("plugins.b.y"); ok {
			t.Fatal("unrelated plugin snapshot key exposed")
		}
	}
	if v, _, err := s.Get(a, "dist.theme"); err != nil || v != "Public" {
		t.Fatal("public host key not readable", err)
	}
	if _, _, err := s.Get(a, "plugins.b.y"); err == nil {
		t.Fatal("unrelated plugin key exposed")
	}
	in.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = []any{map[string]any{"key": "plugins.a.nested.key", "type": "bool", "default": false}}
	_, r, err := s.Rebuild(context.Background(), in)
	if err != nil || !hasProblem(r.Problems, "foreign_schema_key", "warning") {
		t.Fatal("dotted local key not refused as a plugin schema warning", r.Problems, err)
	}
	if _, _, err := s.Get(a, "plugins.a.nested.key"); err == nil {
		t.Fatal("refused dotted key readable")
	}
	delete(in.Declared[store.DeclaredPlugins]["a"], "settings_declared")
	in.Doc.Doc = doc(`{"plugins":{"a.b":{"unknown":true}}}`)
	_, r, err = s.Rebuild(context.Background(), in)
	if err != nil || !hasProblem(r.Problems, "unknown_key", "warning") {
		t.Fatal("parent id's policy masked child's unknown key", r.Problems, err)
	}
}
