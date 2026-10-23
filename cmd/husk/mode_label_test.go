package main

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// topBarText is the top bar with styling stripped.
func topBarText(m *WorkbenchModel) string { return ansi.Strip(m.renderTopWorkbenchBar()) }

func TestLayoutLabelTable(t *testing.T) {
	for _, tc := range []struct {
		placement          contracts.RailPlacement
		left, right, modal bool
		want               string
	}{
		{"", false, false, false, "Workbench"}, {"", true, false, false, "Workbench · Left"},
		{"", false, true, false, "Workbench · Right"}, {"", true, true, false, "Workbench · Left + Right"},
		{contracts.PlacementStage, true, true, false, "Stage · Left"}, {contracts.PlacementStage, false, true, false, "Stage"},
		{contracts.PlacementFullCanvas, true, true, false, "Full canvas"}, {"", true, true, true, "Modal"},
		{contracts.PlacementFullCanvas, true, true, true, "Modal"}, {contracts.PlacementStage, true, true, true, "Modal"},
	} {
		t.Run(tc.want+string(tc.placement), func(t *testing.T) {
			m := goldenModel()
			m.activePlacement = tc.placement
			m.showSidebar = tc.left
			m.showSidePane = tc.right
			if tc.modal {
				m.openModal("p", "p.open")
			}
			m.recomputeLayout()
			if got := m.layoutLabel(); got != tc.want {
				t.Fatalf("label %q, want %q", got, tc.want)
			}
			if !strings.Contains(topBarText(m), " "+tc.want+" ") {
				t.Fatal("label not drawn in top bar")
			}
		})
	}
}

func TestHomeCommandTitleHasNoColumnCount(t *testing.T) {
	for _, c := range goldenModel().builtinCommands() {
		if c.ID == "husk.home" {
			if c.Title != "Activity Rail (🏠 F1): Return to Default Workbench Layout" {
				t.Errorf("unexpected home title %q", c.Title)
			}
			return
		}
	}
	t.Fatal("husk.home command missing")
}

func TestZeroPluginsLayOutEveryRegionSlot(t *testing.T) {
	m := goldenModel()
	want := []protocol.SurfaceID{protocol.SurfaceActivityRail, protocol.SurfaceMainHeader, protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceSidebarFooter, protocol.SurfaceSidebarList, protocol.SurfaceSidebarNavigation}
	if got := m.surfaceView().Visible; !slices.Equal(got, want) {
		t.Fatalf("visible %v, want %v", got, want)
	}
	if len(m.slotRects) != len(want) {
		t.Fatalf("rects %v", m.slotRects)
	}
	for _, id := range want {
		r, ok := m.slotRects[id]
		if !ok {
			t.Fatalf("no rect for %s", id)
		}
		if id == protocol.SurfaceActivityRail {
			continue
		} // host buttons own the rail
		name := humanName(id)
		x := r.InnerX + (r.InnerW-ansi.StringWidth(name))/2
		y := r.InnerY + (r.InnerH-1)/2
		rows := strings.Split(m.View().Content, "\n")
		if got := ansi.Strip(ansi.Cut(rows[y], x, x+ansi.StringWidth(name))); got != name {
			t.Errorf("%s name at (%d,%d) = %q", id, x, y, got)
		}
	}
}

func surfaceText(m *WorkbenchModel, id protocol.SurfaceID) string {
	r := m.slotRects[id]
	rows := strings.Split(m.View().Content, "\n")
	var out []string
	for y := r.InnerY; y < r.InnerY+r.InnerH; y++ {
		if s := strings.TrimSpace(ansi.Strip(ansi.Cut(rows[y], r.InnerX, r.InnerX+r.InnerW))); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

func TestOccupantReplacesNameAndExitRestoresIt(t *testing.T) {
	m := goldenModel()
	before := m.slotRects[protocol.SurfaceSidebarList]
	check := func(want string) {
		t.Helper()
		if got := surfaceText(m, protocol.SurfaceSidebarList); strings.TrimSpace(got) != want {
			t.Fatalf("sidebar %q, want %q", got, want)
		}
		if m.slotRects[protocol.SurfaceSidebarList] != before {
			t.Fatal("column moved with occupancy")
		}
	}
	check("Sidebar list")
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList)
	check("")
	m.content("p.side", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceSidebarList, Lines: []string{"content"}})
	check("content")
	hostLeave(m, "p.side")
	check("Sidebar list")
}

