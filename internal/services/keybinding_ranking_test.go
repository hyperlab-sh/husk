package services

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// rankKeys loads layer documents (and optional focus-local documents of the
// plugin "a", whose only surface is "stage") on top of the built-in layer.
func rankKeys(t *testing.T, layers map[store.Layer]string, focusLocal string) *keybinding.Service {
	t.Helper()
	kb := keybinding.New()
	in := catalog("a")
	if focusLocal != "" {
		in.Declared[store.DeclaredKeymap] = map[string]store.Doc{"a": doc(focusLocal)}
	}
	for _, layer := range []store.Layer{store.LayerDistribution, store.LayerUser, store.LayerProfile} {
		if raw, ok := layers[layer]; ok {
			in.Layers = append(in.Layers, store.LayerDoc{Layer: layer, Doc: doc(raw)})
		}
	}
	apply(t, kb, in)
	return kb
}

func resolved(kb *keybinding.Service, ctx keybinding.KeyContext, keys ...string) string {
	r := kb.Resolve(ctx, keys)
	switch r.Outcome {
	case keybinding.Match:
		return r.Binding.Action
	case keybinding.Prefix:
		return "<prefix>"
	}
	return "<none>"
}

func bind(entries ...string) string {
	return `{"version":1,"bind":[` + strings.Join(entries, ",") + `]}`
}

