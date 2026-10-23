package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

func findProblem(problems []Problem, code string) (Problem, bool) {
	for _, p := range problems {
		if p.Code == code {
			return p, true
		}
	}
	return Problem{}, false
}

// Visibility is exact ownership plus Public; an unknown key is the same
// not_found for every caller, whoever asks.
func TestSettingsVisibilityMatrix(t *testing.T) {
	s := newSet(settings.Options{Schema: []settings.Entry{
		{Key: "dist.pub", Type: "bool", Default: true, Owner: "dist", Apply: "live", Public: true},
		{Key: "dist.priv", Type: "bool", Default: true, Owner: "dist", Apply: "live"},
	}})
	in := catalog("a")
	in.Declared[store.DeclaredPlugins]["b"] = catalog("b").Declared[store.DeclaredPlugins]["b"]
	in.Doc.Doc = doc(`{"nope":{"x":1}}`)
	in.Doc.Origins = map[string]store.Layer{"nope.x": store.LayerUser}
	apply(t, s, in)
	callers := map[string]Caller{
		"cli":    {Kind: "cli"},
		"host":   {Kind: "host"},
		"input":  {Kind: "input"},
		"plugin": {Kind: "plugin", PluginID: "a"},
	}
	// Allowed per caller kind, in the order cli, host, input, plugin: input
	// and host are user-class (everything known and every retained unknown
	// key); a plugin gets its own entries plus public ones.
	cases := []struct {
		key  string
		want [4]bool
	}{
		{"plugins.a.enabled", [4]bool{true, true, true, true}},
		{"plugins.b.enabled", [4]bool{true, true, true, false}},
		{"theme.name", [4]bool{true, true, true, false}},
		{"dist.pub", [4]bool{true, true, true, true}},
		{"dist.priv", [4]bool{true, true, true, false}},
		{"nope.x", [4]bool{true, true, true, false}},
		{"never.registered", [4]bool{false, false, false, false}},
	}
	for _, tc := range cases {
		for i, name := range []string{"cli", "host", "input", "plugin"} {
			_, _, err := s.Get(callers[name], tc.key)
			if (err == nil) != tc.want[i] {
				t.Errorf("%s Get(%s): err=%v, want ok=%v", name, tc.key, err, tc.want[i])
			}
			if err != nil && reason(t, err) != "not_found/unknown_setting" {
				t.Errorf("%s Get(%s): %v, want not_found/unknown_setting", name, tc.key, err)
			}
		}
	}
	// A plugin's snapshot holds exactly its own entries and the public ones.
	view := s.SnapshotFor(callers["plugin"])
	all, err := s.List(callers["cli"])
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]bool{"plugins.a.enabled": true, "dist.pub": true}
	for _, e := range all {
		if _, ok := view.Raw(e.Key); ok != wantKeys[e.Key] {
			t.Errorf("plugin snapshot holds %s = %v, want %v", e.Key, ok, wantKeys[e.Key])
		}
	}
	// An unknown key keeps its raw value for the user listing.
	list, err := s.List(callers["cli"])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range list {
		if e.Key == "nope.x" && e.Source == "user" {
			found = true
		}
	}
	if !found {
		t.Fatal("unknown key missing from settings.list", list)
	}
}

