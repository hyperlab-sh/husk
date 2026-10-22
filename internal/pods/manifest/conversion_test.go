package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func convertBytes(t *testing.T, raw string, modern bool) (*Spec, []Warning, error) {
	t.Helper()
	src := Source{Path: filepath.Join(t.TempDir(), FileName), Dir: t.TempDir(), Bytes: []byte(raw)}
	if modern {
		return (V2Converter{}).Convert(src)
	}
	return (V1Converter{}).Convert(src)
}

func TestManifestConversionFailuresAndLegacyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, raw   string
		modern, bad bool
	}{
		{"v1", `{"id":"p","name":"P","version":"1","command":["./plugin"],"slots":["main-stage"],"transport":"socket","language":"go","hooks":[{"id":"p.before"}],"events":[{"id":"p.changed"}]}`, false, false},
		{"v2", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"actions":[{"id":"p.new","title":"New"}]}`, true, false},
		{"null protocol", `{"manifest_version":2,"protocol":null,"id":"p","name":"P","version":"1","command":["plugin"]}`, true, true},
		{"missing protocol", `{"manifest_version":2,"id":"p","name":"P","version":"1","command":["plugin"]}`, true, true},
		{"wrong major", `{"manifest_version":2,"protocol":1,"id":"p","name":"P","version":"1","command":["plugin"]}`, true, true},
		{"bad id", `{"manifest_version":2,"protocol":2,"id":"bad/id","name":"P","version":"1","command":["plugin"]}`, true, true},
		{"reserved id", `{"id":"husk","name":"P","version":"1","command":["plugin"]}`, false, true},
		{"foreign declaration", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"actions":[{"id":"other.new"}]}`, true, true},
		{"event wildcard", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"events":{"subscribe":["p.*.bad"]}}`, true, true},
		{"region outside claim", `{"id":"p","name":"P","version":"1","command":["plugin"],"published_slots":[{"id":"p.toolbar","kind":"region","rect":{"surface":"main-stage","x":0,"y":0,"width":1,"height":1}}]}`, false, true},
		{"default absent", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"settings":[{"key":"x","type":"bool"}]}`, true, true},
		{"foreign setting", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"settings":[{"key":"plugins.other.x","type":"bool","default":false}]}`, true, true},
		{"retention missing", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"state":{"persistent":true}}`, true, true},
		{"oversize slice", `{"manifest_version":2,"protocol":2,"id":"p","name":"P","version":"1","command":["plugin"],"state":{"max_bytes":1048577}}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, err := convertBytes(t, tc.raw, tc.modern)
			if (err != nil) != tc.bad {
				t.Fatalf("spec=%+v warnings=%+v err=%v", s, w, err)
			}
			if !tc.bad && s.Protocol != 2 {
				t.Fatal("legacy/current major mismatch")
			}
			if tc.name == "v1" && (len(w) != 2 || len(s.HooksDeclared) != 1 || len(s.EventsDeclared) != 1 || len(s.Surfaces) != 1) {
				t.Fatal(s, w)
			}
		})
	}
}

func TestNormalizedCatalogFocusProblemsAndExactCaseSettings(t *testing.T) {
	raw := `{
 // Every declaration stays plain config; no runtime registration here.
 "manifest_version":2,"protocol":2,"id":"Case.Owner","name":"Case","version":"1","command":["./plugin"],
 "actions":[{"id":"Case.Owner.new","title":"New","args_schema":{"type":"object"},"public":true}],
 "surfaces":[{"id":"sidebar-list","frame":{"border":true,"label":"List"}}],
 "keybindings":[
 {"key":"not-a-parsed-key","action":"Case.Owner.new","focus":"sidebar-list"},
 {"keys":["ctrl+k","ctrl+s"],"action":"husk.palette.open","focus":"sidebar-list","args":{"x":1}},
 {"key":"x","action":"other.new","focus":"sidebar-list"},
 {"key":"q","action":"Case.Owner.new"}],
 "events":{"subscribe":["state.changed","Case.Owner.*"],"publish":[{"name":"Case.Owner.changed","coalesce":false}]},
 "hooks":{"publish":[{"id":"Case.Owner.before"}],"subscribe":["other.before"]},
 "published_slots":[{"id":"Case.Owner.toolbar","kind":"region","rect":{"surface":"sidebar-list","x":0,"y":0,"width":2,"height":1}}],
 "state":{"scope":"user","persistent":true,"retention":{"policy":"keep-last-n","n":5},"max_bytes":1024},
 "secrets":[{"name":"git.token","purpose":"Push","required":true}],
 "settings":[{"key":"show_hidden","type":"bool","default":false},{"key":"nullable","type":"string","default":null}]
 }`
	s, w, err := convertBytes(t, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FocusBindings) != 2 || len(w) != 2 || len(s.BindingProblems) != 2 {
		t.Fatal("conversion ownership/scope policy", s, w)
	}
	if s.FocusBindings[0].Key != "not-a-parsed-key" {
		t.Fatal("Pods parsed a key owned by Services")
	}
	if s.BindingProblems[0].Index != 2 || s.BindingProblems[0].Code != "foreign_action" || s.BindingProblems[1].Code != "global_not_allowed" {
		t.Fatal(s.BindingProblems)
	}
	data, err := s.CatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "version", "description", "actions", "surfaces", "focus_bindings", "subscriptions", "events_declared", "hooks_declared", "hook_registrations", "published_slots", "slice", "secret_requests", "settings_declared"} {
		if catalog[key] == nil {
			t.Fatal("catalog missing", key)
		}
	}
	for _, key := range []string{"protocol", "command", "work_dir", "binding_problems"} {
		if catalog[key] != nil {
			t.Fatal("process metadata leaked", key)
		}
	}
	if !bytes.Contains(data, []byte(`"plugins.Case.Owner.show_hidden"`)) || !bytes.Contains(data, []byte(`"default":false`)) || !bytes.Contains(data, []byte(`"default":null`)) || !bytes.Contains(data, []byte(`"required":true`)) {
		t.Fatal("schema normalization lost presence/case")
	}
	focus, err := s.FocusKeymapJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc FocusKeymap
	if err := json.Unmarshal(focus, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || len(doc.Bind) != 2 || len(doc.Problems) != 2 {
		t.Fatal(doc)
	}
	clone := s.Clone()
	clone.Command[0] = "mutated"
	clone.Actions[0].ArgsSchema[0] = 'X'
	clone.Surfaces[0].Frame.Label = "mutated"
	clone.FocusBindings[1].Keys[0] = "mutated"
	*clone.EventsDeclared[0].Coalesce = true
	clone.PublishedSlots[0].Rect.Width = 99
	clone.Slice.Retention.N = 19
	clone.SettingsDeclared[0].Default[0] = 'X'
	if s.Command[0] == "mutated" || s.Actions[0].ArgsSchema[0] == 'X' || s.Surfaces[0].Frame.Label == "mutated" || s.FocusBindings[1].Keys[0] == "mutated" || *s.EventsDeclared[0].Coalesce || s.PublishedSlots[0].Rect.Width == 99 || s.Slice.Retention.N == 19 || s.SettingsDeclared[0].Default[0] == 'X' {
		t.Fatal("declaration clone aliased mutable data")
	}
}

func TestRegistryDirectoryIsolationImplicitFormsAndPureSnapshots(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	bad := filepath.Join(root, "bad")
	empty := filepath.Join(root, "empty")
	for _, dir := range []string{good, bad, empty} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw := `{"id":"p","name":"P","version":"1","command":["./plugin","--flag","relative/file"]}`
	if err := os.WriteFile(filepath.Join(good, FileName), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, FileName), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	results, err := r.LoadDir(root)
	if err != nil || len(results) != 2 {
		t.Fatal(results, err)
	}
	if results[0].Err == nil || results[1].Spec == nil {
		t.Fatal("one bad manifest failed its sibling")
	}
	s, _, err := r.Load(filepath.Join(good, FileName))
	if err != nil || s.Command[0] != filepath.Join(good, "plugin") || s.Command[2] != "relative/file" {
		t.Fatal(s, err)
	}
	src, ok := (V1Converter{}).Detect(good)
	if !ok {
		t.Fatal("not detected")
	}
	if err := os.Remove(filepath.Join(good, FileName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (V1Converter{}).Convert(src); err != nil {
		t.Fatal("Convert reread the filesystem instead of its snapshot", err)
	}
	exe := filepath.Join(root, "plugin")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if s, _, err := r.Load(exe); err != nil || s.ID != "plugin" || s.Command[0] != exe {
		t.Fatal(s, err)
	}
	if s, _, err := r.Load(`interpreter "two words" ''`); err != nil || len(s.Command) != 3 || s.Command[1] != "two words" || s.Command[2] != "" {
		t.Fatal(s, err)
	}
	if _, _, err := r.Load(`interpreter "unterminated`); err == nil {
		t.Fatal("broken command accepted")
	}
	if _, _, err := r.Load(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing bare path accepted")
	}
	outside := filepath.Join(root, "outside.json")
	if err := os.WriteFile(outside, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(good, FileName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Load(good); err == nil {
		t.Fatal("manifest symlink escaped source directory")
	}
	if _, _, err := convertBytes(t, strings.Repeat(" ", maxManifestBytes+1), true); err == nil {
		t.Fatal("manifest cap ignored")
	}
}

type claimingConverter struct {
	calls *int
	id    string
}

func (c claimingConverter) Format() string                   { return c.id }
func (c claimingConverter) Detect(dir string) (Source, bool) { return Source{Dir: dir}, true }
func (c claimingConverter) Convert(Source) (*Spec, []Warning, error) {
	*c.calls++
	return &Spec{ID: c.id}, nil, nil
}
func TestFirstConverterClaimWinsWithoutFallback(t *testing.T) {
	a, b := 0, 0
	r := NewRegistry(claimingConverter{&a, "first"}, claimingConverter{&b, "second"})
	s, _, err := r.Load(t.TempDir())
	if err != nil || s.ID != "first" || a != 1 || b != 0 {
		t.Fatal(s, a, b, err)
	}
}
