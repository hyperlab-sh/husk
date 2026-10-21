package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

type keyRoute struct {
	slotKeys []huskipc.SlotKeyPayload
	invokes  []string
}

func routed(t *testing.T, buf *bytes.Buffer) keyRoute {
	t.Helper()
	var r keyRoute
	for _, msg := range envelopes(t, buf) {
		if msg.SlotKey != nil {
			r.slotKeys = append(r.slotKeys, *msg.SlotKey)
		}
		if msg.CommandInvoke != nil {
			r.invokes = append(r.invokes, msg.CommandInvoke.CommandID)
		}
	}
	return r
}

func press(m *WorkbenchModel, key string) {
	switch key {
	case "f1", "f2", "f3", "f4":
		m.Update(tea.KeyPressMsg{Code: tea.KeyF1 + rune(key[1]-'1')})
	case "enter":
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "tab":
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	case "esc":
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	default:
		if after, ok := strings.CutPrefix(key, "ctrl+"); ok {
			m.Update(tea.KeyPressMsg{Code: rune(after[0]), Mod: tea.ModCtrl})
			return
		}
		r := []rune(key)
		m.Update(tea.KeyPressMsg{Code: r[0], Text: key})
	}
}

func resetAll(bufs ...*bytes.Buffer) {
	for _, b := range bufs {
		b.Reset()
	}
}

// With zero plugins only Home and built-in workbench commands exist: the old
// application shortcuts (F2-F4, ctrl+f/g/e, US-layout Option characters) do
// nothing, and the rail holds only host buttons.
func TestZeroPluginsHaveNoApplicationShortcuts(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.recomputeLayout()
	for _, key := range []string{"f2", "f3", "f4", "ctrl+f", "ctrl+g", "ctrl+e", "¡", "™", "£", "¢", "π", "√"} {
		press(m, key)
		if m.activeRailID != "" || m.activePlacement != "" || m.activeModalRailID != "" || m.showPalette || m.stageMode != "plugin" {
			t.Fatalf("%q changed workbench state: rail=%q placement=%q modal=%q palette=%v stage=%q",
				key, m.activeRailID, m.activePlacement, m.activeModalRailID, m.showPalette, m.stageMode)
		}
	}
	var kinds []string
	for _, b := range m.railButtons() {
		kinds = append(kinds, b.kind)
	}
	if strings.Join(kinds, ",") != "home,hotspots,palette" {
		t.Fatalf("zero-plugin rail = %v", kinds)
	}
	for _, c := range m.allCommands() {
		if !strings.HasPrefix(c.ID, "husk.") || c.PluginID != "husk.core" {
			t.Fatalf("zero-plugin palette has non-host command %+v", c)
		}
	}
}

// Keys that used to be swallowed by host application shortcuts now reach the
// focused surface's plugin unchanged.
func TestFormerApplicationKeysReachFocusedPlugin(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
	out.Reset()
	keys := []string{"f2", "f3", "f4", "ctrl+f", "ctrl+g", "ctrl+e", "™"}
	for _, k := range keys {
		press(m, k)
	}
	r := routed(t, out)
	if len(r.slotKeys) != len(keys) || len(r.invokes) != 0 {
		t.Fatalf("routing = %+v", r)
	}
	for i, k := range keys {
		if r.slotKeys[i].Key != k || r.slotKeys[i].Slot != string(huskipc.SlotSidebarList) {
			t.Fatalf("key %d = %+v, want %q on sidebar-list", i, r.slotKeys[i], k)
		}
	}
}