// Scope decides first, then layer, then the plugin condition, then file order.
func TestRankingScopeLayerConditionOrder(t *testing.T) {
	stage := keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a"}
	for _, tc := range []struct {
		name   string
		layers map[store.Layer]string
		local  string
		ctx    keybinding.KeyContext
		want   string
	}{
		{"surface entry in a lower layer beats a global one in a higher layer",
			map[store.Layer]string{store.LayerDistribution: bind(`{"key":"r","focus":"stage","action":"d.surface"}`), store.LayerProfile: bind(`{"key":"r","action":"p.global"}`)}, "", stage, "d.surface"},
		{"the global entry still wins where the surface is not focused",
			map[store.Layer]string{store.LayerDistribution: bind(`{"key":"r","focus":"stage","action":"d.surface"}`), store.LayerProfile: bind(`{"key":"r","action":"p.global"}`)}, "", keybinding.KeyContext{FocusSurface: "other"}, "p.global"},
		{"focus-local beats a user global",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.global"}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), stage, "a.run"},
		{"user surface entry beats the focus-local one",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","action":"u.surface"}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), stage, "u.surface"},
		{"within a scope the stronger layer wins",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.global"}`), store.LayerProfile: bind(`{"key":"r","action":"p.global"}`), store.LayerDistribution: bind(`{"key":"r","action":"d.global"}`)}, "", stage, "p.global"},
		{"plugin set beats unset in one scope and layer, whatever the order",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","action":"u.any"}`, `{"key":"r","focus":"stage","plugin":"a","action":"u.plugin"}`)}, "", stage, "u.plugin"},
		{"plugin unset applies when another plugin occupies the surface",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","action":"u.any"}`, `{"key":"r","focus":"stage","plugin":"a","action":"u.plugin"}`)}, "", keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "b"}, "u.any"},
		{"a global tombstone does not silence a surface-scoped entry",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","disable":true}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), stage, "a.run"},
		{"a surface-scoped tombstone unbinds the focus-local entry",
			map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","disable":true}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), stage, "<none>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolved(rankKeys(t, tc.layers, tc.local), tc.ctx, "r"); got != tc.want {
				t.Fatalf("Resolve = %s, want %s", got, tc.want)
			}
		})
	}
}

// File order breaks a tie only between entries the other steps cannot separate:
// two entries with the same sequence are a duplicate and the first stays.
func TestRankingFileOrderBreaksTies(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.first"}`, `{"key":"r","action":"u.second"}`)}, "")
	if got := resolved(kb, keybinding.KeyContext{}, "r"); got != "u.first" {
		t.Fatalf("Resolve = %s, want u.first", got)
	}
	// Same layer and scope: KeysFor keeps file order.
	kb = rankKeys(t, map[store.Layer]string{store.LayerUser: bind(`{"key":"b","action":"u.act"}`, `{"key":"a","action":"u.act"}`)}, "")
	if got := fmt.Sprint(kb.KeysFor("u.act")); got != "[b a]" {
		t.Fatalf("KeysFor = %s, want [b a]", got)
	}
}

// Static shadowing pairs and non-pairs.
func TestStaticShadowing(t *testing.T) {
	type want struct{ state, by string }
	for _, tc := range []struct {
		name   string
		layers map[store.Layer]string
		local  string
		// index is "layer#order" of the entry under test.
		entry string
		want  want
	}{
		{"higher layer, same scope", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.g"}`), store.LayerProfile: bind(`{"key":"r","action":"p.g"}`)}, "", "user#0", want{"shadowed", "profile#0"}},
		{"global never shadows surface-scoped", map[store.Layer]string{store.LayerProfile: bind(`{"key":"r","action":"p.g"}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), "focus-local#0", want{"effective", ""}},
		{"surface-scoped never shadows global", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.g"}`), store.LayerProfile: bind(`{"key":"r","focus":"stage","action":"p.s"}`)}, "", "user#0", want{"effective", ""}},
		{"surface entry shadows a lower-layer entry on the same surface", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","action":"u.s"}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), "focus-local#0", want{"shadowed", "user#0"}},
		{"unconditioned entry shadows a conditioned lower one", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","plugin":"a","action":"u.s"}`), store.LayerProfile: bind(`{"key":"r","focus":"stage","action":"p.s"}`)}, "", "user#0", want{"shadowed", "profile#0"}},
		{"conditioned entry does not shadow an unconditioned lower one", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","action":"u.s"}`), store.LayerProfile: bind(`{"key":"r","focus":"stage","plugin":"a","action":"p.s"}`)}, "", "user#0", want{"effective", ""}},
		{"plugin condition protects the lower entry from a different plugin's entry", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"stage","plugin":"a","action":"u.s"}`), store.LayerProfile: bind(`{"key":"r","focus":"stage","plugin":"b","action":"p.s"}`)}, "", "user#0", want{"effective", ""}},
		{"same layer, scope and condition: the first stays effective", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.x"}`, `{"key":"r","action":"u.y"}`)}, "", "user#0", want{"effective", ""}},
		{"same layer, scope and condition: the later one is a duplicate shadowed by the first", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","action":"u.x"}`, `{"key":"r","action":"u.y"}`)}, "", "user#1", want{"shadowed", "user#0"}},
		{"shorter higher entry shadows the longer one", map[store.Layer]string{store.LayerUser: bind(`{"keys":["g","x"],"action":"u.long"}`), store.LayerProfile: bind(`{"key":"g","action":"p.short"}`)}, "", "user#0", want{"shadowed", "profile#0"}},
		{"longer higher entry shadows the shorter one", map[store.Layer]string{store.LayerUser: bind(`{"key":"g","action":"u.short"}`), store.LayerProfile: bind(`{"keys":["g","x"],"action":"p.long"}`)}, "", "user#0", want{"shadowed", "profile#0"}},
		{"shorter tombstone shadows the longer sequence under it", map[store.Layer]string{store.LayerDistribution: bind(`{"keys":["g","x"],"action":"d.long"}`), store.LayerUser: bind(`{"key":"g","disable":true}`)}, "", "distribution#0", want{"shadowed", "user#0"}},
		{"longer tombstone does not shadow the shorter sequence", map[store.Layer]string{store.LayerDistribution: bind(`{"key":"g","action":"d.short"}`), store.LayerUser: bind(`{"keys":["g","x"],"disable":true}`)}, "", "distribution#0", want{"effective", ""}},
		{"tombstone that nothing outranks is disabled", map[store.Layer]string{store.LayerUser: bind(`{"key":"q","disable":true}`)}, "", "user#0", want{"disabled", ""}},
		{"global tombstone does not shadow a surface-scoped entry", map[store.Layer]string{store.LayerUser: bind(`{"key":"r","disable":true}`)}, bind(`{"key":"r","focus":"stage","action":"a.run"}`), "focus-local#0", want{"effective", ""}},
		{"builtin entry shadowed by a user tombstone", map[store.Layer]string{store.LayerUser: bind(`{"key":"[","disable":true}`)}, "", "husk#" + fmt.Sprint(builtinIndex(t, "[")), want{"shadowed", "user#0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kb := rankKeys(t, tc.layers, tc.local)
			for _, e := range kb.Listing().Entries {
				if fmt.Sprintf("%s#%d", e.Source.Layer, e.Source.Order) != tc.entry {
					continue
				}
				by := ""
				if e.ShadowedBy != nil {
					by = fmt.Sprintf("%s#%d", e.ShadowedBy.Layer, e.ShadowedBy.Order)
				}
				if e.State != tc.want.state || by != tc.want.by {
					t.Fatalf("%s: state %s shadowed_by %q, want %s %q", tc.entry, e.State, by, tc.want.state, tc.want.by)
				}
				return
			}
			t.Fatalf("no entry %s in %+v", tc.entry, kb.Listing().Entries)
		})
	}
}

// builtinIndex is the position of the global entry for key in the built-in map.
func builtinIndex(t *testing.T, key string) int {
	t.Helper()
	for _, e := range keybinding.New().Listing().Entries {
		if e.Source.Layer == keybinding.LayerHusk && e.Focus == "" && len(e.Keys) == 1 && e.Keys[0] == key {
			return e.Source.Order
		}
	}
	t.Fatalf("builtin has no %q", key)
	return 0
}

// A tombstone unbinds only its exact sequence when resolving; in listings the
// shorter tombstone still shadows the longer sequences under it.
func TestTombstoneExactLeafVersusListingShadow(t *testing.T) {
	// Spec example: a longer user tombstone over a husk binding on the prefix.
	kb := rankKeys(t, map[store.Layer]string{store.LayerDistribution: bind(`{"key":"ctrl+k","action":"d.plain"}`), store.LayerUser: bind(`{"keys":["ctrl+k","ctrl+s"],"disable":true}`)}, "")
	ctx := keybinding.KeyContext{}
	if got := resolved(kb, ctx, "ctrl+k"); got != "d.plain" {
		t.Errorf("ctrl+k = %s, want d.plain: the longer tombstone is not a candidate", got)
	}
	if got := resolved(kb, ctx, "ctrl+k", "ctrl+s"); got != "<none>" {
		t.Errorf("ctrl+k ctrl+s = %s, want none", got)
	}
	// A prefix whose only continuation is a tombstone is no wait.
	kb = rankKeys(t, map[store.Layer]string{store.LayerUser: bind(`{"keys":["ctrl+k","ctrl+s"],"disable":true}`)}, "")
	if got := resolved(kb, ctx, "ctrl+k"); got != "<none>" {
		t.Errorf("only-tombstone prefix = %s, want none", got)
	}
	// The sibling continuation stays typable in either file order.
	for _, binds := range []string{
		bind(`{"keys":["ctrl+k","ctrl+s"],"disable":true}`, `{"keys":["ctrl+k","ctrl+o"],"action":"u.open"}`),
		bind(`{"keys":["ctrl+k","ctrl+o"],"action":"u.open"}`, `{"keys":["ctrl+k","ctrl+s"],"disable":true}`),
	} {
		kb = rankKeys(t, map[store.Layer]string{store.LayerUser: binds}, "")
		if resolved(kb, ctx, "ctrl+k") != "<prefix>" || resolved(kb, ctx, "ctrl+k", "ctrl+o") != "u.open" || resolved(kb, ctx, "ctrl+k", "ctrl+s") != "<none>" {
			t.Errorf("sibling continuation broken for %s", binds)
		}
	}
	// A shorter tombstone over a longer lower-layer binding: the key reaches the
	// occupant, the sequence can never be typed, and the listing says why.
	kb = rankKeys(t, map[store.Layer]string{store.LayerDistribution: bind(`{"keys":["ctrl+k","ctrl+s"],"action":"d.save"}`), store.LayerUser: bind(`{"key":"ctrl+k","disable":true}`)}, "")
	if got := resolved(kb, ctx, "ctrl+k"); got != "<none>" {
		t.Errorf("shorter tombstone = %s, want none", got)
	}
}

// KeysFor lists global spellings first, then surface-scoped; within each group
// the stronger layer first, then file order.
func TestKeysForOrder(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{
		store.LayerDistribution: bind(`{"key":"d","action":"a.run"}`, `{"key":"e","action":"a.run"}`, `{"key":"i","focus":"stage","action":"a.run"}`),
		store.LayerUser:         bind(`{"key":"c","action":"a.run"}`, `{"key":"b","action":"a.run"}`, `{"key":"g","focus":"stage","action":"a.run"}`),
		store.LayerProfile:      bind(`{"key":"a","action":"a.run"}`, `{"key":"f","focus":"stage","action":"a.run"}`),
	}, bind(`{"key":"j","focus":"stage","action":"a.run"}`))
	got := kb.KeysFor("a.run")
	want := []string{"a", "c", "b", "d", "e", "f", "g", "i", "j"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("KeysFor = %v, want %v", got, want)
	}
}

// A document over 256 KiB is too_large, not too_many; the last good document
// for the layer stays on reload.
func TestOversizedDocumentIsTooLarge(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(bind(`{"key":"g","action":"husk.home"}`))}}
	apply(t, kb, in)
	pad := strings.Repeat("x", 300*1024)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"pad":"` + pad + `","bind":[{"key":"z","action":"husk.home"}]}`)}}
	apply(t, kb, in)
	var codes []string
	for _, p := range kb.Listing().Problems {
		codes = append(codes, p.Code)
	}
	if fmt.Sprint(codes) != "[too_large]" {
		t.Fatalf("problems = %v, want [too_large]", codes)
	}
	if got := resolved(kb, keybinding.KeyContext{}, "g"); got != "husk.home" {
		t.Errorf("last good document lost: g = %s", got)
	}
	if got := resolved(kb, keybinding.KeyContext{}, "z"); got != "<none>" {
		t.Errorf("oversized document applied: z = %s", got)
	}
}

// A missing bind array rejects the file and names the key.
func TestMissingBindNamesTheKey(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1}`)}}
	apply(t, kb, in)
	for _, p := range kb.Listing().Problems {
		if p.Code == "invalid_entry" && p.Key == "bind" {
			return
		}
	}
	t.Fatalf("no invalid_entry for bind: %+v", kb.Listing().Problems)
}

