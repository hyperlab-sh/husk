package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// decodeCharmEvent decodes one terminal event with the pinned decoder and
// converts key events the way Bubble Tea does.
func decodeCharmEvent(t *testing.T, input string) tea.Msg {
	t.Helper()
	var decoder uv.EventDecoder
	n, event := decoder.Decode([]byte(input))
	if n != len(input) {
		t.Fatalf("decode %q: consumed %d bytes, event %#v", input, n, event)
	}
	switch e := event.(type) {
	case uv.KeyPressEvent:
		return tea.KeyPressMsg(e)
	case uv.KeyReleaseEvent:
		return tea.KeyReleaseMsg(e)
	}
	return event
}

func TestViewRequestsAllKeysAsEscapeCodes(t *testing.T) {
	m := newWorkbenchModel()
	m.recomputeLayout()
	ke := m.View().KeyboardEnhancements
	if !ke.ReportAllKeysAsEscapeCodes {
		t.Fatal("View must request all keys as escape codes or bare modifier presses never arrive")
	}
	if !ke.ReportAssociatedText {
		t.Fatal("View must request associated text or AltGr and dead-key text is lost")
	}
	m.width, m.height = 10, 5
	if !m.tooSmall() || m.View().KeyboardEnhancements != ke {
		t.Fatal("the too-small view must keep the same keyboard flags")
	}
}

// Lock keys are reported only in all-keys mode. They are swallowed, so the
// plugin sees no new key names, while an ordinary key still reaches it.
func TestLockKeysAreSwallowed(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.width, m.height = 144, 40
	m.recomputeLayout()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	m.focusedSlot = protocol.SurfaceSidebarList
	for _, in := range []string{
		"\x1b[57358u", "\x1b[57358;65:3u", // caps lock press, release (lock now on)
		"\x1b[57360u", "\x1b[57360;129:3u", // num lock
		"\x1b[57359u", "\x1b[57359;1:3u", // scroll lock
	} {
		out.Reset()
		m.Update(decodeCharmEvent(t, in))
		if msgs := envelopes(t, out); len(msgs) != 0 {
			t.Fatalf("%q: lock key produced messages %+v", in, msgs)
		}
		if len(m.heldKeys) != 0 || m.modifiers != (protocol.ModifierState{}) {
			t.Fatalf("%q: held %+v, modifiers %+v", in, m.heldKeys, m.modifiers)
		}
	}
	out.Reset()
	m.Update(decodeCharmEvent(t, "\x1b[106;65;74u")) // j with caps lock on
	var keys []string
	for _, msg := range envelopes(t, out) {
		if msg.SlotKey != nil {
			keys = append(keys, msg.SlotKey.Key)
		}
	}
	if len(keys) != 1 || keys[0] != "J" {
		t.Fatalf("ordinary key after the lock keys: slot_key %q, want [J]", keys)
	}
}

