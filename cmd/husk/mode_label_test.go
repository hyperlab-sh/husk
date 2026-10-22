package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// topBarText is the top bar with styling stripped.
func topBarText(m *WorkbenchModel) string {
	return ansi.Strip(m.renderTopWorkbenchBar())
}

func TestWorkbenchModeLabelTracksVisibleSideSurfaces(t *testing.T) {
	sidebar := map[protocol.SurfaceID][]string{protocol.SurfaceSidebarList: {"list"}}
	panel := map[protocol.SurfaceID][]string{protocol.SurfaceSidePanel: {"panel"}}
	stage := map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"stage"}}

	cases := []struct {
		name  string
		setup func(m *WorkbenchModel)
		want  string
	}{
		{"zero plugins", func(m *WorkbenchModel) {}, " Workbench "},
		{"stage only", func(m *WorkbenchModel) { goldenPlugin(m, "p.stage", stage) }, " Workbench "},
		{"left only", func(m *WorkbenchModel) { goldenPlugin(m, "p.left", sidebar) }, " Workbench · Left "},
		{"right only", func(m *WorkbenchModel) { goldenPlugin(m, "p.right", panel) }, " Workbench · Right "},
		{"both", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.left", sidebar)
			goldenPlugin(m, "p.right", panel)
		}, " Workbench · Left + Right "},
		{"any sidebar slot counts as left", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.foot", map[protocol.SurfaceID][]string{protocol.SurfaceSidebarFooter: {"foot"}})
		}, " Workbench · Left "},
		{"sidebar dock hidden", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.left", sidebar)
			goldenPlugin(m, "p.right", panel)
			m.showSidebar = false
			m.recomputeLayout()
		}, " Workbench · Right "},
		{"side panel dock hidden", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.left", sidebar)
			goldenPlugin(m, "p.right", panel)
			m.showSidePane = false
			m.recomputeLayout()
		}, " Workbench · Left "},
		{"both docks hidden", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.left", sidebar)
			goldenPlugin(m, "p.right", panel)
			m.showSidebar, m.showSidePane = false, false
			m.recomputeLayout()
		}, " Workbench "},
		{"side panel does not fit beside the sidebar", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.left", sidebar)
			goldenPlugin(m, "p.right", panel)
			m.width = 90
			m.recomputeLayout()
		}, " Workbench · Left "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel()
			tc.setup(m)
			got := topBarText(m)
			if !strings.Contains(got, tc.want) {
				t.Errorf("top bar %q lacks label %q", got, tc.want)
			}
			if strings.Contains(got, "3-Column") {
				t.Errorf("top bar %q still names a column count", got)
			}
			if tc.want == " Workbench " && strings.Contains(got, "·") {
				t.Errorf("top bar %q names a side surface that is not visible", got)
			}
		})
	}
}

