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
// parse_error.
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

// The last good profile document is kept per profile name: a broken file of
// profile "home" never falls back to the document of profile "work".
func TestKeybindingParseErrorFallbackIsPerProfile(t *testing.T) {
	kb := keybinding.New()
	load := func(name string, layers []store.LayerDoc, problems []store.Problem) {
		in := input(`{}`)
		in.Profile = &name
		in.Layers, in.KeymapProblems = layers, problems
		apply(t, kb, in)
	}
	brokenProfile := []store.Problem{{Code: "invalid_document", Layer: store.LayerProfile, Message: "bad"}}
	load("work", []store.LayerDoc{{Layer: store.LayerProfile, Doc: doc(bind(`{"key":"ctrl+9","action":"p.work"}`))}}, nil)
	load("home", nil, brokenProfile)
	if got := resolved(kb, keybinding.KeyContext{}, "ctrl+9"); got != "<none>" || len(problemsWith(kb, "parse_error")) != 1 {
		t.Fatalf("home borrowed work's document: %s, %+v", got, kb.Listing().Problems)
	}
	load("work", nil, brokenProfile)
	if got := resolved(kb, keybinding.KeyContext{}, "ctrl+9"); got != "p.work" {
		t.Errorf("work after break = %s, want p.work", got)
	}
}

// A distribution part that fails to parse while the other loads does not
// serve the partial fold: the last good distribution document stays.
func TestKeybindingParseErrorInOneDistributionPartKeepsLastGood(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerDistribution, Doc: doc(`{"version":1,"builtin":false,"bind":[{"key":"ctrl+9","action":"d.bundle"}]}`)}}
	apply(t, kb, in)
	partial := input(`{}`)
	partial.Layers = []store.LayerDoc{{Layer: store.LayerDistribution, Doc: doc(`{"version":1,"bind":[{"key":"ctrl+8","action":"d.embedded"}]}`)}}
	partial.KeymapProblems = []store.Problem{{Code: "invalid_document", Layer: store.LayerDistribution, File: "keymap.json", Message: "bad"}}
	apply(t, kb, partial)
	if got := resolved(kb, keybinding.KeyContext{}, "ctrl+9"); got != "d.bundle" {
		t.Errorf("ctrl+9 = %s, want the last good distribution binding", got)
	}
	if got := resolved(kb, keybinding.KeyContext{}, "ctrl+8"); got != "<none>" {
		t.Errorf("ctrl+8 = %s, the partial fold was served", got)
	}
	if len(kb.KeysFor("husk.quit")) != 0 {
		t.Error("the last good builtin:false was lost")
	}
	if len(problemsWith(kb, "parse_error")) != 1 {
		t.Errorf("problems = %+v", kb.Listing().Problems)
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

// Past the cap the plugin entry goes first, then the built-in entries; the
// configuration layers stay whole.
func TestKeybindingCapDropsPluginThenHusk(t *testing.T) {
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
	dropped, effective := map[string]int{}, map[keybinding.Layer]int{}
	for _, p := range problemsWith(kb, "too_many") {
		// Only the cap's problems carry a key; the layer files are at their limit.
		if p.Key != "" {
			dropped[p.Layer]++
		}
	}
	builtins := 0
	for _, e := range kb.Listing().Entries {
		if e.Source.Layer == keybinding.LayerHusk {
			builtins++
		}
		if e.State == "effective" {
			effective[e.Source.Layer]++
		}
	}
	total := 0
	for _, n := range effective {
		total += n
	}
	if dropped["plugin:plug"] != 1 || dropped["user"] != 0 || dropped["distribution"] != 0 {
		t.Errorf("dropped per layer = %v", dropped)
	}
	if want := builtins - effective[keybinding.LayerHusk]; dropped["husk"] != want || want == 0 {
		t.Errorf("dropped husk = %d, want %d of %d built-in entries", dropped["husk"], want, builtins)
	}
	if total != 4096 || effective[keybinding.LayerUser] != 2048 || effective[keybinding.LayerDistribution] != 2048 {
		t.Errorf("effective = %v (total %d), want user and distribution whole and 4096 in all", effective, total)
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
		`{"keys":["a",1],"action":"x.run"}`,
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
		`"keys"[1] must be a string, got a number`,
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

func (s *layerStore) LayersWithProblems(name store.DocName, _ string) ([]store.LayerDoc, []store.Problem, error) {
	if name != "keymap" {
		return nil, nil, nil
	}
	if s.broken {
		return nil, []store.Problem{{Code: "invalid_document", Layer: store.LayerUser, File: "/abs/keymap.json", Line: 2, Message: "hujson: line 2: bad"}}, nil
	}
	return []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(bind(`{"key":"ctrl+9","action":"u.run"}`))}}, nil, nil
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
