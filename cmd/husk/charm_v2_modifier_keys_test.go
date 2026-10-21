package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
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
	m := newWorkbenchModel("", nil, t.TempDir())
	m.recomputeLayout()
	if !m.View().KeyboardEnhancements.ReportAllKeysAsEscapeCodes {
		t.Fatal("View must request all keys as escape codes or bare modifier presses never arrive")
	}
	m.width, m.height = 10, 5
	if !m.tooSmall() || !m.View().KeyboardEnhancements.ReportAllKeysAsEscapeCodes {
		t.Fatal("the too-small view must keep the same keyboard flags")
	}
}

// Sequences as a kitty-protocol terminal sends them with flags
// disambiguate|event-types|alternate-keys|all-keys-as-escape-codes.
func TestDecodedBareModifiersDriveModifierState(t *testing.T) {
	type step struct {
		input string
		want  huskipc.ModifierState
		up    string // key_state key expected, "" for none
		phase huskipc.KeyPhase
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"shift", []step{
			{"\x1b[57441;2u", huskipc.ModifierState{Shift: true}, "shift", huskipc.KeyPhaseDown},
			{"\x1b[57441;2:2u", huskipc.ModifierState{Shift: true}, "", ""}, // repeat
			{"\x1b[57441;1:3u", huskipc.ModifierState{}, "shift", huskipc.KeyPhaseUp},
		}},
		{"ctrl", []step{
			{"\x1b[57442;5u", huskipc.ModifierState{Ctrl: true}, "ctrl", huskipc.KeyPhaseDown},
			{"\x1b[57442;1:3u", huskipc.ModifierState{}, "ctrl", huskipc.KeyPhaseUp},
		}},
		{"alt", []step{
			{"\x1b[57443;3u", huskipc.ModifierState{Alt: true}, "alt", huskipc.KeyPhaseDown},
			{"\x1b[57443;1:3u", huskipc.ModifierState{}, "alt", huskipc.KeyPhaseUp},
		}},
		{"super", []step{
			{"\x1b[57444;9u", huskipc.ModifierState{Super: true}, "super", huskipc.KeyPhaseDown},
			{"\x1b[57444;1:3u", huskipc.ModifierState{}, "super", huskipc.KeyPhaseUp},
		}},
		{"left-and-right-shift-overlap", []step{
			{"\x1b[57441;2u", huskipc.ModifierState{Shift: true}, "shift", huskipc.KeyPhaseDown},
			{"\x1b[57447;2u", huskipc.ModifierState{Shift: true}, "", ""},
			{"\x1b[57441;2:3u", huskipc.ModifierState{Shift: true}, "", ""},
			{"\x1b[57447;1:3u", huskipc.ModifierState{}, "shift", huskipc.KeyPhaseUp},
		}},
		{"ctrl-then-shift", []step{
			{"\x1b[57442;5u", huskipc.ModifierState{Ctrl: true}, "ctrl", huskipc.KeyPhaseDown},
			{"\x1b[57441;6u", huskipc.ModifierState{Ctrl: true, Shift: true}, "shift", huskipc.KeyPhaseDown},
			{"\x1b[57442;2:3u", huskipc.ModifierState{Shift: true}, "ctrl", huskipc.KeyPhaseUp},
			{"\x1b[57441;1:3u", huskipc.ModifierState{}, "shift", huskipc.KeyPhaseUp},
		}},
		{"hyper-meta-dropped", []step{
			{"\x1b[57445;17u", huskipc.ModifierState{}, "", ""},
			{"\x1b[57446;33u", huskipc.ModifierState{}, "", ""},
			{"\x1b[57446;1:3u", huskipc.ModifierState{}, "", ""},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
			_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
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
	m := newWorkbenchModel("", nil, t.TempDir())
	m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
	m.altLatched = true
	m.modifiers.Alt = true
	m.Update(decodeCharmEvent(t, "\x1b[57443;3u"))
	m.Update(decodeCharmEvent(t, "\x1b[57443;1:3u"))
	if !m.modifiers.Alt {
		t.Fatal("releasing the Alt key cleared a latched Alt")
	}
}

func TestBlurDropsTerminalModifierKeys(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
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

// With all keys sent as escape codes, text keys arrive as CSI-u without
// associated text. They must keep their wire names and still type into the
// palette; IME commits and bracketed paste are not CSI-u and are unaffected.
func TestAllKeysAsEscapeCodesKeepTextInput(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"\x1b[97u", "a"},
		{"\x1b[97;2u", "A"},
		{"\x1b[49:33;2u", "!"},
		{"\x1b[32u", " "},
		{"\x1b[13u", "enter"},
		{"\x1b[9u", "tab"},
		{"\x1b[127u", "backspace"},
		{"\x1b[27u", "esc"},
		{"\x1b[99;5u", "ctrl+c"},
		{"\x1b[120;3u", "alt+x"},
		{"界", "界"}, // an IME commit arrives as plain UTF-8
	} {
		msg, ok := decodeCharmEvent(t, tc.input).(tea.KeyPressMsg)
		if !ok {
			t.Fatalf("%q did not decode as a key press", tc.input)
		}
		if got := legacyKeyString(msg); got != tc.want {
			t.Fatalf("%q: wire name %q, want %q", tc.input, got, tc.want)
		}
	}

	m := newWorkbenchModel("", nil, t.TempDir())
	m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
	m.width, m.height = 144, 40
	m.recomputeLayout()
	m.showPalette = true
	for _, in := range []string{"\x1b[57441;2u", "\x1b[97;2u", "\x1b[57441;1:3u", "\x1b[98u", "界"} {
		m.Update(decodeCharmEvent(t, in))
	}
	if m.paletteQuery != "Ab界" {
		t.Fatalf("palette query = %q, want %q", m.paletteQuery, "Ab界")
	}

	if _, ok := decodeCharmEvent(t, "\x1b[200~").(uv.PasteStartEvent); !ok {
		t.Fatal("bracketed paste start no longer decodes")
	}
}
