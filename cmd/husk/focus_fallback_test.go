package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// sidesModelPanelGone has a sidebar list and a side panel from two plugins, with focus
// on the panel; the panel plugin then leaves, pushing focus to the stage and
// remembering the panel.
func sidesModelPanelGone(t *testing.T) *WorkbenchModel {
	t.Helper()
	m := goldenModel()
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
	hostPlugin(m, "p.panel", protocol.SurfaceSidePanel)
	m.focusedSlot = protocol.SurfaceSidePanel
	hostLeave(m, "p.panel")
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
	hostPlugin(m, "p.panel", protocol.SurfaceSidePanel)
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
	hostPlugin(m, "p.panel", protocol.SurfaceSidePanel)
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Errorf("returning panel stole focus: now %q", m.focusedSlot)
	}
}

// Tab skips slots that are not on screen.
func TestTabSkipsAbsentSlots(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
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
	hostLeave(m, "p.list")
	if m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Fatalf("focus = %q after the sidebar vanished, want the workspace stage", m.focusedSlot)
	}
	hostPlugin(m, "p.list", protocol.SurfaceSidebarList)
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Errorf("focus = %q after the sidebar returned, want the sidebar list", m.focusedSlot)
	}
}