// Rail buttons follow registration order; no item ID is preferred.
func TestRailOrderIsRegistrationOrder(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "z", RailItems: []huskipc.RailItemSpec{
		{ID: "z.second", Label: "Z2", Placement: huskipc.PlacementStage},
		{ID: "rail.project-git", Label: "Z1", Placement: huskipc.PlacementModal},
	}})
	addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", RailItems: []huskipc.RailItemSpec{
		{ID: "rail.tv-search", Label: "A", Placement: huskipc.PlacementModal},
	}})
	m.recomputeLayout()
	var ids []string
	for _, b := range m.railButtons() {
		if b.kind == "plugin" {
			ids = append(ids, b.item.ID)
		}
	}
	if strings.Join(ids, ",") != "z.second,rail.project-git,rail.tv-search" {
		t.Fatalf("rail order = %v", ids)
	}
	if ri, ok := m.firstRailItemWithPlacement(huskipc.PlacementModal); !ok || ri.ID != "rail.project-git" {
		t.Fatalf("first modal item = %+v %v", ri, ok)
	}
	if _, ok := m.firstRailItemWithPlacement(huskipc.PlacementFullCanvas); ok {
		t.Fatal("found a full-canvas item that nobody declared")
	}
}

// A rail item's declared shortcut opens it and, pressed again, closes it.
func TestRailItemShortcutTogglesItem(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, side := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "side", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
	_, stage := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "st", Slots: []huskipc.SlotID{huskipc.SlotWorkspaceStage},
		RailItems: []huskipc.RailItemSpec{{ID: "st.open", Label: "Stage", Shortcut: "f4", Placement: huskipc.PlacementStage}}})
	m.recomputeLayout()
	resetAll(side, stage)

	press(m, "f4")
	if m.activeRailID != "st.open" || m.activePlacement != huskipc.PlacementStage || m.focusedSlot != huskipc.SlotWorkspaceStage {
		t.Fatalf("f4 did not open stage: rail=%q placement=%q focus=%q", m.activeRailID, m.activePlacement, m.focusedSlot)
	}
	var activated *huskipc.RailActivatedPayload
	for _, msg := range envelopes(t, stage) {
		if msg.RailActivated != nil {
			activated = msg.RailActivated
		}
	}
	if activated == nil || activated.RailID != "st.open" || !activated.Active || activated.Width <= 0 {
		t.Fatalf("rail_activated = %+v", activated)
	}
	if r := routed(t, side); len(r.slotKeys) != 0 {
		t.Fatalf("shortcut leaked to sidebar plugin: %+v", r)
	}

	press(m, "f4")
	if m.activeRailID != "" || m.activePlacement != "" || m.focusedSlot != huskipc.SlotSidebarList {
		t.Fatal("second f4 did not return home")
	}
}

// Inside a modal, esc and the modal item's own shortcut close it; every other
// key, including the old hard-coded f2/ctrl+f, goes to the modal plugin.
func TestModalClosesOnOwnShortcutOnly(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "mod",
		RailItems: []huskipc.RailItemSpec{{ID: "mod.find", Label: "Find", Shortcut: "ctrl+t", Placement: huskipc.PlacementModal}}})
	m.recomputeLayout()
	press(m, "ctrl+t")
	if m.activeModalRailID != "mod.find" || m.activeModalPlugin != "mod" {
		t.Fatal("ctrl+t did not open the modal")
	}
	out.Reset()
	press(m, "f2")
	press(m, "ctrl+f")
	if m.activeModalRailID != "mod.find" {
		t.Fatal("f2/ctrl+f closed a modal that does not use them")
	}
	r := routed(t, out)
	if len(r.slotKeys) != 2 || r.slotKeys[0].Key != "f2" || r.slotKeys[1].Key != "ctrl+f" || r.slotKeys[0].Slot != string(huskipc.SlotModalSurface) {
		t.Fatalf("modal routing = %+v", r)
	}
	press(m, "ctrl+t")
	if m.activeModalRailID != "" || m.activeModalPlugin != "" {
		t.Fatal("modal shortcut did not close the modal")
	}
}