// Sequences as a kitty-protocol terminal sends them with flags
// disambiguate|event-types|alternate-keys|all-keys-as-escape-codes.
func TestDecodedBareModifiersDriveModifierState(t *testing.T) {
	type step struct {
		input string
		want  protocol.ModifierState
		up    string // key_state key expected, "" for none
		phase protocol.KeyPhase
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"shift", []step{
			{"\x1b[57441;2u", protocol.ModifierState{Shift: true}, "shift", protocol.KeyPhaseDown},
			{"\x1b[57441;2:2u", protocol.ModifierState{Shift: true}, "", ""}, // repeat
			{"\x1b[57441;1:3u", protocol.ModifierState{}, "shift", protocol.KeyPhaseUp},
		}},
		{"ctrl", []step{
			{"\x1b[57442;5u", protocol.ModifierState{Ctrl: true}, "ctrl", protocol.KeyPhaseDown},
			{"\x1b[57442;1:3u", protocol.ModifierState{}, "ctrl", protocol.KeyPhaseUp},
		}},
		{"alt", []step{
			{"\x1b[57443;3u", protocol.ModifierState{Alt: true}, "alt", protocol.KeyPhaseDown},
			{"\x1b[57443;1:3u", protocol.ModifierState{}, "alt", protocol.KeyPhaseUp},
		}},
		{"super", []step{
			{"\x1b[57444;9u", protocol.ModifierState{Super: true}, "super", protocol.KeyPhaseDown},
			{"\x1b[57444;1:3u", protocol.ModifierState{}, "super", protocol.KeyPhaseUp},
		}},
		{"left-and-right-shift-overlap", []step{
			{"\x1b[57441;2u", protocol.ModifierState{Shift: true}, "shift", protocol.KeyPhaseDown},
			{"\x1b[57447;2u", protocol.ModifierState{Shift: true}, "", ""},
			{"\x1b[57441;2:3u", protocol.ModifierState{Shift: true}, "", ""},
			{"\x1b[57447;1:3u", protocol.ModifierState{}, "shift", protocol.KeyPhaseUp},
		}},
		{"ctrl-then-shift", []step{
			{"\x1b[57442;5u", protocol.ModifierState{Ctrl: true}, "ctrl", protocol.KeyPhaseDown},
			{"\x1b[57441;6u", protocol.ModifierState{Ctrl: true, Shift: true}, "shift", protocol.KeyPhaseDown},
			{"\x1b[57442;2:3u", protocol.ModifierState{Shift: true}, "ctrl", protocol.KeyPhaseUp},
			{"\x1b[57441;1:3u", protocol.ModifierState{}, "shift", protocol.KeyPhaseUp},
		}},
		{"hyper-meta-dropped", []step{
			{"\x1b[57445;17u", protocol.ModifierState{}, "", ""},
			{"\x1b[57446;33u", protocol.ModifierState{}, "", ""},
			{"\x1b[57446;1:3u", protocol.ModifierState{}, "", ""},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel()
			m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
			_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
			for _, s := range tc.steps {
				out.Reset()
				m.Update(decodeCharmEvent(t, s.input))
				if m.modifiers != s.want {
					t.Fatalf("%q: modifiers = %+v, want %+v", s.input, m.modifiers, s.want)
				}
				if len(m.heldKeys) != 0 {
					t.Fatalf("%q: modifier key recorded as held key: %+v", s.input, m.heldKeys)
				}
				msgs := envelopes(t, out)
				for _, msg := range msgs {
					if msg.SlotKey != nil {
						t.Fatalf("%q: modifier key forwarded as slot_key %q", s.input, msg.SlotKey.Key)
					}
				}
				if s.up == "" {
					if len(msgs) != 0 {
						t.Fatalf("%q: unexpected messages %+v", s.input, msgs)
					}
					continue
				}
				if len(msgs) != 1 || msgs[0].KeyState == nil {
					t.Fatalf("%q: want one key_state, got %+v", s.input, msgs)
				}
				ks := msgs[0].KeyState
				if ks.Key != s.up || ks.Phase != s.phase || ks.Modifiers != s.want {
					t.Fatalf("%q: key_state = %+v, want %s %s", s.input, ks, s.up, s.phase)
				}
			}
		})
	}
}

func TestBareAltReleaseKeepsLatchedAlt(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.altLatched = true
	m.modifiers.Alt = true
	m.Update(decodeCharmEvent(t, "\x1b[57443;3u"))
	m.Update(decodeCharmEvent(t, "\x1b[57443;1:3u"))
	if !m.modifiers.Alt {
		t.Fatal("releasing the Alt key cleared a latched Alt")
	}
}

func TestBlurDropsTerminalModifierKeys(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.Update(decodeCharmEvent(t, "\x1b[57441;2u")) // left shift down
	m.Update(tea.BlurMsg{})                        // its release goes to another window
	if m.modifiers.Shift {
		t.Fatal("blur kept Shift")
	}
	m.Update(tea.FocusMsg{})
	m.Update(decodeCharmEvent(t, "\x1b[57447;2u"))   // right shift down
	m.Update(decodeCharmEvent(t, "\x1b[57447;1:3u")) // right shift up
	if m.modifiers.Shift {
		t.Fatal("a left Shift press from before the blur is still counted")
	}
}

