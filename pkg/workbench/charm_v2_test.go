package workbench

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestCharmV2ViewDeclaresTerminalModes(t *testing.T) {
	m := newWorkbenchModel()
	m.recomputeLayout()
	m.seedPubslotsDemo()
	view := m.View()
	if !view.AltScreen || view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("terminal modes lost: alt=%v mouse=%v", view.AltScreen, view.MouseMode)
	}
	if !strings.Contains(ansi.Strip(view.Content), "[Inspect]") {
		t.Fatal("view content missing composed frame")
	}
}

func TestViewRequestsKeyEventTypes(t *testing.T) {
	m := newWorkbenchModel()
	m.recomputeLayout()
	if !m.View().KeyboardEnhancements.ReportEventTypes {
		t.Fatal("View must request kitty key event types or KeyReleaseMsg never fires")
	}
}

func TestKeyReleaseClearsHeldKeyWithoutTimer(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p"})
	m.Update(tea.KeyboardEnhancementsMsg{Flags: ansi.KittyReportEventTypes})
	if !m.keyReleases {
		t.Fatal("event-type support not recorded")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	// A held key older than the 320ms fallback must not be expired by the tick
	// when the terminal reports releases.
	m.heldKeys["x"] = time.Now().Add(-500 * time.Millisecond)
	out.Reset()
	m.Update(modifierTickMsg{})
	if _, held := m.heldKeys["x"]; !held || out.Len() != 0 {
		t.Fatal("timer expired a key although the terminal reports releases")
	}
	m.Update(tea.KeyReleaseMsg{Code: 'x', Text: "x"})
	if _, held := m.heldKeys["x"]; held {
		t.Fatal("release did not clear the held key")
	}
	msgs := envelopes(t, out)
	if len(msgs) != 1 || msgs[0].KeyState == nil || msgs[0].KeyState.Phase != "up" {
		t.Fatalf("release did not emit one key-up: %+v", msgs)
	}
}

func TestHeldKeyTimerStillExpiresWithoutEventTypes(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m.heldKeys["x"] = time.Now().Add(-time.Second)
	m.Update(modifierTickMsg{})
	if len(m.heldKeys) != 0 {
		t.Fatal("fallback timer must still expire keys when releases are unreported")
	}
}

func TestHeldKeySafetyExpiryWithEventTypes(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.Update(tea.KeyboardEnhancementsMsg{Flags: ansi.KittyReportEventTypes})
	m.heldKeys["x"] = time.Now().Add(-2 * time.Second)
	m.Update(modifierTickMsg{})
	if len(m.heldKeys) != 0 {
		t.Fatal("a key whose release never arrived must lapse via the safety expiry")
	}
}

func TestKeyboardEnhancementsFallbackResumesShortExpiry(t *testing.T) {
	m := newWorkbenchModel()
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.Update(tea.KeyboardEnhancementsMsg{Flags: ansi.KittyReportEventTypes})
	m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m.heldKeys["x"] = time.Now().Add(-500 * time.Millisecond)
	m.Update(modifierTickMsg{})
	if len(m.heldKeys) != 0 {
		t.Fatal("short expiry did not resume after event types were lost")
	}
}

func TestViewRequestsAlternateKeys(t *testing.T) {
	m := newWorkbenchModel()
	m.recomputeLayout()
	if !m.View().KeyboardEnhancements.ReportAlternateKeys {
		t.Fatal("View must request alternate keys so shifted-symbol releases match their press")
	}
}

func TestDecodedShiftedReleaseMatchesPress(t *testing.T) {
	// Sequences as sent by a terminal with flags disambiguate|event-types|alternate-keys.
	for _, tc := range []struct{ name, press, release, want string }{
		{"bang", "!", "\x1b[49:33;2:3u", "!"},
		{"question", "?", "\x1b[47:63;2:3u", "?"},
		{"enter", "\r", "\x1b[13;1:3u", "enter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d uv.EventDecoder
			_, pe := d.Decode([]byte(tc.press))
			n, re := d.Decode([]byte(tc.release))
			if n == 0 {
				t.Fatal("release not decoded")
			}
			pk, ok1 := pe.(uv.KeyPressEvent)
			rk, ok2 := re.(uv.KeyReleaseEvent)
			if !ok1 || !ok2 {
				t.Fatalf("decoded %T / %T", pe, re)
			}
			ps, rs := legacyKeyString(tea.KeyPressMsg(pk)), legacyKeyString(tea.KeyReleaseMsg(rk))
			if ps != tc.want || rs != tc.want {
				t.Fatalf("press %q release %q, want %q", ps, rs, tc.want)
			}
		})
	}
}