// Declared keybindings become command_invoke to the declaring plugin instead
// of slot_key; unbound keys still reach the focused surface.
func TestKeybindingInvokesDeclaringPlugin(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotSidebarList},
		Keybindings: []huskipc.KeybindSpec{{Key: "n", CommandID: "a.new"}, {Key: "f5", CommandID: "a.refresh"}}})
	out.Reset()
	press(m, "n")
	m.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	press(m, "x")
	r := routed(t, out)
	if strings.Join(r.invokes, ",") != "a.new,a.refresh" {
		t.Fatalf("invokes = %v", r.invokes)
	}
	if len(r.slotKeys) != 1 || r.slotKeys[0].Key != "x" {
		t.Fatalf("slot keys = %+v, want only unbound x", r.slotKeys)
	}
}

// when_slot limits a binding to its surface's focus; a global binding from
// another plugin still fires regardless of focus.
func TestKeybindingWhenSlotAndPrecedence(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, a := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotSidebarList},
		Keybindings: []huskipc.KeybindSpec{{Key: "g", CommandID: "a.global"}}})
	_, b := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "b", Slots: []huskipc.SlotID{huskipc.SlotMainStage},
		Keybindings: []huskipc.KeybindSpec{
			{Key: "r", CommandID: "b.refresh", WhenSlot: huskipc.SlotMainStage},
			{Key: "g", CommandID: "b.global"},
			{Key: "s", CommandID: "b.scoped-sidebar", WhenSlot: huskipc.SlotSidebarList},
			{Key: "w", CommandID: "b.anywhere"},
		}})
	_, c := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "c",
		Keybindings: []huskipc.KeybindSpec{{Key: "s", CommandID: "c.global"}}})
	m.recomputeLayout()
	resetAll(a, b, c)

	// Focus sidebar-list (owned by a).
	press(m, "r") // b's binding is scoped to main-stage: plain key to a
	press(m, "g") // a owns the focused surface: a's global wins over b's
	press(m, "s") // b's binding scoped to sidebar-list beats c's global
	press(m, "w") // b's global binding fires although a has focus
	ra, rb, rc := routed(t, a), routed(t, b), routed(t, c)
	if len(ra.slotKeys) != 1 || ra.slotKeys[0].Key != "r" || strings.Join(ra.invokes, ",") != "a.global" {
		t.Fatalf("a = %+v", ra)
	}
	if strings.Join(rb.invokes, ",") != "b.scoped-sidebar,b.anywhere" || len(rb.slotKeys) != 0 {
		t.Fatalf("b = %+v", rb)
	}
	if len(rc.invokes)+len(rc.slotKeys) != 0 {
		t.Fatalf("c = %+v", rc)
	}

	// Focus main-stage (owned by b).
	resetAll(a, b, c)
	m.focusedSlot = huskipc.SlotMainStage
	press(m, "r")
	press(m, "g")
	press(m, "s") // b's binding is scoped to sidebar-list now: c's global
	ra, rb, rc = routed(t, a), routed(t, b), routed(t, c)
	if strings.Join(rb.invokes, ",") != "b.refresh,b.global" || len(ra.invokes)+len(ra.slotKeys) != 0 {
		t.Fatalf("main-stage focus: a=%+v b=%+v", ra, rb)
	}
	if strings.Join(rc.invokes, ",") != "c.global" {
		t.Fatalf("c = %+v", rc)
	}
}

// Bindings may target host commands and rail items; host shortcuts win over
// plugin bindings for the same key.
func TestKeybindingHostCommandsRailItemsAndReservedKeys(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotSidebarList},
		RailItems: []huskipc.RailItemSpec{{ID: "a.dash", Label: "Dash", Placement: huskipc.PlacementFullCanvas}},
		Keybindings: []huskipc.KeybindSpec{
			{Key: "S", CommandID: "husk.sidepanel.toggle"},
			{Key: "D", CommandID: "a.dash"},
			{Key: "[", CommandID: "a.shadow"},
		}})
	m.recomputeLayout()
	out.Reset()
	press(m, "S")
	if m.showSidePane {
		t.Fatal("binding to husk.sidepanel.toggle did not run it")
	}
	press(m, "[")
	if m.showSidebar {
		t.Fatal("host [ shortcut did not toggle the sidebar")
	}
	press(m, "D")
	if m.activeRailID != "a.dash" || m.activePlacement != huskipc.PlacementFullCanvas {
		t.Fatal("binding to a rail item ID did not activate it")
	}
	r := routed(t, out)
	if len(r.invokes) != 0 {
		t.Fatalf("host-run bindings leaked as command_invoke: %v", r.invokes)
	}
}