// An unknown key is a warning beside a valid document; an invalid_value
// alone blocks the rebuild and names layer, key, type and file.
func TestSettingsUnknownKeyWarnsAndInvalidValueBlocks(t *testing.T) {
	s := newSet(settings.Options{Schema: []settings.Entry{{Key: "dist.count", Type: "int", Default: int64(1), Owner: "dist", Apply: "live"}}})
	in := input(`{"dist":{"count":3},"nope":{"x":1},"deep":{"er":{"y":true}}}`)
	r := apply(t, s, in)
	for _, k := range []string{"nope.x", "deep.er.y"} {
		p := Problem{}
		for _, q := range r.Problems {
			if q.Code == "unknown_key" && q.Key == k {
				p = q
			}
		}
		if p.Severity != "warning" {
			t.Errorf("unknown_key %s: %+v", k, r.Problems)
		}
	}
	if s.Snapshot().Int("dist.count") != 3 {
		t.Fatal("valid value dropped beside unknown keys")
	}

	bad := input(`{"dist":{"count":"x"}}`)
	bad.Doc.Origins = map[string]store.Layer{"dist.count": store.LayerUser}
	bad.ConfigLayers = []store.LayerDoc{{Layer: store.LayerUser, File: "/cfg/config.json"}}
	before := s.Snapshot()
	cand, r, err := s.Rebuild(context.Background(), bad)
	p, ok := findProblem(r.Problems, "invalid_value")
	if err == nil || cand != nil || !ok || p.Severity != "error" || p.Layer != "user" || p.Key != "dist.count" || p.Source.File != "/cfg/config.json" || !strings.Contains(p.Message, "user") || !strings.Contains(p.Message, "dist.count") || !strings.Contains(p.Message, "int") {
		t.Fatalf("invalid_value = %+v err=%v", r.Problems, err)
	}
	if s.Snapshot() != before {
		t.Fatal("invalid value published")
	}
}

// Every plugin schema problem is a warning against plugin:<id> with the
// plugin id on the source, and none of that plugin's entries registers.
func TestSettingsPluginSchemaProblemsAreWarnings(t *testing.T) {
	cases := []struct {
		name     string
		declared []any
		code     string
		secrets  []any
	}{
		{"foreign key", []any{map[string]any{"key": "plugins.other.x", "type": "bool", "default": false}}, "foreign_schema_key", nil},
		{"husk key", []any{map[string]any{"key": "husk.injected", "type": "bool", "default": false}}, "foreign_schema_key", nil},
		{"duplicate", []any{map[string]any{"key": "plugins.a.k", "type": "bool", "default": false}, map[string]any{"key": "plugins.a.k", "type": "bool", "default": true}}, "duplicate_schema_key", nil},
		{"bad default", []any{map[string]any{"key": "plugins.a.k", "type": "int", "default": "x"}}, "invalid_schema_default", nil},
		{"public", []any{map[string]any{"key": "plugins.a.k", "type": "bool", "default": false, "public": true}}, "invalid_schema_entry", nil},
		{"secret exact", []any{map[string]any{"key": "plugins.a.token", "type": "string", "default": ""}}, "secret_as_setting", []any{map[string]any{"name": "token"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSet(settings.Options{})
			in := catalog("a")
			in.Declared[store.DeclaredPlugins]["b"] = catalog("b").Declared[store.DeclaredPlugins]["b"]
			// A good sibling entry in the same plugin must be dropped with the bad one.
			in.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = append([]any{map[string]any{"key": "plugins.a.good", "type": "bool", "default": false}}, tc.declared...)
			if tc.secrets != nil {
				in.Declared[store.DeclaredPlugins]["a"]["secret_requests"] = tc.secrets
			}
			r := apply(t, s, in) // a plugin schema problem never fails the rebuild
			p, ok := findProblem(r.Problems, tc.code)
			if !ok || p.Severity != "warning" || p.Layer != "plugin:a" || p.Source.Plugin != "a" {
				t.Fatalf("%s = %+v", tc.code, r.Problems)
			}
			if _, _, err := s.Get(Caller{Kind: "cli"}, "plugins.a.good"); err == nil {
				t.Fatal("a rejected plugin kept some of its entries")
			}
			if _, _, err := s.Get(Caller{Kind: "cli"}, "plugins.b.enabled"); err != nil {
				t.Fatal("another plugin was dropped", err)
			}
		})
	}
}

// A secret named like a setting key of a different owner is not a collision.
func TestSettingsSecretCollisionIsOwnerScoped(t *testing.T) {
	s := newSet(settings.Options{})
	in := catalog("a")
	in.Declared[store.DeclaredPlugins]["b"] = catalog("b").Declared[store.DeclaredPlugins]["b"]
	in.Declared[store.DeclaredPlugins]["b"]["settings_declared"] = []any{map[string]any{"key": "plugins.b.token", "type": "string", "default": ""}}
	in.Declared[store.DeclaredPlugins]["a"]["secret_requests"] = []any{map[string]any{"name": "token"}}
	r := apply(t, s, in)
	if _, ok := findProblem(r.Problems, "secret_as_setting"); ok {
		t.Fatal("another owner's setting flagged", r.Problems)
	}
	if _, _, err := s.Get(Caller{Kind: "cli"}, "plugins.b.token"); err != nil {
		t.Fatal(err)
	}
}

// New refuses a bad built-in or distribution schema, naming the entry.
func TestSettingsNewRejectsBadSchemas(t *testing.T) {
	good := settings.Entry{Key: "dist.a", Type: "bool", Default: false, Owner: "dist", Apply: "live"}
	cases := map[string][]settings.Entry{
		"duplicate dist":       {good, good},
		"dist shadows builtin": {{Key: "theme.name", Type: "string", Default: "x", Owner: "dist", Apply: "live"}},
		"dist in plugins":      {{Key: "plugins.x.k", Type: "bool", Default: false, Owner: "dist", Apply: "live"}},
		"bad default":          {{Key: "dist.b", Type: "int", Default: "x", Owner: "dist", Apply: "live"}},
		"bad type":             {{Key: "dist.b", Type: "weird", Default: "x", Owner: "dist", Apply: "live"}},
	}
	for name, entries := range cases {
		s, err := settings.New(settings.Options{Schema: entries})
		if err == nil || s != nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), entries[len(entries)-1].Key) {
			t.Errorf("%s: error %q does not name the entry", name, err)
		}
	}
}

