package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func problemsWith(kb *keybinding.Service, code string) []keybinding.Problem {
	var out []keybinding.Problem
	for _, p := range kb.Listing().Problems {
		if p.Code == code {
			out = append(out, p)
		}
	}
	return out
}

// A layer file that stops parsing keeps its last good document and reports
// parse_error; the profile layer is cached per profile name.
func TestKeybindingParseErrorKeepsLastGoodDocument(t *testing.T) {
	for _, layer := range []store.Layer{store.LayerUser, store.LayerProfile, store.LayerDistribution} {
		t.Run(string(layer), func(t *testing.T) {
			profile := "work"
			kb := keybinding.New()
			in := input(`{}`)
			in.Profile = &profile
			in.Layers = []store.LayerDoc{{Layer: layer, Doc: doc(bind(`{"key":"ctrl+9","action":"u.run"}`))}}
			apply(t, kb, in)
			if got := resolved(kb, keybinding.KeyContext{}, "ctrl+9"); got != "u.run" {
				t.Fatalf("before = %s", got)
			}
			// The Store left the broken file out and reports it.
			broken := input(`{}`)
			broken.Profile = &profile
			broken.KeymapProblems = []store.Problem{{Code: "invalid_document", Layer: layer, File: "keymap.json", Line: 3, Message: "hujson: line 3, column 5: invalid character"}}
			apply(t, kb, broken)
			if got := resolved(kb, keybinding.KeyContext{}, "ctrl+9"); got != "u.run" {
				t.Errorf("after parse error = %s, want the last good binding", got)
			}
			ps := problemsWith(kb, "parse_error")
			if len(ps) != 1 || ps[0].Layer != string(layer) || ps[0].Source == nil || ps[0].Source.Line != 3 || !strings.Contains(ps[0].Message, "line 3") {
				t.Errorf("parse_error = %+v", ps)
			}
		})
	}
}

// At first load a broken layer is simply absent.
func TestKeybindingParseErrorAtFirstLoadLeavesLayerAbsent(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.KeymapProblems = []store.Problem{{Code: "invalid_document", Layer: store.LayerUser, Message: "bad"}}
	apply(t, kb, in)
	if len(problemsWith(kb, "parse_error")) != 1 || resolved(kb, keybinding.KeyContext{}, "ctrl+9") != "<none>" {
		t.Fatal(kb.Listing().Problems)
	}
}

// Fixing the file ends the parse_error and replaces the stale document.
func TestKeybindingParseErrorClearsWhenFileIsFixed(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(bind(`{"key":"ctrl+9","action":"u.one"}`))}}
	apply(t, kb, in)
	broken := input(`{}`)
	broken.KeymapProblems = []store.Problem{{Code: "invalid_document", Layer: store.LayerUser, Message: "bad"}}
	apply(t, kb, broken)
	fixed := input(`{}`)
	fixed.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(bind(`{"key":"ctrl+9","action":"u.two"}`))}}
	apply(t, kb, fixed)
	if got := resolved(kb, keybinding.KeyContext{}, "ctrl+9"); got != "u.two" || len(problemsWith(kb, "parse_error")) != 0 {
		t.Fatalf("got %s, %+v", got, kb.Listing().Problems)
	}
}

// A flood of plugin bindings past the cap drops only plugin entries (D216).
func TestKeybindingCapFloodKeepsConfigurationAndBuiltins(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{
		{Layer: store.LayerUser, Doc: doc(bind(`{"key":"ctrl+9","action":"u.run"}`))},
		{Layer: store.LayerProfile, Doc: doc(bind(`{"key":"ctrl+8","action":"p.run"}`))},
	}
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{}
	for p := 0; p < 33; p++ {
		var entries []string
		for i := 0; i < 128; i++ {
			entries = append(entries, fmt.Sprintf(`{"key":%q,"focus":"stage","action":"plug.run"}`, string(rune(0x4e00+i))))
		}
		in.Declared[store.DeclaredKeymap][fmt.Sprintf("plug%02d", p)] = doc(bind(entries...))
	}
	apply(t, kb, in)
	if keys := kb.KeysFor("husk.quit"); len(keys) == 0 {
		t.Fatal("husk.quit lost to a plugin flood")
	}
	if resolved(kb, keybinding.KeyContext{}, "ctrl+9") != "u.run" || resolved(kb, keybinding.KeyContext{}, "ctrl+8") != "p.run" {
		t.Error("a configuration binding was dropped")
	}
	effective := 0
	for _, e := range kb.Listing().Entries {
		if e.State == "effective" {
			effective++
		}
	}
	if effective != 4096 {
		t.Errorf("effective = %d, want the cap", effective)
	}
	ps := problemsWith(kb, "too_many")
	if len(ps) == 0 {
		t.Fatal("no too_many")
	}
	for _, p := range ps {
		if p.Source == nil || p.Source.Layer != keybinding.LayerFocusLocal {
			t.Fatalf("dropped entry from %+v, want only the lowest layer", p.Source)
		}
	}
}