// Stage placement routes every key to the focused surface: the stage plugin
// until tab moves focus to the sidebar. No key is diverted to the sidebar.
func TestStageRoutesKeysToFocusedSurfaceOnly(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, side := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "side", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
	_, stage := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "st", Slots: []huskipc.SlotID{huskipc.SlotWorkspaceStage},
		RailItems: []huskipc.RailItemSpec{{ID: "st.open", Label: "Stage", Placement: huskipc.PlacementStage}}})
	m.recomputeLayout()
	m.executeCommand("st.open")
	resetAll(side, stage)

	keys := []string{"j", "k", "enter", "1", "2", "3", "o", "n", "x"}
	for _, k := range keys {
		press(m, k)
	}
	rs, rside := routed(t, stage), routed(t, side)
	if len(rside.slotKeys) != 0 {
		t.Fatalf("stage keys diverted to sidebar plugin: %+v", rside.slotKeys)
	}
	if len(rs.slotKeys) != len(keys) {
		t.Fatalf("stage got %d keys, want %d", len(rs.slotKeys), len(keys))
	}
	for i, k := range keys {
		if rs.slotKeys[i].Key != k || rs.slotKeys[i].Slot != string(huskipc.SlotWorkspaceStage) {
			t.Fatalf("stage key %d = %+v", i, rs.slotKeys[i])
		}
	}

	resetAll(side, stage)
	press(m, "tab")
	press(m, "j")
	rs, rside = routed(t, stage), routed(t, side)
	if len(rside.slotKeys) != 1 || rside.slotKeys[0].Key != "j" || rside.slotKeys[0].Slot != string(huskipc.SlotSidebarList) || len(rs.slotKeys) != 0 {
		t.Fatalf("after tab: side=%+v stage=%+v", rside.slotKeys, rs.slotKeys)
	}
}

// A key for an unoccupied focused surface is dropped, not handed to whoever
// owns the sidebar list.
func TestUnoccupiedFocusDropsKeys(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, out := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
	m.focusedSlot = huskipc.SlotMainStage
	out.Reset()
	press(m, "x")
	if r := routed(t, out); len(r.slotKeys)+len(r.invokes) != 0 {
		t.Fatalf("key for unoccupied main-stage reached sidebar owner: %+v", r)
	}
}

// A full-canvas rail item whose plugin did not claim the surface in hello
// still receives the keys for the canvas it opened.
func TestFullCanvasKeysReachRailItemPlugin(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, side := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "side", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
	_, dash := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "dash",
		RailItems: []huskipc.RailItemSpec{{ID: "dash.open", Label: "Dash", Placement: huskipc.PlacementFullCanvas}}})
	m.recomputeLayout()
	m.executeCommand("dash.open")
	resetAll(side, dash)
	press(m, "j")
	rd, rside := routed(t, dash), routed(t, side)
	if len(rd.slotKeys) != 1 || rd.slotKeys[0].Slot != string(huskipc.SlotFullCanvas) || len(rside.slotKeys) != 0 {
		t.Fatalf("dash=%+v side=%+v", rd.slotKeys, rside.slotKeys)
	}
}

// Guard: the host source names no application command, rail item or
// keyboard-layout-specific character.
func TestHostSourceHasNoApplicationIdentifiers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{`"tv.`, `"fleet.`, `"gitstage.`, `"rail.tv-search"`, `"rail.fleet-dashboard"`, `"rail.project-git"`, "¡", "™", "£", "¢", "π", "√", "BBHotspot"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range banned {
			if strings.Contains(string(data), b) {
				t.Fatalf("%s contains %q", f, b)
			}
		}
	}
}