// With all keys sent as escape codes, text keys arrive as CSI-u, with the
// text the layout produced as the third parameter (associated text). The
// sequences follow the encoders of kitty (key_encoding.c), foot (input.c,
// kitty_kbd_protocol) and ghostty (input/key_encode.zig). Keys must keep
// their wire names and still type into the palette, including text the key
// code alone cannot give: AltGr symbols and dead-key or compose results.
//
// IME commits do not go through the key encoder in kitty (keys.c writes
// GLFW_IME_COMMIT_TEXT to the child as text), foot (ime.c, term_to_slave) or
// wezterm (Key::Composed, send_composed_text), so they stay plain UTF-8.
// Ghostty encodes an IME commit as plain UTF-8 when no key is attached and as
// CSI-u with associated text otherwise; both forms are covered here.
func TestAllKeysAsEscapeCodesKeepTextInput(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"\x1b[97u", "a"},
		{"\x1b[97;;97u", "a"}, // foot omits the modifier field before text
		{"\x1b[97;2u", "A"},
		{"\x1b[97:65;2;65u", "A"},
		{"\x1b[49:33;2u", "!"},
		{"\x1b[49:33;2;33u", "!"},
		{"\x1b[32u", " "},
		{"\x1b[32;;32u", " "},
		{"\x1b[13u", "enter"},
		{"\x1b[9u", "tab"},
		{"\x1b[127u", "backspace"},
		{"\x1b[27u", "esc"},
		{"\x1b[113;1;64u", "@"},      // AltGr+Q on a German layout
		{"\x1b[101;;233u", "é"},      // dead acute, then e (kitty, ghostty)
		{"\x1b[233;;233u", "é"},      // the same through foot's xkb compose
		{"\x1b[101;;101:769u", "é"}, // a composed result of two code points
		{"\x1b[0;;229u", "å"},        // pure text event: no key, only text
		{"\x1b[0;3;229u", "alt+å"},   // the same with a modifier left on
		{"\x1b[0;;101:769u", "é"},
		{"\x1b[106;;30028u", "界"}, // IME commit on the key that confirmed it (ghostty)
		{"界", "界"},                // IME commit written as plain UTF-8
	} {
		msg, ok := decodeCharmEvent(t, tc.input).(tea.KeyPressMsg)
		if !ok {
			t.Fatalf("%q did not decode as a key press", tc.input)
		}
		if got := legacyKeyString(normalizeTextEvent(msg)); got != tc.want {
			t.Fatalf("%q: wire name %q, want %q", tc.input, got, tc.want)
		}
	}

	// Chords keep their names whether or not a terminal attaches text.
	for _, tc := range []struct{ bare, withText, want string }{
		{"\x1b[99;5u", "\x1b[99;5;99u", "ctrl+c"},
		{"\x1b[97:65;6u", "\x1b[97:65;6;65u", "ctrl+shift+a"},
		{"\x1b[120;3u", "\x1b[120;3;120u", "alt+x"},
		{"\x1b[97:65;4u", "\x1b[97:65;4;65u", "alt+A"},
		{"\x1b[120;9u", "\x1b[120;9;120u", "super+x"},
		{"\x1b[32;5u", "\x1b[32;5;32u", "ctrl+@"},
	} {
		for _, in := range []string{tc.bare, tc.withText} {
			msg, ok := decodeCharmEvent(t, in).(tea.KeyPressMsg)
			if !ok {
				t.Fatalf("%q did not decode as a key press", in)
			}
			if got := legacyKeyString(normalizeTextEvent(msg)); got != tc.want {
				t.Fatalf("%q: wire name %q, want %q", in, got, tc.want)
			}
		}
	}

	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.width, m.height = 144, 40
	m.recomputeLayout()
	m.showPalette = true
	for _, in := range []string{
		"\x1b[57441;2u", "\x1b[97:65;2;65u", "\x1b[57441;1:3u", // Shift, A, release
		"\x1b[98;;98u", "\x1b[113;1;64u", "\x1b[101;;233u", "\x1b[0;;229u", "界",
		"\x1b[99;5;99u", // ctrl+c with text closes the palette, never types
	} {
		m.Update(decodeCharmEvent(t, in))
		if in == "界" && m.paletteQuery != "Ab@éå界" {
			t.Fatalf("palette query = %q, want %q", m.paletteQuery, "Ab@éå界")
		}
	}
	if m.showPalette {
		t.Fatal("ctrl+c with associated text did not close the palette")
	}

	if _, ok := decodeCharmEvent(t, "\x1b[200~").(uv.PasteStartEvent); !ok {
		t.Fatal("bracketed paste start no longer decodes")
	}
}

