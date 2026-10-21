package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func TestCharmV2ViewDeclaresTerminalModes(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
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
			m := newWorkbenchModel("", nil, t.TempDir())
			_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
			out.Reset()
			m.Update(tc.key)
			var keys []string
			for _, msg := range envelopes(t, out) {
				if msg.SlotKey != nil {
					if msg.SlotKey.Slot != string(huskipc.SlotSidebarList) {
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
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
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
	if !m.showSidebar || !m.showPalette || m.paletteQuery != "" {
		t.Fatal("paste ran a dock shortcut or changed palette input")
	}
}

func TestCharmV2KeyReleaseDoesNotRepeatShortcut(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p"})
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
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p"})
	for _, key := range []tea.KeyPressMsg{
		{Code: 'x', Text: "x"},
		{Code: 'x', Text: "x", IsRepeat: true},
	} {
		m.Update(key)
	}
	var phases []string
	for _, msg := range envelopes(t, out) {
		if msg.KeyState != nil {
			phases = append(phases, msg.KeyState.Phase)
		}
	}
	if strings.Join(phases, ",") != "down,repeat" {
		t.Fatalf("key phases = %q", phases)
	}
}

func TestCharmV2DockAndPaletteKeys(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
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
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotModalSurface}})
	m.activeModalPlugin, m.activeModalRailID = "p", "modal"
	m.recomputeLayout()
	out.Reset()
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	var keys []*huskipc.SlotKeyPayload
	for _, msg := range envelopes(t, out) {
		if msg.SlotKey != nil {
			keys = append(keys, msg.SlotKey)
		}
	}
	if len(keys) != 1 || keys[0].Slot != string(huskipc.SlotModalSurface) || keys[0].Key != " " {
		t.Fatalf("modal key routing = %+v", keys)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.activeModalPlugin != "" || m.activeModalRailID != "" {
		t.Fatal("escape did not close modal")
	}
}

func TestCharmV2MouseResizeLifecycle(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	addTestPlugin(m, "p", map[huskipc.SlotID]frame.Spec{huskipc.SlotSidebarList: {Border: true}})
	m.slotOwners[huskipc.SlotSidebarList] = "p"
	m.recomputeLayout()
	r := m.slotRects[huskipc.SlotSidebarList]
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
	if m.sidebarWidth != before+4 || m.slotRects[huskipc.SlotSidebarList].Width != before+4 {
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
