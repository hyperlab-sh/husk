package keyspelling

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// Covers the canonical host binding vocabulary, not arbitrary synthetic key
// structs: control chords use base lowercase codes; shifted symbols use their
// produced text. D38 intentionally rejects Shift+symbol control-chord names.
func TestNormalizeFormatRoundTrip(t *testing.T) {
	check := func(key tea.Key) {
		t.Helper()
		wire := Format(key)
		got, err := Normalize(wire)
		if err != nil || got != wire {
			t.Errorf("Normalize(Format(%+v)) = %q, %v; wire %q", key, got, err, wire)
		}
	}
	modifiers := []tea.KeyMod{0, tea.ModAlt, tea.ModShift, tea.ModAlt | tea.ModShift, tea.ModCtrl, tea.ModAlt | tea.ModCtrl, tea.ModCtrl | tea.ModShift, tea.ModAlt | tea.ModCtrl | tea.ModShift, tea.ModSuper, tea.ModAlt | tea.ModSuper, tea.ModSuper | tea.ModShift, tea.ModMeta, tea.ModHyper}
	locks := []tea.KeyMod{0, tea.ModNumLock, tea.ModScrollLock, tea.ModNumLock | tea.ModScrollLock, tea.ModCapsLock}
	printable := []rune("aAzZéÉöÖфФ你界 0123456789`-=[]\\;',./~!@#$%^&*()_+{}|:\"<>?")
	for _, code := range printable {
		for _, mods := range modifiers {
			for _, lock := range locks {
				command := mods&(tea.ModCtrl|tea.ModMeta|tea.ModSuper|tea.ModHyper) != 0
				if command && mods.Contains(tea.ModShift) && !unicode.IsLetter(code) && !unicode.IsSpace(code) {
					continue
				}
				base := code
				if command && unicode.IsLetter(base) {
					base = unicode.ToLower(base)
				}
				key := tea.Key{Code: base, Mod: mods | lock}
				if !command && mods.Contains(tea.ModShift) {
					key.ShiftedCode = unicode.ToUpper(base)
					const unshifted = "`1234567890-=[]\\;',./"
					const shifted = "~!@#$%^&*()_+{}|:\"<>?"
					if at := strings.IndexRune(unshifted, base); at >= 0 {
						key.ShiftedCode = rune(shifted[at])
					}
				}
				check(key)
			}
		}
	}
	named := []rune{tea.KeyEnter, tea.KeyTab, tea.KeyBackspace, tea.KeyEscape, tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown, tea.KeyInsert, tea.KeyDelete}
	for i := 0; i < 63; i++ {
		named = append(named, tea.KeyF1+rune(i))
	}
	for _, code := range named {
		for _, mods := range modifiers {
			check(tea.Key{Code: code, Mod: mods})
		}
	}
	for _, text := range []string{"é", "É", "@", "!", "ö"} {
		for _, mods := range []tea.KeyMod{0, tea.ModAlt} {
			check(tea.Key{Code: tea.KeyExtended, Text: text, Mod: mods})
		}
	}
	t.Log(fmt.Sprintf("checked printable/lock/modifier matrix, %d named keys and associated text", len(named)))
}

// The three categories are intentional: not every synthetic Format output is
// a canonical binding spelling, and an alias need not be an identity spelling.
func TestNormalizeBindingVocabularyBoundary(t *testing.T) {
	cases := []struct{ category, input, want string }{
		{"identity", "alt+X", "alt+X"}, {"identity", "alt+!", "alt+!"},
		{"canonicalization", "ctrl+X", "ctrl+x"}, {"canonicalization", "super+X", "super+x"},
		{"invalid_key", "shift+1", ""}, {"invalid_key", "ctrl+shift+1", ""}, {"invalid_key", "shift+/", ""},
		{"invalid_key", "ab", ""}, {"invalid_key", "alt+ab", ""},
	}
	for _, tc := range cases {
		t.Run(tc.category+"/"+tc.input, func(t *testing.T) {
			got, err := Normalize(tc.input)
			if tc.category == "invalid_key" {
				if err == nil {
					t.Fatalf("unsupported spelling accepted as %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("%q => %q %v, want %q", tc.input, got, err, tc.want)
			}
		})
	}
}
