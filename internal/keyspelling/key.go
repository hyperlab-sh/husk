// Package keyspelling shares the stable slot_key vocabulary with v2 Services.
// The legacy host adopts Format during switchover, not in this standalone task.
package keyspelling

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Format preserves v1 spelling, including Alt-first and printable shifted text.
func Format(key tea.Key) string {
	alt := key.Mod.Contains(tea.ModAlt)
	key.Mod &^= tea.ModAlt | tea.ModNumLock | tea.ModScrollLock
	if key.Mod&(tea.ModCtrl|tea.ModMeta|tea.ModSuper|tea.ModHyper) != 0 && key.Code != tea.KeyExtended {
		key.Text = ""
	}
	if key.Code == tea.KeySpace && key.Mod&^tea.ModCapsLock == tea.ModCtrl {
		key.Code = '@'
	}
	if unicode.IsPrint(key.Code) && key.Mod&^(tea.ModShift|tea.ModCapsLock) == 0 {
		if key.Text == "" {
			code := key.ShiftedCode
			if code == 0 {
				code = key.Code
				if key.Mod.Contains(tea.ModShift) || key.Mod.Contains(tea.ModCapsLock) {
					code = unicode.ToUpper(code)
				}
			}
			key.Text = string(code)
		}
		key.Mod &^= tea.ModShift | tea.ModCapsLock
	}
	name := key.String()
	if name == "space" {
		name = " "
	}
	if alt {
		name = "alt+" + name
	}
	return name
}

// Normalize parses one binding key using Bubble Tea's canonical formatter.
// Canonical host spellings round-trip, including case-sensitive lone-Alt text.
// Ctrl/command letters canonicalize to lowercase; Shift+digit/symbol spellings
// and multi-rune text are rejected rather than guessed. Format may represent
// synthetic keys outside this binding vocabulary; those are not identity cases.
// Sequences are arrays; whitespace-separated key lists are deliberately invalid.
func Normalize(text string) (string, error) {
	original := text
	mods := tea.KeyMod(0)
	for {
		found := false
		for _, item := range []struct {
			name string
			mod  tea.KeyMod
		}{{"alt", tea.ModAlt}, {"ctrl", tea.ModCtrl}, {"shift", tea.ModShift}, {"super", tea.ModSuper}, {"meta", tea.ModMeta}, {"hyper", tea.ModHyper}} {
			if strings.HasPrefix(text, item.name+"+") {
				if mods&item.mod != 0 {
					return "", fmt.Errorf("invalid key")
				}
				mods |= item.mod
				text = strings.TrimPrefix(text, item.name+"+")
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	names := map[string]rune{"enter": tea.KeyEnter, "tab": tea.KeyTab, "backspace": tea.KeyBackspace, "esc": tea.KeyEscape, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "insert": tea.KeyInsert, "delete": tea.KeyDelete, "space": tea.KeySpace}
	for i := 1; i <= 63; i++ {
		names[fmt.Sprintf("f%d", i)] = tea.KeyF1 + rune(i-1)
	}
	code, known := names[text]
	if !known {
		r, n := utf8.DecodeRuneInString(text)
		if n != len(text) || r == utf8.RuneError || !unicode.IsPrint(r) {
			return "", fmt.Errorf("invalid key %q", original)
		}
		code = r
	}
	if mods&(tea.ModCtrl|tea.ModMeta|tea.ModSuper|tea.ModHyper) != 0 && unicode.IsLetter(code) {
		code = unicode.ToLower(code)
	}
	if mods.Contains(tea.ModShift) && unicode.IsPrint(code) && !unicode.IsLetter(code) && !unicode.IsSpace(code) {
		// A symbol's produced text is layout-dependent; require that text,
		// rather than silently binding its unshifted physical key.
		shifted := code
		const bases = "`1234567890-=[]\\;',./"
		const symbols = "~!@#$%^&*()_+{}|:\"<>?"
		for i, r := range bases {
			if r == code {
				shifted = rune(symbols[i])
				break
			}
		}
		hint := Format(tea.Key{Code: shifted, Mod: mods &^ tea.ModShift})
		return "", fmt.Errorf("invalid_key: shifted key produces %q; bind produced character %q", string(shifted), hint)
	}
	return Format(tea.Key{Code: code, Mod: mods}), nil
}

// textNames are the named keys that edit or move through text.
var textNames = map[string]bool{
	"enter": true, "backspace": true, "delete": true, "tab": true,
	"up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "pgup": true, "pgdown": true, "insert": true,
}

// IsTextKey reports whether spelling is a text key: one unmodified key that
// produces or edits text. That is a single printable rune (the wire space
// included) or one of enter, backspace, delete, tab, up, down, left, right,
// home, end, pgup, pgdown and insert. esc, function keys, anything with a
// modifier prefix, the empty string and multi-rune spellings that are not a
// named key (a bracket-wrapped paste) are not text keys.
func IsTextKey(spelling string) bool {
	if textNames[spelling] {
		return true
	}
	r, n := utf8.DecodeRuneInString(spelling)
	return n > 0 && n == len(spelling) && r != utf8.RuneError && unicode.IsPrint(r)
}
