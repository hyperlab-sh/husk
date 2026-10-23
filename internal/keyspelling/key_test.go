package keyspelling

import (
	tea "charm.land/bubbletea/v2"
	"testing"
)

func TestNormalizeStableWireSpelling(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"space", " "}, {" ", " "}, {"ctrl+space", "ctrl+@"}, {"ctrl+@", "ctrl+@"}, {"ctrl+alt+x", "alt+ctrl+x"}, {"alt+shift+x", "alt+X"}, {"é", "é"}, {"alt+!", "alt+!"}, {"ctrl++", "ctrl++"}, {"f12", "f12"}, {"esc", "esc"}, {"shift+tab", "shift+tab"}} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := Normalize(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("%q => %q %v, want %q", tc.in, got, err, tc.want)
			}
		})
	}
}
func TestRejectInvalidOrJoinedKeys(t *testing.T) {
	for _, key := range []string{"", "ctrl+", "ctrl+ctrl+x", "ctrl+k ctrl+s", "ctrl+unknown", "\n", "space x"} {
		if _, err := Normalize(key); err == nil {
			t.Fatal("accepted", key)
		}
	}
}
func TestFormatAlternateTextAndLocks(t *testing.T) {
	for _, tc := range []struct {
		key  tea.Key
		want string
	}{{tea.Key{Code: 'x', Mod: tea.ModAlt | tea.ModShift, ShiftedCode: 'X'}, "alt+X"}, {tea.Key{Code: 'a', Mod: tea.ModCtrl | tea.ModNumLock | tea.ModScrollLock, Text: "ф"}, "ctrl+a"}, {tea.Key{Code: 'a', Mod: tea.ModCapsLock}, "A"}} {
		if got := Format(tc.key); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestIsTextKey(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"enter", true}, {"backspace", true}, {"delete", true}, {"tab", true}, {"up", true}, {"down", true},
		{"left", true}, {"right", true}, {"home", true}, {"end", true}, {"pgup", true}, {"pgdown", true}, {"insert", true},
		{" ", true}, {"a", true}, {"A", true}, {"q", true}, {":", true}, {"ö", true}, {"日", true},
		{"esc", false}, {"f1", false}, {"f63", false}, {"ctrl+a", false}, {"alt+ö", false}, {"alt+1", false},
		{"shift+tab", false}, {"ctrl+@", false}, {"", false}, {"[pasted text]", false}, {"space", false}, {"ab", false},
	} {
		if got := IsTextKey(tc.in); got != tc.want {
			t.Errorf("IsTextKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
