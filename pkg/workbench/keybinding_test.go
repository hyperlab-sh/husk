package workbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type keyRoute struct {
	slotKeys []slotKey
	invokes  []string
}

func routed(t *testing.T, buf *pluginWire) keyRoute {
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

func press(m *workbenchModel, key string) {
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

func resetAll(bufs ...*pluginWire) {
	for _, b := range bufs {
		b.Reset()
	}
}

// With zero plugins only Home and built-in workbench commands exist: the old
// application shortcuts (F2-F4, ctrl+f/g/e, US-layout Option characters) do
// nothing, and the rail holds only host buttons.
func TestZeroPluginsHaveNoApplicationShortcuts(t *testing.T) {
	m := newModel()
	m.recomputeLayout()
	for _, key := range []string{"f2", "f3", "f4", "ctrl+f", "ctrl+g", "ctrl+e", "¡", "™", "£", "¢", "π", "√"} {
		press(m, key)
		if m.activeRailID != "" || m.activePlacement != "" || m.modalOwner().Open() || m.showPalette || m.stageMode != "plugin" {
			t.Fatalf("%q changed workbench state: rail=%q placement=%q modal=%q palette=%v stage=%q",
				key, m.activeRailID, m.activePlacement, m.modalOwner().RailID, m.showPalette, m.stageMode)
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
	m := newModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
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
		if r.slotKeys[i].Key != k || r.slotKeys[i].Slot != protocol.SurfaceSidebarList {
			t.Fatalf("key %d = %+v, want %q on sidebar-list", i, r.slotKeys[i], k)
		}
	}
}

// Rail buttons follow registration order; no item ID is preferred.
func TestRailOrderIsRegistrationOrder(t *testing.T) {
	m := newModel()
	addPublicationTestPlugin(m, hello{PluginID: "z", RailItems: []contracts.RailItem{
		{ID: "z.second", Label: "Z2", Placement: contracts.PlacementStage},
		{ID: "rail.project-git", Label: "Z1", Placement: contracts.PlacementModal},
	}})
	addPublicationTestPlugin(m, hello{PluginID: "a", RailItems: []contracts.RailItem{
		{ID: "rail.tv-search", Label: "A", Placement: contracts.PlacementModal},
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
	if ri, ok := m.firstRailItemWithPlacement(contracts.PlacementModal); !ok || ri.ID != "rail.project-git" {
		t.Fatalf("first modal item = %+v %v", ri, ok)
	}
	if _, ok := m.firstRailItemWithPlacement(contracts.PlacementFullCanvas); ok {
		t.Fatal("found a full-canvas item that nobody declared")
	}
}

// A rail item's declared shortcut opens it and, pressed again, closes it.
func TestRailItemShortcutTogglesItem(t *testing.T) {
	m := newModel()
	_, side := addPublicationTestPlugin(m, hello{PluginID: "side", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	_, stage := addPublicationTestPlugin(m, hello{PluginID: "st", Slots: []protocol.SurfaceID{protocol.SurfaceWorkspaceStage},
		RailItems: []contracts.RailItem{{ID: "st.open", Label: "Stage", Placement: contracts.PlacementStage}}, Binds: []bind{{Key: "f4", Action: "st.open"}}})
	m.recomputeLayout()
	resetAll(side, stage)

	press(m, "f4")
	if m.activeRailID != "st.open" || m.activePlacement != contracts.PlacementStage || m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Fatalf("f4 did not open stage: rail=%q placement=%q focus=%q", m.activeRailID, m.activePlacement, m.focusedSlot)
	}
	if r := routed(t, side); len(r.slotKeys) != 0 {
		t.Fatalf("shortcut leaked to sidebar plugin: %+v", r)
	}

	press(m, "f4")
	if m.activeRailID != "" || m.activePlacement != "" || m.focusedSlot != protocol.SurfaceSidebarList {
		t.Fatal("second f4 did not return home")
	}
}

// Inside a modal, esc and the modal item's own shortcut close it; every other
// key, including the old hard-coded f2/ctrl+f, goes to the modal plugin.
func TestModalClosesOnOwnShortcutOnly(t *testing.T) {
	m := newModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "mod",
		RailItems: []contracts.RailItem{{ID: "mod.find", Label: "Find", Placement: contracts.PlacementModal}}, Binds: []bind{{Key: "ctrl+t", Action: "mod.find"}}})
	m.recomputeLayout()
	press(m, "ctrl+t")
	if m.modalOwner().RailID != "mod.find" || m.modalOwner().PluginID != "mod" {
		t.Fatal("ctrl+t did not open the modal")
	}
	out.Reset()
	press(m, "f2")
	press(m, "ctrl+f")
	if m.modalOwner().RailID != "mod.find" {
		t.Fatal("f2/ctrl+f closed a modal that does not use them")
	}
	r := routed(t, out)
	if len(r.slotKeys) != 2 || r.slotKeys[0].Key != "f2" || r.slotKeys[1].Key != "ctrl+f" || r.slotKeys[0].Slot != protocol.SurfaceModal {
		t.Fatalf("modal routing = %+v", r)
	}
	press(m, "ctrl+t")
	if m.modalOwner().Open() {
		t.Fatal("modal shortcut did not close the modal")
	}
}

// A bound key becomes an action invocation instead of a surface key; unbound keys still reach the focused surface.
func TestKeybindingInvokesDeclaringPlugin(t *testing.T) {
	m := newModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList},
		Binds: []bind{{Key: "n", Action: "a.new"}, {Key: "f5", Action: "a.refresh"}}})
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

// Bindings may target host commands and rail items; the host serves them
// itself, so nothing is delivered to a plugin as an invocation. (Which binding
// wins a conflicting key is the keybinding service's rule, tested there.)
func TestKeybindingHostCommandsAndRailItems(t *testing.T) {
	m := newModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList},
		RailItems: []contracts.RailItem{{ID: "a.dash", Label: "Dash", Placement: contracts.PlacementFullCanvas}},
		Binds: []bind{
			{Key: "S", Action: "husk.sidepanel.toggle"},
			{Key: "D", Action: "a.dash"},
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
	if m.activeRailID != "a.dash" || m.activePlacement != contracts.PlacementFullCanvas {
		t.Fatal("binding to a rail item ID did not activate it")
	}
	r := routed(t, out)
	if len(r.invokes) != 0 {
		t.Fatalf("host-run bindings leaked as invocations: %v", r.invokes)
	}
}

// Stage placement routes every key to the focused surface: the stage plugin
// until tab moves focus to the sidebar. No key is diverted to the sidebar.
func TestStageRoutesKeysToFocusedSurfaceOnly(t *testing.T) {
	m := newModel()
	_, side := addPublicationTestPlugin(m, hello{PluginID: "side", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	_, stage := addPublicationTestPlugin(m, hello{PluginID: "st", Slots: []protocol.SurfaceID{protocol.SurfaceWorkspaceStage},
		RailItems: []contracts.RailItem{{ID: "st.open", Label: "Stage", Placement: contracts.PlacementStage}}})
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
		if rs.slotKeys[i].Key != k || rs.slotKeys[i].Slot != protocol.SurfaceWorkspaceStage {
			t.Fatalf("stage key %d = %+v", i, rs.slotKeys[i])
		}
	}

	resetAll(side, stage)
	press(m, "tab")
	press(m, "j")
	rs, rside = routed(t, stage), routed(t, side)
	if len(rside.slotKeys) != 1 || rside.slotKeys[0].Key != "j" || rside.slotKeys[0].Slot != protocol.SurfaceSidebarList || len(rs.slotKeys) != 0 {
		t.Fatalf("after tab: side=%+v stage=%+v", rside.slotKeys, rs.slotKeys)
	}
}

// A key for an unoccupied focused surface is dropped, not handed to whoever
// owns the sidebar list.
func TestUnoccupiedFocusDropsKeys(t *testing.T) {
	m := newModel()
	_, out := addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	m.focusedSlot = protocol.SurfaceMainStage
	out.Reset()
	press(m, "x")
	if r := routed(t, out); len(r.slotKeys)+len(r.invokes) != 0 {
		t.Fatalf("key for unoccupied main-stage reached sidebar owner: %+v", r)
	}
}

// A full-canvas rail item whose plugin did not claim the surface in hello
// still receives the keys for the canvas it opened.
func TestFullCanvasKeysReachRailItemPlugin(t *testing.T) {
	m := newModel()
	_, side := addPublicationTestPlugin(m, hello{PluginID: "side", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	_, dash := addPublicationTestPlugin(m, hello{PluginID: "dash",
		RailItems: []contracts.RailItem{{ID: "dash.open", Label: "Dash", Placement: contracts.PlacementFullCanvas}}})
	m.recomputeLayout()
	m.executeCommand("dash.open")
	resetAll(side, dash)
	press(m, "j")
	rd, rside := routed(t, dash), routed(t, side)
	if len(rd.slotKeys) != 1 || rd.slotKeys[0].Slot != protocol.SurfaceFullCanvas || len(rside.slotKeys) != 0 {
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
	commandFiles, err := filepath.Glob(filepath.Join("..", "..", "cmd", "husk", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, commandFiles...)
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
