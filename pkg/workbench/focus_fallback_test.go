package workbench

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// sidesModelPanelGone hides the panel dock while it has focus, pushing focus
// to the stage and remembering the panel.
func sidesModelPanelGone(t *testing.T) *workbenchModel {
	t.Helper()
	m := goldenModel()
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
	hostPlugin(m, "p.panel", protocol.SurfaceSidePanel)
	m.focusedSlot = protocol.SurfaceSidePanel
	press(m, "]")
	if m.focusedSlot != protocol.SurfaceMainStage || m.focusFallback != protocol.SurfaceSidePanel {
		t.Fatalf("setup: focus %q fallback %q", m.focusedSlot, m.focusFallback)
	}
	return m
}

// Tab is the user moving focus: a column that returns afterwards must not
// take it back.
func TestTabClearsFocusFallback(t *testing.T) {
	m := sidesModelPanelGone(t)
	press(m, "tab") // main stage -> sidebar list (the panel is absent)
	press(m, "tab") // and back to the main stage, where a stale fallback bites
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatalf("tabs left focus on %q", m.focusedSlot)
	}
	press(m, "]")
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Errorf("returning panel stole focus: now %q", m.focusedSlot)
	}
}

// Clicking a surface is the user moving focus, even onto the stage that
// focus already sits on.
func TestClickClearsFocusFallback(t *testing.T) {
	m := sidesModelPanelGone(t)
	r := m.slotRects[protocol.SurfaceMainStage]
	m.Update(tea.MouseClickMsg{X: r.InnerX + 1, Y: r.InnerY + 1, Button: tea.MouseLeft})
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatalf("click left focus on %q", m.focusedSlot)
	}
	press(m, "]")
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Errorf("returning panel stole focus: now %q", m.focusedSlot)
	}
}

// Tab skips slots that are not on screen.
func TestTabSkipsAbsentSlots(t *testing.T) {
	m := goldenModel()
	m.showSidePane = false
	m.recomputeLayout()
	if _, shown := m.slotRects[protocol.SurfaceSidePanel]; shown {
		t.Fatal("setup: the side panel is shown")
	}
	var got []protocol.SurfaceID
	for range 4 {
		press(m, "tab")
		got = append(got, m.focusedSlot)
	}
	want := []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceSidebarList, protocol.SurfaceMainStage, protocol.SurfaceSidebarList}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tab order = %v, want %v", got, want)
		}
	}
}

// Under a stage placement the centre is the workspace stage, not the main
// stage, and the vanished sidebar's focus goes there and comes back.
func TestStagePlacementFocusFallback(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
	hostPlugin(m, "p.stage", protocol.SurfaceWorkspaceStage)
	m.activePlacement = contracts.PlacementStage
	m.showSidebar = true
	m.recomputeLayout()
	m.focusedSlot = protocol.SurfaceSidebarList
	press(m, "[")
	if m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Fatalf("focus = %q after the sidebar vanished, want the workspace stage", m.focusedSlot)
	}
	press(m, "[")
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Errorf("focus = %q after the sidebar returned, want the sidebar list", m.focusedSlot)
	}
}

// panelGoneThen runs a user focus change after the panel left, brings the
// panel back, and expects focus unchanged.
func TestUserFocusChangesDropFallback(t *testing.T) {
	for name, move := range map[string]func(*workbenchModel){
		// husk.home declines on the home layout, so give it somewhere to go.
		"home":         func(m *workbenchModel) { m.stageMode = "hotspots"; m.run("husk.home", nil) },
		"toggle stage": func(m *workbenchModel) { m.run("husk.hotspots.toggle_stage", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			m := sidesModelPanelGone(t)
			move(m)
			want := m.focusedSlot
			press(m, "]")
			if m.focusedSlot != want {
				t.Errorf("returning panel moved focus %q -> %q", want, m.focusedSlot)
			}
		})
	}
}

func stageRailItem(m *workbenchModel) contracts.RailItem {
	ri := contracts.RailItem{Plugin: "p.stage", ID: "p.stage.open", Placement: contracts.PlacementStage}
	withFE(m).Rails = append(withFE(m).Rails, ri)
	return ri
}

// Opening a stage rail item is a user focus move, and under that placement
// the side panel is not drawn: the returning panel must not take focus.
func TestStageRailItemThenReturningPanelKeepsFocus(t *testing.T) {
	m := sidesModelPanelGone(t)
	hostPlugin(m, "p.stage", protocol.SurfaceWorkspaceStage)
	m.activateRailItem(stageRailItem(m))
	if m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Fatalf("setup: focus %q", m.focusedSlot)
	}
	m.showSidePane = true
	m.recomputeLayout()
	if m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Errorf("focus = %q, want the workspace stage", m.focusedSlot)
	}
}

