package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// With ReportAlternateKeys the wire spelling of a chord follows the
// layout-independent base key, and a shifted symbol under Alt is spelled by
// the symbol itself. This pins the documented spellings.
func TestKittyWireSpellingOfAlternateKeys(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"alt+shift+1 is alt+!", "\x1b[49:33;4u", "alt+!"},
		{"alt+shift+1 with text is alt+!", "\x1b[49:33;4;33u", "alt+!"},
		{"alt+1 stays alt+1", "\x1b[49;3u", "alt+1"},
		{"alt+shift+a is alt+A", "\x1b[97:65;4u", "alt+A"},
		{"cyrillic ctrl+ф is ctrl+a", "\x1b[1092:1060:97;5u", "ctrl+a"},
		{"cyrillic ctrl+ф without base is ctrl+ф", "\x1b[1092;5u", "ctrl+ф"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := decodeCharmEvent(t, tc.input).(tea.KeyPressMsg)
			if !ok {
				t.Fatalf("%q did not decode as a key press", tc.input)
			}
			if got := legacyKeyString(normalizeTextEvent(msg)); got != tc.want {
				t.Fatalf("%q: wire name %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// On a terminal that reports releases but not kitty flag 8, Enter, Tab and
// Backspace releases never arrive, so the held key outlives the physical key until the 1s safety net lapses.
func TestUnreportedEnterTabBackspaceReleaseLapsesAtSafetyNet(t *testing.T) {
	for _, tc := range []struct{ name, press, held string }{
		{"enter", "\x1b[13u", "enter"},
		{"tab", "\x1b[9u", "tab"},
		{"backspace", "\x1b[127u", "backspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
			m.keyReleases = true
			_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p"})
			m.Update(decodeCharmEvent(t, tc.press))
			if _, held := m.heldKeys[tc.held]; !held {
				t.Fatalf("press held %v, want %s", m.heldKeys, tc.held)
			}

			// No release arrives. Past the 320ms fallback but inside 1s the key
			// is still reported held.
			m.heldKeys[tc.held] = time.Now().Add(-500 * time.Millisecond)
			out.Reset()
			m.Update(modifierTickMsg{})
			if _, held := m.heldKeys[tc.held]; !held || len(envelopes(t, out)) != 0 {
				t.Fatalf("key dropped before the safety net: held %v", m.heldKeys)
			}

			// Past 1s the safety net sends up.
			m.heldKeys[tc.held] = time.Now().Add(-2 * time.Second)
			m.Update(modifierTickMsg{})
			msgs := envelopes(t, out)
			if len(m.heldKeys) != 0 || len(msgs) != 1 || msgs[0].KeyState == nil ||
				msgs[0].KeyState.Phase != huskipc.KeyPhaseUp || msgs[0].KeyState.Key != tc.held {
				t.Fatalf("safety net: held %v, sent %+v", m.heldKeys, msgs)
			}
		})
	}
}

// With flag 8 granted (View requests ReportAllKeysAsEscapeCodes) the release
// of Enter, Tab and Backspace arrives and clears the held key at once.
func TestFlag8EnterTabBackspaceReleaseClearsHeldKey(t *testing.T) {
	for _, tc := range []struct{ name, press, release, held string }{
		{"enter", "\x1b[13u", "\x1b[13;1:3u", "enter"},
		{"tab", "\x1b[9u", "\x1b[9;1:3u", "tab"},
		{"backspace", "\x1b[127u", "\x1b[127;1:3u", "backspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
			m.keyReleases = true
			_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p"})
			m.Update(decodeCharmEvent(t, tc.press))
			if _, held := m.heldKeys[tc.held]; !held {
				t.Fatalf("press held %v, want %s", m.heldKeys, tc.held)
			}
			out.Reset()
			m.Update(decodeCharmEvent(t, tc.release))
			msgs := envelopes(t, out)
			if len(m.heldKeys) != 0 || len(msgs) != 1 || msgs[0].KeyState == nil ||
				msgs[0].KeyState.Phase != huskipc.KeyPhaseUp || msgs[0].KeyState.Key != tc.held {
				t.Fatalf("release: held %v, sent %+v", m.heldKeys, msgs)
			}
		})
	}
}