// The service trusts conversion: a focus-local entry is loaded with its
// plugin taken from the document key, whatever else the document says; only
// key parsing remains.
func TestFocusLocalTrustsConversion(t *testing.T) {
	kb := rankKeys(t, nil, `{"version":1,"bind":[{"key":"ctrl+badkey","focus":"stage","action":"a.run"},{"key":"n","focus":"stage","action":"a.run"}]}`)
	var unparsable int
	for _, p := range kb.Listing().Problems {
		if p.Code == "unparsable_key" {
			unparsable++
		}
	}
	if unparsable != 1 {
		t.Fatalf("unparsable_key problems = %d, want 1", unparsable)
	}
	r := kb.Resolve(keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a"}, []string{"n"})
	if r.Outcome != keybinding.Match || r.Binding.Plugin != "a" || r.Binding.Source.Plugin != "a" {
		t.Fatalf("Resolve = %+v", r)
	}
}

// A same-layer shorter binding makes the longer one unreachable; the same
// prefix across layers or scopes is ranking, not a conflict.
func TestUnreachableIsPerLayerAndScope(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{
		store.LayerUser: bind(`{"key":"g","action":"u.short"}`, `{"keys":["g","x"],"action":"u.long"}`, `{"keys":["g","y"],"focus":"stage","action":"u.surface"}`),
	}, "")
	var unreachable []int
	for _, p := range kb.Listing().Problems {
		if p.Code == "unreachable" {
			unreachable = append(unreachable, p.Source.Order)
		}
	}
	if fmt.Sprint(unreachable) != "[1]" {
		t.Fatalf("unreachable entries = %v, want [1]", unreachable)
	}
}
