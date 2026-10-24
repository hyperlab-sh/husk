package workbench

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Bubble Tea converts ultraviolet key events by a direct type conversion. Use
// its pinned decoder here so legacy terminal bytes exercise the actual fields,
// including empty Text for Alt-prefixed printable keys.
func decodeCharmKey(t *testing.T, input string) tea.KeyPressMsg {
	t.Helper()
	var decoder uv.EventDecoder
	n, event := decoder.Decode([]byte(input))
	key, ok := event.(uv.KeyPressEvent)
	if !ok || n != len(input) {
		t.Fatalf("decode %q: consumed %d bytes, event %#v", input, n, event)
	}
	return tea.KeyPressMsg(key)
}

func TestCharmV2DecodedLegacyControlNames(t *testing.T) {
	// These are v1's keyNames for all C0 controls, Space and DEL.
	names := []string{
		"ctrl+@", "ctrl+a", "ctrl+b", "ctrl+c", "ctrl+d", "ctrl+e", "ctrl+f", "ctrl+g",
		"ctrl+h", "tab", "ctrl+j", "ctrl+k", "ctrl+l", "enter", "ctrl+n", "ctrl+o",
		"ctrl+p", "ctrl+q", "ctrl+r", "ctrl+s", "ctrl+t", "ctrl+u", "ctrl+v", "ctrl+w",
		"ctrl+x", "ctrl+y", "ctrl+z", "esc", "ctrl+\\", "ctrl+]", "ctrl+^", "ctrl+_", " ",
	}
	for code, want := range names {
		for _, alt := range []bool{false, true} {
			input, expected := string(rune(code)), want
			if alt {
				input, expected = "\x1b"+input, "alt+"+expected
			}
			t.Run(expected, func(t *testing.T) {
				if got := legacyKeyString(decodeCharmKey(t, input)); got != expected {
					t.Fatalf("input %q: wire name %q, want %q", input, got, expected)
				}
			})
		}
	}
	for _, input := range []string{"\x7f", "\x1b\x7f"} {
		want := "backspace"
		if strings.HasPrefix(input, "\x1b") {
			want = "alt+" + want
		}
		if got := legacyKeyString(decodeCharmKey(t, input)); got != want {
			t.Fatalf("input %q: wire name %q, want %q", input, got, want)
		}
	}
}

func TestCharmV2DecodedAltShiftedText(t *testing.T) {
	for _, text := range []string{"A", "O", "P", "X", "Z", "?", "!", "界", "Ü"} {
		t.Run(text, func(t *testing.T) {
			if got := legacyKeyString(decodeCharmKey(t, "\x1b"+text)); got != "alt+"+text {
				t.Fatalf("Alt+%s wire name = %q", text, got)
			}
		})
	}
}

func TestCharmV2DecodedKeyStateAndReleaseNames(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"ctrl-space", "\x00", "ctrl+@"},
		{"alt-ctrl-space", "\x1b\x00", "alt+ctrl+@"},
		{"alt-ctrl-x", "\x1b\x18", "alt+ctrl+x"},
		{"alt-shift-x", "\x1bX", "alt+X"},
		{"shift-x", "X", "X"},
		{"ctrl-space-numlock", "\x1b[32;133u", "ctrl+@"},
		{"ctrl-space-capslock", "\x1b[32;69u", "ctrl+@"},
		{"alt-ctrl-space-locks", "\x1b[32;199u", "alt+ctrl+@"},
		{"alt-shift-x-numlock", "\x1b[120;132u", "alt+X"},
		{"alt-a-capslock", "\x1b[97;67u", "alt+A"},
		{"alt-a-locks", "\x1b[97;195u", "alt+A"},
		{"a-locks", "\x1b[97;193u", "A"},
		{"alt-ctrl-x-locks", "\x1b[120;199u", "alt+ctrl+x"},
		{"shift-x-locks", "\x1b[120;194u", "X"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := decodeCharmKey(t, tc.input)
			m := newModel()
			_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
			out.Reset()
			m.Update(key)
			keys, downs := 0, 0
			for _, msg := range envelopes(t, out) {
				if msg.SlotKey != nil {
					keys++
					if msg.SlotKey.Key != tc.want {
						t.Fatalf("slot_key = %q, want %q", msg.SlotKey.Key, tc.want)
					}
				}
				if state := msg.KeyState; state != nil && state.Key != "alt" {
					downs++
					if state.Key != tc.want || state.Phase != "down" || !state.Held {
						t.Fatalf("key_state = %+v, want down for %q", state, tc.want)
					}
				}
			}
			if keys != 1 || downs != 1 {
				t.Fatalf("got %d slot keys and %d key-down events", keys, downs)
			}
			if _, held := m.heldKeys[tc.want]; !held {
				t.Fatal("normalized key not held")
			}

			// CSI-u event type 3 is a native release. Decode it too, rather
			// than assuming its fields match those of the press.
			release := tea.KeyReleaseMsg(key)
			if strings.HasPrefix(tc.input, "\x1b[") && strings.HasSuffix(tc.input, "u") {
				input := strings.TrimSuffix(tc.input, "u") + ":3u"
				var decoder uv.EventDecoder
				n, event := decoder.Decode([]byte(input))
				native, ok := event.(uv.KeyReleaseEvent)
				if !ok || n != len(input) {
					t.Fatalf("decode release %q: consumed %d bytes, event %#v", input, n, event)
				}
				release = tea.KeyReleaseMsg(native)
			}
			// Native releases may omit printable Text. ShiftedCode or
			// Shift/CapsLock fallback must still match the press spelling.
			release.Text = ""
			out.Reset()
			m.Update(release)
			if len(m.heldKeys) != 0 {
				t.Fatalf("released key still held: %+v", m.heldKeys)
			}
			msgs := envelopes(t, out)
			if len(msgs) != 1 || msgs[0].KeyState == nil {
				t.Fatalf("release must emit only key-up: %+v", msgs)
			}
			state := msgs[0].KeyState
			if state.Key != tc.want || state.Phase != "up" || state.Held {
				t.Fatalf("key-up = %+v, want %q", state, tc.want)
			}
		})
	}
}
