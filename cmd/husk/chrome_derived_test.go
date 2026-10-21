package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func hintKeys(m *WorkbenchModel) []string {
	var out []string
	for _, h := range m.footerHints() {
		out = append(out, h.key)
	}
	return out
}

// With no plugins connected the chrome advertises only host features: the rail
// is Home, the slot lab and the palette, the palette lists only workbench
// commands, and the footer has no navigation keys of any application.
func TestZeroPluginsChromeIsHostOnly(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.width, m.height = 160, 40
	m.recomputeLayout()

	var kinds []string
	for _, b := range m.railButtons() {
		kinds = append(kinds, b.kind)
		if b.kind == "plugin" {
			t.Fatalf("plugin rail button with zero plugins: %+v", b)
		}
	}
	if got, want := strings.Join(kinds, ","), "home,hotspots,palette"; got != want {
		t.Fatalf("rail kinds = %s, want %s", got, want)
	}

	for _, c := range m.allCommands() {
		if c.PluginID != "husk.core" || !strings.HasPrefix(c.ID, "husk.") {
			t.Errorf("non-host command with zero plugins: %+v", c)
		}
		for _, banned := range []string{"bb", "Herdr", "tv.", "fleet", "F2", "F3", "F4"} {
			if strings.Contains(c.Title, banned) || strings.Contains(c.Description, banned) {
				t.Errorf("command %q mentions %q", c.ID, banned)
			}
		}
	}

	if got, want := strings.Join(hintKeys(m), "|"), "F1|drag/= |[ ] /|ctrl+p|q"; got != want {
		t.Fatalf("footer keys = %q, want %q", got, want)
	}
	bar := ansi.Strip(m.renderBottomKeybindBar())
	for _, banned := range []string{"j/k", "focus", "nav"} {
		if strings.Contains(bar, banned) {
			t.Errorf("footer %q advertises %q with zero plugins", bar, banned)
		}
	}
}

// Plugin-declared keybindings that carry a description appear in the footer
// when that plugin owns the focused surface, and vanish on disconnect.
func TestFooterHintsFollowFocusedPluginBindings(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.width, m.height = 200, 40
	cp, _ := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "side",
		Keybindings: []huskipc.KeybindSpec{
			{Key: "n", CommandID: "side.new", Description: "new item"},
			{Key: "ctrl+r", CommandID: "side.refresh", Description: "refresh"},
			{Key: "x", CommandID: "side.silent"}, // no description: not advertised
			{Key: "n", CommandID: "side.dup", Description: "duplicate key"},
			{Key: "z", CommandID: "side.other", Description: "other slot", WhenSlot: huskipc.SlotSidePanel},
		}})
	m.registry.Occupants[huskipc.SlotSidebarList] = "side"
	m.focusedSlot = huskipc.SlotSidebarList

	if got, want := strings.Join(hintKeys(m), "|"), "F1|n|Ctrl+r|drag/= |[ ] /|ctrl+p|q"; got != want {
		t.Fatalf("footer keys = %q, want %q", got, want)
	}
	bar := ansi.Strip(m.renderBottomKeybindBar())
	if !strings.Contains(bar, "n new item") || !strings.Contains(bar, "Ctrl+r refresh") {
		t.Fatalf("footer missing plugin hints: %q", bar)
	}
	if strings.Contains(bar, "duplicate key") || strings.Contains(bar, "other slot") {
		t.Fatalf("footer shows shadowed or wrong-slot binding: %q", bar)
	}

	m.focusedSlot = huskipc.SlotSidePanel // nobody owns it
	if strings.Contains(strings.Join(hintKeys(m), "|"), "Ctrl+r") {
		t.Fatal("hints leaked to a surface the plugin does not own")
	}

	m.focusedSlot = huskipc.SlotSidebarList
	m.Update(pluginDisconnectedMsg{plugin: cp})
	if strings.Contains(strings.Join(hintKeys(m), "|"), "Ctrl+r") {
		t.Fatal("hints survived plugin disconnect")
	}
}

// A plugin cannot crowd the footer: at most maxPluginFooterHints are shown.
func TestFooterHintsCapPluginBindings(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var kb []huskipc.KeybindSpec
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		kb = append(kb, huskipc.KeybindSpec{Key: k, CommandID: "p." + k, Description: "do " + k})
	}
	addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Keybindings: kb})
	m.registry.Occupants[huskipc.SlotSidebarList] = "p"
	m.focusedSlot = huskipc.SlotSidebarList
	if got, want := len(m.footerHints()), 1+maxPluginFooterHints+4; got != want {
		t.Fatalf("hint count = %d, want %d", got, want)
	}
}

// The slot map's j/k hint appears only while it is the focused stage.
func TestFooterHintsSlotMapNav(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.stageMode = "hotspots"
	m.focusedSlot = huskipc.SlotMainStage
	if !strings.Contains(strings.Join(hintKeys(m), "|"), "j/k") {
		t.Fatal("slot map focused but no j/k hint")
	}
	m.focusedSlot = huskipc.SlotSidebarList
	if strings.Contains(strings.Join(hintKeys(m), "|"), "j/k") {
		t.Fatal("j/k hint shown while slot map is not focused")
	}
}