// Layout text arrives on press and repeat only, so a release is named by its
// key code. The release of AltGr+Q (held as "@") and of a dead key (held as
// "é") must still clear the held key and send key_state up.
func TestLayoutTextReleaseClearsHeldKey(t *testing.T) {
	for _, tc := range []struct{ name, press, release, held string }{
		{"altgr", "\x1b[113;1;64u", "\x1b[113;1:3u", "@"},
		{"dead key", "\x1b[101;1;233u", "\x1b[101;1:3u", "é"},
		{"plain", "\x1b[113;1;113u", "\x1b[113;1:3u", "q"},
		{"shifted", "\x1b[97:65;2;65u", "\x1b[97:65;2:3u", "A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel()
			m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
			m.keyReleases = true
			_, out := addPublicationTestPlugin(m, hello{PluginID: "p"})
			m.Update(decodeCharmEvent(t, tc.press))
			if _, held := m.heldKeys[tc.held]; !held || len(m.heldKeys) != 1 {
				t.Fatalf("press held %v, want [%s]", m.heldKeys, tc.held)
			}
			out.Reset()
			m.Update(decodeCharmEvent(t, tc.release))
			if len(m.heldKeys) != 0 || len(m.heldAliases) != 0 {
				t.Fatalf("release left held %v, aliases %v", m.heldKeys, m.heldAliases)
			}
			msgs := envelopes(t, out)
			if len(msgs) != 1 || msgs[0].KeyState == nil || msgs[0].KeyState.Phase != protocol.KeyPhaseUp || msgs[0].KeyState.Key != tc.held {
				t.Fatalf("release sent %+v, want one key_state up for %q", msgs, tc.held)
			}
		})
	}
}

// An alias whose key expired must not swallow a later plain release, and
// expiry and blur drop aliases with their keys.
func TestLayoutTextAliasDoesNotOutliveItsKey(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.keyReleases = true
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p"})
	m.Update(decodeCharmEvent(t, "\x1b[113;1;64u"))
	m.heldKeys["@"] = time.Now().Add(-2 * time.Second)
	m.Update(modifierTickMsg{})
	if len(m.heldKeys) != 0 || len(m.heldAliases) != 0 {
		t.Fatalf("expiry left held %v, aliases %v", m.heldKeys, m.heldAliases)
	}

	m.Update(decodeCharmEvent(t, "\x1b[113;1;64u"))
	delete(m.heldKeys, "@") // simulate a lost key whose alias is still recorded
	m.Update(decodeCharmEvent(t, "\x1b[113;1;113u"))
	out.Reset()
	m.Update(decodeCharmEvent(t, "\x1b[113;1:3u"))
	msgs := envelopes(t, out)
	if len(m.heldKeys) != 0 || len(msgs) != 1 || msgs[0].KeyState == nil || msgs[0].KeyState.Key != "q" {
		t.Fatalf("plain release after a stale alias: held %v, sent %+v", m.heldKeys, msgs)
	}

	m.Update(decodeCharmEvent(t, "\x1b[113;1;64u"))
	m.Update(tea.BlurMsg{})
	if len(m.heldKeys) != 0 || len(m.heldAliases) != 0 {
		t.Fatalf("blur left held %v, aliases %v", m.heldKeys, m.heldAliases)
	}
}