// Even with the fallback still remembered, a stage placement never restores
// focus to the undrawn side panel.
func TestStagePlacementNeverRestoresSidePanel(t *testing.T) {
	m := sidesModelPanelGone(t)
	hostPlugin(m, "p.stage", protocol.SurfaceWorkspaceStage)
	m.activePlacement = contracts.PlacementStage
	m.focusedSlot = protocol.SurfaceWorkspaceStage // not via setFocus: fallback stays
	m.showSidePane = true
	m.recomputeLayout()
	if m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Errorf("focus = %q, want the workspace stage", m.focusedSlot)
	}
}

// narrowPanelWidth is a terminal width at which the side panel is shown but
// not drawn: the centre column beside the sidebar is one cell short of
// minPanelCentreWidth.
func narrowPanelWidth(m *workbenchModel) int {
	return activityRailWidth + m.sidebarWidth + minPanelCentreWidth - 1
}

// On a terminal too narrow to draw the side panel, a returning panel plugin
// must not take focus either.
func TestNarrowTerminalDoesNotRestoreUndrawnPanel(t *testing.T) {
	m := sidesModelPanelGone(t)
	m.width = narrowPanelWidth(m)
	m.showSidePane = true
	m.recomputeLayout()
	if _, drawn := m.slotRects[protocol.SurfaceSidePanel]; drawn {
		t.Fatalf("setup: the side panel is drawn at width %d", m.width)
	}
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Errorf("focus = %q, want the main stage", m.focusedSlot)
	}
}

// Focusing the side panel and then narrowing the terminal until it is not
// drawn moves focus to the stage; widening again gives it back.
func TestNarrowingTerminalMovesFocusOffDrawnPanel(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
	hostPlugin(m, "p.panel", protocol.SurfaceSidePanel)
	m.width = narrowPanelWidth(m) + 40
	m.recomputeLayout()
	if _, drawn := m.slotRects[protocol.SurfaceSidePanel]; !drawn {
		t.Fatalf("setup: the side panel is not drawn at width %d", m.width)
	}
	wide := m.width
	m.focusedSlot = protocol.SurfaceSidePanel
	m.width = narrowPanelWidth(m)
	m.recomputeLayout()
	if _, drawn := m.slotRects[protocol.SurfaceSidePanel]; drawn {
		t.Fatalf("setup: the side panel is drawn at width %d", m.width)
	}
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatalf("focus = %q on an undrawn panel, want the main stage", m.focusedSlot)
	}
	m.width = wide
	m.recomputeLayout()
	if m.focusedSlot != protocol.SurfaceSidePanel {
		t.Errorf("focus = %q after widening, want the side panel back", m.focusedSlot)
	}
}

// Tab with focus outside the cycle (sidebar footer) under a stage placement
// still moves focus.
func TestTabFromSidebarFooterUnderStagePlacement(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
	hostPlugin(m, "p.stage", protocol.SurfaceWorkspaceStage)
	m.activePlacement = contracts.PlacementStage
	m.showSidebar = true
	m.recomputeLayout()
	m.focusedSlot = protocol.SurfaceSidebarFooter
	press(m, "tab")
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Errorf("focus = %q after tab, want the sidebar list", m.focusedSlot)
	}
}

// A click on a published-slot contribution is a user focus move: the
// returning panel must not take focus afterwards. The side panel plugin is the
// one that leaves, so the stage keeps its own occupant.
func TestContributionClickClearsFocusFallback(t *testing.T) {
	m := newModel()
	addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}, PublishedSlots: []slotDecl{{ID: "p.actions", Kind: "inline"}}})
	addPublicationTestPlugin(m, hello{PluginID: "x"})
	contribute(m, "p.actions", contribution{PluginID: "x", ID: "x.button", Content: "[Go]", OnClick: "x.click"})
	m.content("p", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{"", anchor("p.actions", "row")}})
	m.focusedSlot, m.focusFallback = protocol.SurfaceMainStage, protocol.SurfaceSidePanel
	r := m.slotRects[protocol.SurfaceMainStage]
	if !m.clickContributionAt(r.InnerX+1, r.InnerY+1) {
		t.Fatal("setup: the click hit no contribution")
	}
	if m.focusFallback != "" {
		t.Errorf("fallback %q survived a contribution click", m.focusFallback)
	}
}