// restart_required: never on the first load; on a later load the in-force
// value stays and the message carries both values.
func TestSettingsRestartRequiredOnSecondLoadOnly(t *testing.T) {
	s := newSet(settings.Options{})
	r := apply(t, s, input(`{"husk":{"plugins":{"paths":["one"]}}}`))
	if _, ok := findProblem(r.Problems, "restart_required"); ok {
		t.Fatal("restart_required on the first load", r.Problems)
	}
	r = apply(t, s, input(`{"husk":{"plugins":{"paths":["two"]}}}`))
	p, ok := findProblem(r.Problems, "restart_required")
	if !ok || p.Severity != "warning" || p.Key != "husk.plugins.paths" || !strings.Contains(p.Message, "one") || !strings.Contains(p.Message, "two") {
		t.Fatalf("restart_required = %+v", r.Problems)
	}
	if fmt.Sprint(s.Snapshot().Strings("husk.plugins.paths")) != "[one]" {
		t.Fatal("in-force value changed")
	}
	// An unchanged value on a later load is quiet.
	r = apply(t, s, input(`{"husk":{"plugins":{"paths":["one"]}}}`))
	if _, ok := findProblem(r.Problems, "restart_required"); ok {
		t.Fatal("restart_required for an unchanged value", r.Problems)
	}
}

// type_clash comes from Store's Resolved.Problems and is a warning.
func TestSettingsTypeClashImported(t *testing.T) {
	s := newSet(settings.Options{})
	in := input(`{}`)
	in.Doc.Problems = []store.Problem{{Code: "type_clash", Path: "theme", Layer: store.LayerUser, LowerLayer: store.LayerDistribution}, {Code: "skipped_layer", Path: "other"}}
	r := apply(t, s, in)
	p, ok := findProblem(r.Problems, "type_clash")
	if !ok || p.Severity != "warning" || p.Layer != "user" || !strings.Contains(p.Message, "user") || !strings.Contains(p.Message, "distribution") || !strings.Contains(p.Message, "theme") {
		t.Fatalf("type_clash = %+v", r.Problems)
	}
	// An object node is no settings key; settings.get would refuse it.
	if p.Key != "" {
		t.Fatalf("type_clash key %q for an object node", p.Key)
	}
	in = input(`{"theme":{"name":"x"}}`)
	in.Doc.Problems = []store.Problem{{Code: "type_clash", Path: "theme.name", Layer: store.LayerUser, LowerLayer: store.LayerDistribution}}
	r = apply(t, s, in)
	if p, _ := findProblem(r.Problems, "type_clash"); p.Key != "theme.name" {
		t.Fatalf("leaf clash key = %q", p.Key)
	}
	if _, ok := findProblem(r.Problems, "skipped_layer"); ok {
		t.Fatal("only type_clash is imported", r.Problems)
	}
}