func TestCharmV2KeyWireSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"space", tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, " "},
		{"alt-space", tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModAlt}, "alt+ "},
		{"text", tea.KeyPressMsg{Code: 'x', Text: "x"}, "x"},
		{"unicode", tea.KeyPressMsg{Code: '界', Text: "界"}, "界"},
		{"shifted", tea.KeyPressMsg{Code: 'x', ShiftedCode: 'X', Text: "X", Mod: tea.ModShift}, "X"},
		{"alt", tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}, "alt+x"},
		{"ctrl", tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}, "ctrl+x"},
		{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}, "enter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel()
			_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
			out.Reset()
			m.Update(tc.key)
			var keys []string
			for _, msg := range envelopes(t, out) {
				if msg.SlotKey != nil {
					if msg.SlotKey.Slot != protocol.SurfaceSidebarList {
						t.Fatal("key routed to the wrong surface")
					}
					keys = append(keys, msg.SlotKey.Key)
				}
			}
			if len(keys) != 1 || keys[0] != tc.want {
				t.Fatalf("wire keys = %q, want %q", keys, tc.want)
			}
			if tc.key.Mod.Contains(tea.ModAlt) && !m.modifiers.Alt {
				t.Fatal("Alt modifier was not synchronized")
			}
		})
	}
}

func TestCharmV2PastePreservesWireAndDoesNotRunShortcuts(t *testing.T) {
	m := newWorkbenchModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	out.Reset()
	_, cmd := m.Update(tea.PasteMsg{Content: "q"})
	if cmd != nil {
		t.Fatal("pasted q triggered a host command")
	}
	var keys []string
	for _, msg := range envelopes(t, out) {
		if msg.SlotKey != nil {
			keys = append(keys, msg.SlotKey.Key)
		}
	}
	if len(keys) != 1 || keys[0] != "[q]" {
		t.Fatalf("paste wire spelling = %q, want v1 bracketed spelling", keys)
	}
	m.showPalette = true
	m.Update(tea.PasteMsg{Content: "["})
	if !m.showSidebar || !m.showPalette || m.paletteQuery != "[" {
		t.Fatal("paste ran a dock shortcut or mis-handled palette input")
	}
}

func TestCharmV2KeyReleaseDoesNotRepeatShortcut(t *testing.T) {
	m := newWorkbenchModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p"})
	m.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	if m.showSidebar {
		t.Fatal("press did not toggle sidebar")
	}
	out.Reset()
	m.Update(tea.KeyReleaseMsg{Code: '[', Text: "["})
	if m.showSidebar {
		t.Fatal("release repeated sidebar shortcut")
	}
	if _, held := m.heldKeys["["]; held {
		t.Fatal("released key remained held")
	}
	msgs := envelopes(t, out)
	if len(msgs) != 1 || msgs[0].KeyState == nil || msgs[0].KeyState.Phase != "up" || msgs[0].KeyState.Held {
		t.Fatalf("release did not emit only key-up: %+v", msgs)
	}
	out.Reset()
	m.Update(tea.KeyReleaseMsg{Code: '[', Text: "["})
	if out.Len() != 0 {
		t.Fatal("duplicate release emitted another key-up")
	}
}

func TestCharmV2KeyRepeatState(t *testing.T) {
	m := newWorkbenchModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p"})
	for _, key := range []tea.KeyPressMsg{
		{Code: 'x', Text: "x"},
		{Code: 'x', Text: "x", IsRepeat: true},
	} {
		m.Update(key)
	}
	var phases []string
	for _, msg := range envelopes(t, out) {
		if msg.KeyState != nil {
			phases = append(phases, string(msg.KeyState.Phase))
		}
	}
	if strings.Join(phases, ",") != "down,repeat" {
		t.Fatalf("key phases = %q", phases)
	}
}

