package main

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// topBarText is the top bar with styling stripped.
func topBarText(m *WorkbenchModel) string {
	return ansi.Strip(m.renderTopWorkbenchBar())
}

func TestWorkbenchModeLabelTracksVisibleSideSurfaces(t *testing.T) {
	sidebar := map[huskipc.SlotID][]string{huskipc.SlotSidebarList: {"list"}}
	panel := map[huskipc.SlotID][]string{huskipc.SlotSidePanel: {"panel"}}
	stage := map[huskipc.SlotID][]string{huskipc.SlotMainStage: {"stage"}}

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
			goldenPlugin(m, "p.foot", map[huskipc.SlotID][]string{huskipc.SlotSidebarFooter: {"foot"}})
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
	goldenPlugin(m, "p.left", map[huskipc.SlotID][]string{huskipc.SlotSidebarList: {"list"}})
	m.activePlacement = huskipc.PlacementFullCanvas
	if got := topBarText(m); !strings.Contains(got, "Full-Canvas Mode") {
		t.Errorf("full-canvas label missing: %q", got)
	}
	m.activePlacement = huskipc.PlacementStage
	if got := topBarText(m); !strings.Contains(got, "Stage-minus-Sidebar Mode") {
		t.Errorf("stage label missing: %q", got)
	}
}

func TestHomeCommandTitleHasNoColumnCount(t *testing.T) {
	for _, c := range builtinCommands() {
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
	for _, s := range []huskipc.SlotID{
		huskipc.SlotSidebarNavigation, huskipc.SlotSidebarList, huskipc.SlotSidebarFooter, huskipc.SlotSidePanel,
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
	stage := m.slotRects[huskipc.SlotMainStage]
	rail := m.slotRects[huskipc.SlotActivityRail]
	if stage.X != rail.Width || stage.X+stage.Width != goldenW {
		t.Errorf("stage spans x=%d..%d, want the whole width after the rail (%d..%d)",
			stage.X, stage.X+stage.Width, rail.Width, goldenW)
	}
}

func TestSideColumnAppearsAndGoesWithPlugin(t *testing.T) {
	m := goldenModel()
	emptyStageW := m.slotRects[huskipc.SlotMainStage].Width

	cp := &ConnectedPlugin{
		Info: huskipc.HelloPayload{
			PluginID: "p.side", Name: "p.side", Version: "0.1.0", Language: "go",
			Slots: []huskipc.SlotID{huskipc.SlotSidebarList, huskipc.SlotSidePanel},
		},
		Writer: huskipc.NewConnWriter(io.Discard),
	}
	m.Update(pluginRegisteredMsg{cp})
	if _, ok := m.slotRects[huskipc.SlotSidebarList]; !ok {
		t.Fatal("sidebar did not appear when a plugin occupied it")
	}
	if _, ok := m.slotRects[huskipc.SlotSidePanel]; !ok {
		t.Fatal("side panel did not appear when a plugin occupied it")
	}
	if w := m.slotRects[huskipc.SlotMainStage].Width; w >= emptyStageW {
		t.Errorf("stage did not yield space: %d >= %d", w, emptyStageW)
	}
	if got := topBarText(m); !strings.Contains(got, " Workbench · Left + Right ") {
		t.Errorf("label after connect: %q", got)
	}

	m.Update(pluginDisconnectedMsg{plugin: cp})
	if n := sideRectCount(m); n != 0 {
		t.Errorf("%d side rects survived the plugin exit", n)
	}
	if w := m.slotRects[huskipc.SlotMainStage].Width; w != emptyStageW {
		t.Errorf("stage width %d after exit, want %d", w, emptyStageW)
	}
	if got := topBarText(m); strings.Contains(got, "·") {
		t.Errorf("label after exit still names a side: %q", got)
	}
}
