package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// focusKeys loads focus-local bindings declared by plugins "a" and "b".
func focusKeys(t *testing.T, aDoc, bDoc string) *keybinding.Service {
	t.Helper()
	kb := keybinding.New()
	in := catalog("a")
	in.Declared[store.DeclaredPlugins]["b"] = catalog("b").Declared[store.DeclaredPlugins]["b"]
	in.Declared[store.DeclaredFocusKeymap] = map[string]store.Doc{"a": doc(aDoc), "b": doc(bDoc)}
	apply(t, kb, in)
	return kb
}

// A plugin's focus-local binding fires only while its own surface is focused
// by that plugin; another surface or another owner does not see it.
func TestFocusLocalBindingFiresOnlyForTheFocusedOwner(t *testing.T) {
	kb := focusKeys(t,
		`{"version":1,"bind":[{"key":"n","focus":"stage","action":"a.run"}]}`,
		`{"version":1,"bind":[{"key":"n","focus":"stage","action":"b.run"}]}`)
	for _, tc := range []struct {
		name string
		ctx  keybinding.KeyContext
		want string
	}{
		{"owner a on stage", keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a"}, "a.run"},
		{"owner b on stage", keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "b"}, "b.run"},
		{"owner a elsewhere", keybinding.KeyContext{FocusSurface: "sidebar-list", FocusOwner: "a"}, ""},
		{"no owner", keybinding.KeyContext{FocusSurface: "stage"}, ""},
	} {
		r := kb.Resolve(tc.ctx, []string{"n"})
		switch {
		case tc.want == "" && r.Outcome != keybinding.NoMatch:
			t.Errorf("%s: %+v, want no match", tc.name, r)
		case tc.want != "" && (r.Outcome != keybinding.Match || r.Binding.Action != tc.want):
			t.Errorf("%s: %+v, want %s", tc.name, r, tc.want)
		}
	}
}

// A focus-local binding on a key a host command also binds wins while its
// surface is focused and falls through to the host command otherwise.
func TestFocusLocalBindingBeatsHostKeyOnlyWhenFocused(t *testing.T) {
	kb := focusKeys(t,
		`{"version":1,"bind":[{"key":"[","focus":"stage","action":"a.run"}]}`,
		`{"version":1,"bind":[]}`)
	if r := kb.Resolve(keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a"}, []string{"["}); r.Outcome != keybinding.Match || r.Binding.Action != "a.run" {
		t.Errorf("focused: %+v", r)
	}
	if r := kb.Resolve(keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "b"}, []string{"["}); r.Outcome != keybinding.Match || r.Binding.Action != "husk.sidebar.toggle" {
		t.Errorf("other owner focused: %+v", r)
	}
	if r := kb.Resolve(keybinding.KeyContext{FocusSurface: "sidebar-list", FocusOwner: "a"}, []string{"["}); r.Outcome != keybinding.Match || r.Binding.Action != "husk.sidebar.toggle" {
		t.Errorf("other surface: %+v", r)
	}
}

// Footer hints list the focused owner's hinted bindings only; a binding
// without a hint is not advertised.
func TestHintsFollowTheFocusedOwner(t *testing.T) {
	kb := focusKeys(t,
		`{"version":1,"bind":[{"key":"n","focus":"stage","action":"a.run","hint":"Run"},{"key":"m","focus":"stage","action":"a.run"}]}`,
		`{"version":1,"bind":[{"key":"n","focus":"stage","action":"b.run","hint":"Other"}]}`)
	hints := kb.Hints(keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a"})
	if len(hints) != 1 || hints[0].Action != "a.run" || hints[0].Text != "Run" {
		t.Errorf("a's hints = %+v", hints)
	}
	if got := kb.Hints(keybinding.KeyContext{FocusSurface: "sidebar-list", FocusOwner: "a"}); len(got) != 0 {
		t.Errorf("hints off-surface = %+v", got)
	}
	if got := kb.Hints(keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "b"}); len(got) != 1 || got[0].Action != "b.run" {
		t.Errorf("b's hints = %+v", got)
	}
}