func TestCharmV2DockAndPaletteKeys(t *testing.T) {
	m := newWorkbenchModel()
	m.recomputeLayout()
	m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if m.showSidePane {
		t.Fatal("side-panel toggle failed")
	}
	m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if m.showSidebar || !m.showSidePane {
		t.Fatal("dock cycle failed")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !m.showPalette {
		t.Fatal("palette shortcut failed")
	}
	for _, key := range []tea.KeyPressMsg{
		{Code: 'a', Text: "a"},
		{Code: tea.KeySpace, Text: " "},
		{Code: 'b', Text: "b"},
	} {
		m.Update(key)
	}
	if m.paletteQuery != "a b" {
		t.Fatalf("palette query = %q, want space preserved", m.paletteQuery)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.showPalette || m.paletteQuery != "" {
		t.Fatal("escape did not close/reset palette")
	}
}

func TestCharmV2ModalKeyRouting(t *testing.T) {
	m := newWorkbenchModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceModal}})
	m.openModal("p", "modal")
	m.recomputeLayout()
	out.Reset()
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	var keys []*slotKey
	for _, msg := range envelopes(t, out) {
		if msg.SlotKey != nil {
			keys = append(keys, msg.SlotKey)
		}
	}
	if len(keys) != 1 || keys[0].Slot != protocol.SurfaceModal || keys[0].Key != " " {
		t.Fatalf("modal key routing = %+v", keys)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modalOwner().Open() {
		t.Fatal("escape did not close modal")
	}
}

func TestCharmV2MouseResizeLifecycle(t *testing.T) {
	m := newWorkbenchModel()
	addTestPlugin(m, "p", map[protocol.SurfaceID]frame.Spec{protocol.SurfaceSidebarList: {Border: true}})
	m.catalog.Occupants[protocol.SurfaceSidebarList] = "p"
	m.recomputeLayout()
	r := m.slotRects[protocol.SurfaceSidebarList]
	x, y := r.X+r.Width-1, r.Y+2
	m.Update(tea.MouseMotionMsg{X: x, Y: y})
	if m.hoverDivider != "sidebar-width" {
		t.Fatal("divider hover lost")
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.activeDrag != "sidebar-width" {
		t.Fatal("divider click did not start drag")
	}
	before := m.sidebarWidth
	m.Update(tea.MouseMotionMsg{X: x + 4, Y: y, Button: tea.MouseLeft})
	if m.sidebarWidth != before+4 || m.slotRects[protocol.SurfaceSidebarList].Width != before+4 {
		t.Fatal("motion did not resize/recompute framed sidebar")
	}
	m.Update(tea.MouseReleaseMsg{X: x + 4, Y: y, Button: tea.MouseLeft})
	if m.activeDrag != "" {
		t.Fatal("release did not stop drag")
	}
	m.Update(tea.MouseMotionMsg{X: x + 8, Y: y})
	if m.sidebarWidth != before+4 {
		t.Fatal("hover continued resizing after release")
	}
}

func TestPalettePasteInsertsTextWithoutBrackets(t *testing.T) {
	for _, tc := range []struct {
		name, paste, want string
	}{
		{"ascii word", "deploy", "deploy"},
		{"ascii single", "q", "q"},
		{"ascii with spaces", "open the file", "open the file"},
		{"ascii bracketed", "[x]", "[x]"},
		{"lone bracket", "[", "["},
		{"accented", "café", "café"},
		{"cjk", "日本語", "日本語"},
		{"emoji", "👍🏽", "👍🏽"},
		{"multiline ascii", "line one\nline two", "line one line two"},
		{"multiline non-ascii crlf", "café\r\nx", "café x"},
		{"tab", "a\tb", "a b"},
		{"control chars dropped", "a\x1bb\x00c", "abc"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel()
			m.showPalette = true
			_, cmd := m.Update(tea.PasteMsg{Content: tc.paste})
			if cmd != nil {
				t.Fatal("paste returned a command")
			}
			if m.paletteQuery != tc.want {
				t.Fatalf("palette query after paste %q = %q, want %q", tc.paste, m.paletteQuery, tc.want)
			}
			if !m.showPalette || !m.showSidebar {
				t.Fatal("paste closed the palette or ran a dock shortcut")
			}
		})
	}
}

func TestPalettePasteAppendsToTypedTextAndResetsCursor(t *testing.T) {
	m := newWorkbenchModel()
	m.showPalette = true
	m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m.paletteCursor = 2
	m.Update(tea.PasteMsg{Content: "bc"})
	m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.paletteQuery != "abcd" {
		t.Fatalf("query = %q, want abcd", m.paletteQuery)
	}
	if m.paletteCursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.paletteCursor)
	}
}

func TestPaletteTypedSingleCharsStillWork(t *testing.T) {
	m := newWorkbenchModel()
	m.showPalette = true
	for _, r := range "q[é" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.paletteQuery != "q[é" {
		t.Fatalf("query = %q, want q[é", m.paletteQuery)
	}
}

func TestPaletteKeepsBracketedExtendedKeyText(t *testing.T) {
	m := newWorkbenchModel()
	m.showPalette = true
	m.Update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "[é]"})
	if m.paletteQuery != "[é]" {
		t.Fatalf("query = %q, want [é] (IME text keeps its brackets)", m.paletteQuery)
	}
}
