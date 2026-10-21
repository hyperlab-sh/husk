package main

import (
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
		{"side panel does not fit", func(m *WorkbenchModel) {
			goldenPlugin(m, "p.right", panel)
			m.width = 90
			m.recomputeLayout()
		}, " Workbench "},
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