func TestWorkbenchModeLabelKeepsOtherPlacementLabels(t *testing.T) {
	m := goldenModel()
	goldenPlugin(m, "p.left", map[protocol.SurfaceID][]string{protocol.SurfaceSidebarList: {"list"}})
	m.activePlacement = contracts.PlacementFullCanvas
	if got := topBarText(m); !strings.Contains(got, "Full-Canvas Mode") {
		t.Errorf("full-canvas label missing: %q", got)
	}
	m.activePlacement = contracts.PlacementStage
	if got := topBarText(m); !strings.Contains(got, "Stage-minus-Sidebar Mode") {
		t.Errorf("stage label missing: %q", got)
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

func sideRectCount(m *WorkbenchModel) int {
	n := 0
	for _, s := range []protocol.SurfaceID{
		protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList, protocol.SurfaceSidebarFooter, protocol.SurfaceSidePanel,
	} {
		if _, ok := m.slotRects[s]; ok {
			n++
		}
	}
	return n
}

func TestZeroPluginsLayDownNoSideColumns(t *testing.T) {
	m := goldenModel()
	if n := sideRectCount(m); n != 0 {
		t.Fatalf("zero plugins laid out %d side rects", n)
	}
	stage := m.slotRects[protocol.SurfaceMainStage]
	rail := m.slotRects[protocol.SurfaceActivityRail]
	if stage.X != rail.Width || stage.X+stage.Width != goldenW {
		t.Errorf("stage spans x=%d..%d, want the whole width after the rail (%d..%d)",
			stage.X, stage.X+stage.Width, rail.Width, goldenW)
	}
}

func TestSideColumnAppearsAndGoesWithPlugin(t *testing.T) {
	m := goldenModel()
	emptyStageW := m.slotRects[protocol.SurfaceMainStage].Width

	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	if _, ok := m.slotRects[protocol.SurfaceSidebarList]; !ok {
		t.Fatal("sidebar did not appear when a plugin occupied it")
	}
	if _, ok := m.slotRects[protocol.SurfaceSidePanel]; !ok {
		t.Fatal("side panel did not appear when a plugin occupied it")
	}
	if w := m.slotRects[protocol.SurfaceMainStage].Width; w >= emptyStageW {
		t.Errorf("stage did not yield space: %d >= %d", w, emptyStageW)
	}
	if got := topBarText(m); !strings.Contains(got, " Workbench · Left + Right ") {
		t.Errorf("label after connect: %q", got)
	}

	hostLeave(m, "p.side")
	if n := sideRectCount(m); n != 0 {
		t.Errorf("%d side rects survived the plugin exit", n)
	}
	if w := m.slotRects[protocol.SurfaceMainStage].Width; w != emptyStageW {
		t.Errorf("stage width %d after exit, want %d", w, emptyStageW)
	}
	if got := topBarText(m); strings.Contains(got, "·") {
		t.Errorf("label after exit still names a side: %q", got)
	}
}

// The occupant of the stage learns its size from husk.geometry (the host turns
// the write into surface.resized): it must follow side columns coming and going.
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
	wide := stageWidth()

	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	if narrow := stageWidth(); narrow >= wide || narrow != m.slotRects[protocol.SurfaceMainStage].InnerW {
		t.Fatalf("after side connect geometry says %d (layout %d), want narrower than %d", narrow, m.slotRects[protocol.SurfaceMainStage].InnerW, wide)
	}
	hostLeave(m, "p.side")
	if got := stageWidth(); got != wide {
		t.Errorf("after side exit geometry says %d, want %d", got, wide)
	}
}

// With zero plugins the default focus (sidebar list) must survive, so a
// sidebar plugin that connects has keys from its first frame.
func TestDefaultFocusSurvivesZeroPluginStartup(t *testing.T) {
	m := goldenModel()
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Fatalf("zero-plugin focus moved to %q", m.focusedSlot)
	}
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Errorf("focus after sidebar plugin connects = %q, want the sidebar list", m.focusedSlot)
	}
}

// Focus pushed off a vanished column comes back when the column does.
func TestFocusReturnsWhenSideColumnReturns(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	hostLeave(m, "p.side")
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatalf("focus = %q after the column vanished, want the main stage", m.focusedSlot)
	}
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	if m.focusedSlot != protocol.SurfaceSidebarList {
		t.Errorf("focus = %q after the column returned, want the sidebar list", m.focusedSlot)
	}
}

func TestVanishingSideCancelsDragMovesFocusAndDisablesResizeKeys(t *testing.T) {
	m := goldenModel()
	hostPlugin(m, "p.side", protocol.SurfaceSidebarList, protocol.SurfaceSidePanel)
	m.activeDrag, m.hoverDivider = "sidepanel-width", "sidepanel-width"
	m.focusedSlot = protocol.SurfaceSidePanel
	hostLeave(m, "p.side")
	if m.activeDrag != "" || m.hoverDivider != "" {
		t.Errorf("drag %q / hover %q survived its column", m.activeDrag, m.hoverDivider)
	}
	if m.focusedSlot != protocol.SurfaceMainStage {
		t.Errorf("focus stayed on vanished %q", m.focusedSlot)
	}

	w := m.sidebarWidth
	m.Update(tea.KeyPressMsg{Code: '>', Text: ">"})
	m.Update(tea.KeyPressMsg{Code: '<', Text: "<"})
	if m.sidebarWidth != w {
		t.Errorf("resize keys changed sidebar width %d -> %d with no sidebar", w, m.sidebarWidth)
	}
}
