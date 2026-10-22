package state_test

// The distribution keymap fragment for the three host dock keys, validated
// against the keymap file shape in docs/docs/design/keybindings.md
// ("Keymap file shape"). The fragment is standalone JSON: state.md places no
// distribution documents and the Services keybinding package lives on another
// branch, so this test re-states the schema it must satisfy without importing
// that package. `cmd/husk/input.go` hard-codes these three keys today
// (`[`, `]`, `/`); this is the deliverable that externalizes them.

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
)

// keymapDoc mirrors the keymap file shape: version and builtin at the top,
// bind entries with key/keys, action, focus, plugin, args, hint, disable.
type keymapDoc struct {
	Version int           `json:"version"`
	Builtin *bool         `json:"builtin"`
	Bind    []keymapEntry `json:"bind"`
}

type keymapEntry struct {
	Key     *string        `json:"key"`
	Keys    []string       `json:"keys"`
	Action  string         `json:"action"`
	Focus   string         `json:"focus"`
	Plugin  string         `json:"plugin"`
	Args    map[string]any `json:"args"`
	Hint    string         `json:"hint"`
	Disable bool           `json:"disable"`
}

func loadHostKeymapFragment(t *testing.T) keymapDoc {
	t.Helper()
	raw, err := os.ReadFile("testdata/host-keys-keymap.json")
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	var doc keymapDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("fragment is not a valid keymap document: %v", err)
	}
	return doc
}

// TestHostKeymapFragmentSchema validates the fragment against every field
// rule keybindings.md gives: version 1, no builtin flag, exactly one of
// key/keys per entry, sequences of 2 to 4 keys, action unless disable,
// disable without action/args/hint, plugin only with focus, hint at most 40
// runes, primitive args, and no duplicate (key, focus, plugin) triples.
func TestHostKeymapFragmentSchema(t *testing.T) {
	doc := loadHostKeymapFragment(t)

	if doc.Version != 1 {
		t.Fatalf("version = %d, want 1", doc.Version)
	}
	if doc.Builtin != nil {
		t.Fatal("builtin is a distribution-layer field; the fragment must not set it")
	}
	if len(doc.Bind) == 0 {
		t.Fatal("bind is empty")
	}

	seen := map[[3]string]string{} // (key, focus, plugin) -> action
	for i, e := range doc.Bind {
		where := func(what string) string {
			return "bind[" + strconv.Itoa(i) + "]: " + what
		}
		hasKey, hasKeys := e.Key != nil, len(e.Keys) > 0
		if hasKey == hasKeys {
			t.Fatal(where("exactly one of key/keys must be present"))
		}
		keys := []string{}
		if hasKey {
			keys = append(keys, *e.Key)
		} else {
			if len(e.Keys) < 2 || len(e.Keys) > 4 {
				t.Fatal(where("keys sequence must hold 2 to 4 keys"))
			}
			keys = e.Keys
		}
		for _, k := range keys {
			if k == "" {
				t.Fatal(where("empty key spelling"))
			}
			if len([]rune(k)) > 1 && containsSpace(k) {
				t.Fatal(where("key " + k + " carries whitespace; sequences use the keys field"))
			}
		}
		if e.Disable {
			if e.Action != "" || e.Args != nil || e.Hint != "" {
				t.Fatal(where("disable forbids action, args and hint"))
			}
		} else if e.Action == "" {
			t.Fatal(where("action is required unless disable"))
		}
		if e.Plugin != "" && e.Focus == "" {
			t.Fatal(where("plugin is invalid without focus"))
		}
		if len([]rune(e.Hint)) > 40 {
			t.Fatal(where("hint exceeds 40 runes"))
		}
		for ak, av := range e.Args {
			switch av.(type) {
			case string, float64, bool:
			default:
				t.Fatal(where("args." + ak + " is not a string, number or boolean"))
			}
		}
		for _, k := range keys {
			triple := [3]string{k, e.Focus, e.Plugin}
			if prev, dup := seen[triple]; dup {
				t.Fatal(where("duplicate key " + k + " (first bound to " + prev + ")"))
			}
			seen[triple] = e.Action
		}
	}
}

// TestHostKeymapFragmentCarriesTheHostDockKeys pins the three bindings the
// brief externalizes: the exact keys and actions cmd/husk/input.go
// hard-codes today, verbatim the entries of the built-in husk keymap block in
// keybindings.md, all global scope, none disabled, so resolution sends
// `[`/`]`/`/` to the dock toggles unless a stronger layer overrides or
// disables them.
func TestHostKeymapFragmentCarriesTheHostDockKeys(t *testing.T) {
	doc := loadHostKeymapFragment(t)

	global := map[string]string{}
	for _, e := range doc.Bind {
		if e.Disable || e.Focus != "" || e.Plugin != "" {
			continue
		}
		if e.Key != nil {
			global[*e.Key] = e.Action
		}
	}
	want := map[string]string{
		"[": "husk.sidebar.toggle",
		"]": "husk.sidepanel.toggle",
		"/": "husk.docks.cycle",
	}
	for key, action := range want {
		if got := global[key]; got != action {
			t.Fatalf("key %q resolves to %q, want %q", key, got, action)
		}
	}
	if len(global) != len(want) {
		t.Fatalf("fragment binds %d global keys (%v), want exactly the three host dock keys", len(global), global)
	}

	// Byte-level parity with the spec's built-in husk keymap entries: same
	// field set, nothing extra.
	specEntries := []keymapEntry{
		{Key: strPtr("["), Action: "husk.sidebar.toggle"},
		{Key: strPtr("]"), Action: "husk.sidepanel.toggle"},
		{Key: strPtr("/"), Action: "husk.docks.cycle"},
	}
	if len(doc.Bind) != len(specEntries) {
		t.Fatalf("bind has %d entries, want %d", len(doc.Bind), len(specEntries))
	}
	for i, spec := range specEntries {
		if !reflect.DeepEqual(doc.Bind[i], spec) {
			t.Fatalf("bind[%d] = %+v, want %+v", i, doc.Bind[i], spec)
		}
	}
}

func strPtr(s string) *string { return &s }

func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' {
			return true
		}
	}
	return false
}