// The warning sink fires once per key per process, in its fixed format.
func TestSettingsWarnSinkFiresOncePerKey(t *testing.T) {
	var got []string
	s := newSet(settings.Options{Warn: func(msg string) { got = append(got, msg) }})
	apply(t, s, input(`{"nope":{"x":1}}`))
	// A typed read of a key with no schema entry is the unregistered case.
	for i := 0; i < 3; i++ {
		s.Snapshot().Bool("nope.x")
		s.Snapshot().String("other.y")
	}
	want := []string{"settings: unregistered key nope.x", "settings: unregistered key other.y"}
	if len(got) != 2 || !(got[0] == want[0] && got[1] == want[1] || got[0] == want[1] && got[1] == want[0]) {
		t.Fatalf("warn = %q, want once each of %q", got, want)
	}
}

// A value of the wrong shape for a known key is invalid_value whatever it is,
// an object included; an empty object at a schema prefix says nothing.
func TestSettingsWrongShapeForKnownKey(t *testing.T) {
	cases := []struct {
		name, doc string
		code      string // "" means no problem at all
		key       string
	}{
		{"object under string key", `{"theme":{"name":{"x":1}}}`, "invalid_value", "theme.name"},
		{"object under restart key", `{"profile":{"default":{"a":1}}}`, "invalid_value", "profile.default"},
		{"object under palette role", `{"theme":{"palette":{"ink":{"a":1}}}}`, "invalid_value", "theme.palette.ink"},
		{"empty object at prefix", `{"theme":{}}`, "", ""},
		{"empty object at deeper prefix", `{"theme":{"palette":{}}}`, "", ""},
		{"empty object at a leaf", `{"theme":{"name":{}}}`, "invalid_value", "theme.name"},
		{"empty object unknown", `{"nope":{}}`, "unknown_key", "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSet(settings.Options{})
			in := input(tc.doc)
			cand, r, err := s.Rebuild(context.Background(), in)
			if tc.code == "" {
				if err != nil || cand == nil || len(r.Problems) != 0 {
					t.Fatalf("problems = %+v err=%v", r.Problems, err)
				}
				return
			}
			p, ok := findProblem(r.Problems, tc.code)
			if !ok || p.Key != tc.key {
				t.Fatalf("problems = %+v, want %s on %s", r.Problems, tc.code, tc.key)
			}
			if tc.code == "invalid_value" && (err == nil || cand != nil || p.Severity != "error") {
				t.Fatalf("invalid_value must block: err=%v cand=%v", err, cand != nil)
			}
			if _, bad := findProblem(r.Problems, "unknown_key"); bad && tc.code == "invalid_value" {
				t.Fatalf("wrong-shape value also reported as unknown leaves: %+v", r.Problems)
			}
		})
	}
}

// profile.default is read fresh by every profile selection, so it is live:
// a later change applies at once and never asks for a restart.
func TestSettingsProfileDefaultIsLive(t *testing.T) {
	s := newSet(settings.Options{})
	apply(t, s, input(`{"profile":{"default":"one"}}`))
	r := apply(t, s, input(`{"profile":{"default":"two"}}`))
	if _, ok := findProblem(r.Problems, "restart_required"); ok || s.Snapshot().String("profile.default") != "two" {
		t.Fatalf("profile.default = %q, problems %+v", s.Snapshot().String("profile.default"), r.Problems)
	}
}