// Geometry follows dock changes, never a side occupant's lifecycle.
func TestStageGeometryFollowsSideColumns(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.stage", protocol.SurfaceMainStage)
	stageWidth := func() int {
		g, ok := geometryOf(t, m, protocol.SurfaceMainStage)
		if !ok {
			t.Fatal("stage missing from geometry")
		}
		return g.Inner.Width
	}
	narrow := stageWidth()
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	if stageWidth() != narrow {
		t.Fatal("occupancy changed geometry")
	}
	hostLeave(m, "p.side")
	if stageWidth() != narrow {
		t.Fatal("exit changed geometry")
	}
	press(m, "[")
	press(m, "]")
	wide := stageWidth()
	if wide <= narrow {
		t.Fatal("docks did not release width")
	}
	press(m, "[")
	press(m, "]")
	if stageWidth() != narrow {
		t.Fatal("dock restore changed geometry")
	}
}

func TestDefaultFocusSurvivesZeroPluginStartup(t *testing.T) {
	m := goldenModel()
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Fatalf("focus %s", m.focusedSlot)
	}
	if _, ok := m.slotRects[m.focusedSlot]; !ok {
		t.Fatal("default focus not drawn")
	}
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList)
	hostLeave(m, "p.side")
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Fatalf("occupancy moved focus to %s", m.focusedSlot)
	}
}

func TestFocusReturnsWhenSideColumnReturns(t *testing.T) {
	m := goldenModel()
	press(m, "[")
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatal("hidden sidebar kept focus")
	}
	press(m, "[")
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Fatal("sidebar did not recover focus")
	}
}

func TestVanishingSideCancelsDragMovesFocusAndDisablesResizeKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		drag      string
		focus     protocol.SurfaceID
		placement contracts.RailPlacement
		narrow    bool
	}{
		{"dock hidden", "sidepanel-width", protocol.SurfaceSidePanel, "", false},
		{"panel collapsed", "sidepanel-width", protocol.SurfaceSidePanel, "", true},
		{"stage hides panel", "sidepanel-width", protocol.SurfaceSidePanel, contracts.PlacementStage, false},
		{"canvas hides panel", "sidepanel-width", protocol.SurfaceSidePanel, contracts.PlacementFullCanvas, false},
		{"canvas hides sidebar", "sidebar-width", protocol.SurfaceSidebarList, contracts.PlacementFullCanvas, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel()
			m.activeDrag, m.hoverDivider = tc.drag, tc.drag
			m.focusedSlot = tc.focus
			if tc.placement != "" {
				m.activePlacement = tc.placement
				m.recomputeLayout()
			} else if tc.narrow {
				m.width = narrowPanelWidth(m)
				m.recomputeLayout()
			} else {
				press(m, "]")
			}
			if m.activeDrag != "" || m.hoverDivider != "" || m.focusedSlot != m.centreSlot() {
				t.Fatalf("vanished side: drag %q hover %q focus %q", m.activeDrag, m.hoverDivider, m.focusedSlot)
			}
		})
	}
	m := goldenModel()
	w := m.sidebarWidth
	press(m, ">")
	if m.sidebarWidth <= w {
		t.Fatal("empty sidebar cannot resize")
	}
	press(m, "<")
	press(m, "[")
	w = m.sidebarWidth
	m.Update(tea.KeyPressMsg{Code: '>', Text: ">"})
	if m.sidebarWidth != w {
		t.Fatal("hidden sidebar resized")
	}
}