// The cap drops the lowest layer present first, in the layer order of D216.
func TestKeybindingCapLayerOrderWithoutPlugins(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	for i, layer := range []store.Layer{store.LayerDistribution, store.LayerUser} {
		var entries []string
		for n := 0; n < 2048; n++ {
			entries = append(entries, fmt.Sprintf(`{"keys":["ctrl+k",%q],"action":"x.run"}`, string(rune(0x4e00+i*2048+n))))
		}
		in.Layers = append(in.Layers, store.LayerDoc{Layer: layer, Doc: doc(bind(entries...))})
	}
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{"plug": doc(bind(`{"key":"a","focus":"stage","action":"plug.run"}`))}
	apply(t, kb, in)
	dropped := map[string]int{}
	for _, p := range problemsWith(kb, "too_many") {
		dropped[p.Layer]++
	}
	// 2048+2048 plus husk and plugin entries exceed the cap: plugin first, then husk.
	if dropped["plugin:plug"] != 1 || dropped["husk"] == 0 || dropped["user"] != 0 || dropped["distribution"] != 0 {
		t.Errorf("dropped per layer = %v", dropped)
	}
}

// Every loader problem says what was wrong, not only its code.
func TestKeybindingProblemMessagesNameTheCause(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(bind(
		`{"key":"ctrl+1","action":42}`,
		`{"key":"ctrl+2","keys":["a","b"],"action":"x.run"}`,
		`{"keys":["a"],"action":"x.run"}`,
		`{"key":"ctrl+3","action":"nodot"}`,
		`{"key":"ctrl+4","plugin":"p","action":"x.run"}`,
		`{"key":"ctrl+5","action":"x.run","hint":"this hint is much longer than forty runes allow"}`,
		`{"key":"ctrl+6","action":"x.run","args":{"a":[1]}}`,
		`{"key":"ctrl+7","disable":true,"action":"x.run"}`,
		`{"key":"ctrl+0","action":"x.run","disable":"yes"}`,
	))}}
	apply(t, kb, in)
	want := []string{
		`"action" must be a string, got a number`,
		`exactly one of "key" or "keys" is required, got both`,
		`"keys" must hold 2 to 4 keys, got 1`,
		`"action" must contain a "."`,
		`"plugin" requires "focus"`,
		`"hint" must be at most 40 runes`,
		`"args" value "a" must be a string, number or boolean, got an array`,
		`"action" must be absent when "disable" is true`,
		`"disable" must be a boolean, got a string`,
	}
	ps := problemsWith(kb, "invalid_entry")
	if len(ps) != len(want) {
		t.Fatalf("problems = %+v", ps)
	}
	for i, w := range want {
		if !strings.Contains(ps[i].Message, w) || ps[i].Message == "invalid_entry" {
			t.Errorf("problem %d message = %q, want it to contain %q", i, ps[i].Message, w)
		}
	}
}

// A document-level problem names its cause as well.
func TestKeybindingFileProblemMessages(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{
		{Layer: store.LayerUser, Doc: doc(`{"version":2,"bind":[]}`)},
		{Layer: store.LayerProfile, Doc: doc(`{"version":1}`)},
	}
	apply(t, kb, in)
	for _, code := range []string{"unsupported_version", "invalid_entry"} {
		ps := problemsWith(kb, code)
		if len(ps) != 1 || ps[0].Message == code || !strings.Contains(ps[0].Message, `"`) {
			t.Errorf("%s = %+v", code, ps)
		}
	}
}

// A builtin field of the wrong type is reported and does not remove the
// built-in layer; only the boolean false does.
func TestKeybindingBuiltinWrongTypeIsInvalidEntry(t *testing.T) {
	for _, value := range []string{`"false"`, `0`, `null`, `[]`} {
		kb := keybinding.New()
		in := input(`{}`)
		in.Layers = []store.LayerDoc{{Layer: store.LayerDistribution, Doc: doc(`{"version":1,"builtin":` + value + `,"bind":[]}`)}}
		apply(t, kb, in)
		ps := problemsWith(kb, "invalid_entry")
		if len(ps) != 1 || ps[0].Key != "builtin" || !strings.Contains(ps[0].Message, "boolean") {
			t.Errorf("builtin %s: problems = %+v", value, kb.Listing().Problems)
		}
		if len(kb.KeysFor("husk.quit")) == 0 {
			t.Errorf("builtin %s removed the built-in layer", value)
		}
	}
}

// layerStore serves a keymap user layer until broken is set, then leaves it out
// and reports it the way Store does.
type layerStore struct {
	fakeStore
	broken bool
}

func (s *layerStore) Layers(name store.DocName, _ string) ([]store.LayerDoc, error) {
	if name != "keymap" || s.broken {
		return nil, nil
	}
	return []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(bind(`{"key":"ctrl+9","action":"u.run"}`))}}, nil
}
func (s *layerStore) LayerProblems(name store.DocName, _ string) ([]store.Problem, error) {
	if name != "keymap" || !s.broken {
		return nil, nil
	}
	return []store.Problem{{Code: "invalid_document", Layer: store.LayerUser, File: "/abs/keymap.json", Line: 2, Message: "hujson: line 2: bad"}}, nil
}

// Through the coordinator: the Store's parse problem reaches the keymap reload,
// which keeps the user bindings and reports parse_error.
func TestCoordinatorCarriesStoreParseProblemsToKeybinding(t *testing.T) {
	services := fakeServices()
	kb := keybinding.New()
	services["keybinding"] = kb
	source := &layerStore{}
	co, err := NewCoordinator(Options{Store: source, Services: services, Clock: clock()})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	if _, err := co.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	if resolved(kb, keybinding.KeyContext{}, "ctrl+9") != "u.run" {
		t.Fatal("user binding missing before the break")
	}
	source.broken = true
	if _, err := co.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	ps := problemsWith(kb, "parse_error")
	if resolved(kb, keybinding.KeyContext{}, "ctrl+9") != "u.run" || len(ps) != 1 || ps[0].Source.File != "keymap.json" {
		t.Errorf("after break: %+v", kb.Listing().Problems)
	}
}
