package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestHumanName(t *testing.T) {
	for _, tc := range []struct {
		id   protocol.SurfaceID
		want string
	}{
		{protocol.SurfaceActivityRail, "Activity rail"}, {protocol.SurfaceSidebarNavigation, "Sidebar navigation"},
		{protocol.SurfaceSidebarList, "Sidebar list"}, {protocol.SurfaceThreadRowStatus, "Thread row status"},
		{protocol.SurfaceSidebarFooter, "Sidebar footer"}, {protocol.SurfaceMainHeader, "Main header"},
		{protocol.SurfaceMainStage, "Main stage"}, {protocol.SurfaceSidePanel, "Side panel"},
		{protocol.SurfaceAppOverlay, "App overlay"}, {protocol.SurfaceModal, "Modal surface"},
		{protocol.SurfaceFullCanvas, "Full canvas"}, {protocol.SurfaceWorkspaceStage, "Workspace stage"},
		{"écran-test", "Écran test"}, {"", ""},
	} {
		t.Run(string(tc.id), func(t *testing.T) {
			if got := humanName(tc.id); got != tc.want {
				t.Fatalf("name %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEmptySurfaceNameClipsAndHides(t *testing.T) {
	for _, tc := range []struct {
		w, h, x, y int
		want       string
	}{
		{20, 5, 5, 2, "Main stage"}, {10, 1, 0, 0, "Main stage"}, {9, 1, 0, 0, "Main sta…"},
		{3, 1, 0, 0, "Ma…"}, {2, 1, 0, 0, ""}, {10, 0, 0, 0, ""}, {13, 4, 1, 1, "Main stage"},
	} {
		t.Run(fmt.Sprintf("%dx%d", tc.w, tc.h), func(t *testing.T) {
			m := goldenModel()
			m.slotRects[protocol.SurfaceMainStage] = SlotRect{Width: tc.w, Height: tc.h, InnerW: tc.w, InnerH: tc.h}
			c := newCanvas(24, 6)
			defer c.release()
			m.drawSurface(c, protocol.SurfaceMainStage, "⑥", "main-stage")
			if got := strings.TrimSpace(ansi.Strip(c.render())); got != tc.want {
				t.Fatalf("render %q, want %q", got, tc.want)
			}
			if tc.want != "" {
				cell := c.buf.CellAt(tc.x, tc.y)
				if cell.Content != "M" || !sameColor(cell.Style.Fg, lipgloss.Color(m.palette.InkSubtle)) || !sameColor(cell.Style.Bg, lipgloss.Color(m.palette.Slot)) || cell.Style.Attrs&uv.AttrBold != 0 {
					t.Fatalf("wrong name position/style: %+v", cell)
				}
			}
		})
	}
}

func TestEmptyNameNeverOnPlacementSurfaces(t *testing.T) {
	for _, p := range []contracts.RailPlacement{contracts.PlacementFullCanvas, contracts.PlacementStage, contracts.PlacementModal} {
		m := goldenModel()
		m.activePlacement = p
		if p == contracts.PlacementModal {
			m.activePlacement = ""
			m.openModal("p", "p.open")
		}
		m.recomputeLayout()
		id := protocol.SurfaceFullCanvas
		if p == contracts.PlacementStage {
			id = protocol.SurfaceWorkspaceStage
		}
		if p == contracts.PlacementModal {
			id = protocol.SurfaceModal
			m.slotRects[id] = m.modalRect
		}
		if got := surfaceText(m, id); got != "" {
			t.Fatalf("%s unexpectedly draws %q", id, got)
		}
	}
}

func TestHotspotBadgeDrawnOverEmptyName(t *testing.T) {
	m := goldenModel()
	m.showHotspots = true
	m.slotRects[protocol.SurfaceMainStage] = SlotRect{Width: 10, Height: 1, InnerW: 10, InnerH: 1}
	c := newCanvas(10, 1)
	defer c.release()
	m.drawSurface(c, protocol.SurfaceMainStage, "⑥", "main-stage")
	if got := ansi.Strip(c.render()); !strings.HasPrefix(got, "⑥ main-sta") {
		t.Fatalf("badge did not cover name: %q", got)
	}
}

func TestExplorerReplacesMainStageName(t *testing.T) {
	m := goldenModel()
	m.stageMode = "hotspots"
	if got := surfaceText(m, protocol.SurfaceMainStage); strings.Contains(got, "Main stage") || !strings.Contains(got, "ARCHITECTURE") {
		t.Fatalf("explorer content: %q", got)
	}
}

func docksHint(m *WorkbenchModel) string {
	for _, h := range m.footerHints() {
		if h.desc == "docks" {
			return h.key
		}
	}
	return ""
}

func TestSidePanelFitAtThreshold(t *testing.T) {
	for _, left := range []bool{true, false} {
		m := goldenModel()
		m.showSidebar = left
		for _, w := range []int{narrowPanelWidth(m), narrowPanelWidth(m) + 1, minTermWidth, goldenW} {
			m.width = w
			m.recomputeLayout()
			fit := !left || w >= narrowPanelWidth(m)+1
			_, drawn := m.slotRects[protocol.SurfaceSidePanel]
			visible := false
			for _, s := range m.surfaceView().Visible {
				visible = visible || s == protocol.SurfaceSidePanel
			}
			if drawn != fit || visible != fit || !m.showSidePane {
				t.Fatalf("width %d sidebar %v: drawn %v visible %v preference %v", w, left, drawn, visible, m.showSidePane)
			}
			label := "Workbench"
			if left {
				label += " · Left"
			}
			if fit {
				if left {
					label += " + Right"
				} else {
					label += " · Right"
				}
			}
			want := "["
			if fit {
				want = "[ ] /"
			}
			if m.layoutLabel() != label || docksHint(m) != want {
				t.Fatalf("width %d label %q docks %q", w, m.layoutLabel(), docksHint(m))
			}
		}
	}
}

func TestFooterDockKeysFollowPlacementAndFit(t *testing.T) {
	for _, p := range []contracts.RailPlacement{"", contracts.PlacementFullCanvas, contracts.PlacementStage} {
		for _, left := range []bool{false, true} {
			for _, wide := range []bool{false, true} {
				for _, panel := range []bool{false, true} {
					for _, modal := range []bool{false, true} {
						m := goldenModel()
						m.activePlacement = p
						m.showSidebar = left
						m.showSidePane = panel
						if !wide {
							m.width = minTermWidth
						}
						if modal {
							m.openModal("p", "p.open")
						}
						m.recomputeLayout()
						want := "["
						if p == contracts.PlacementFullCanvas {
							want = ""
						} else if p == "" && (wide || !left) {
							want = "[ ] /"
						}
						if got := docksHint(m); got != want {
							t.Fatalf("placement %q left %v wide %v panel %v modal %v: docks %q want %q", p, left, wide, panel, modal, got, want)
						}
					}
				}
			}
		}
	}
	m := goldenModel()
	m.activePlacement = contracts.PlacementFullCanvas
	m.recomputeLayout()
	press(m, "[")
	press(m, "]")
	if m.showSidebar || m.showSidePane {
		t.Fatal("hidden dock bindings stopped working")
	}
	m = goldenModel()
	m.keys = noDockKeys{m.keys}
	if got := docksHint(m); got != "" {
		t.Fatalf("unbound keys advertised: %q", got)
	}
}

type noDockKeys struct{ keySource }

func (k noDockKeys) KeysFor(action string) []string {
	switch action {
	case "husk.sidebar.toggle", "husk.sidepanel.toggle", "husk.docks.cycle":
		return nil
	}
	return k.keySource.KeysFor(action)
}

func TestEmptySurfaceInputAndFocus(t *testing.T) {
	for _, position := range []string{"corner", "center"} {
		t.Run(position, func(t *testing.T) {
			m := goldenModel()
			hostPlugin(m, "p.stage", protocol.SurfaceMainStage)
			fe := withFE(m)
			m.focusedSlot = protocol.SurfaceSidebarList
			r := m.slotRects[protocol.SurfaceSidebarList]
			x, y := r.X, r.Y
			if position == "center" {
				x, y = r.InnerX+r.InnerW/2, r.InnerY+r.InnerH/2
			}
			m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			if len(fe.Mice) != 0 {
				t.Fatalf("empty sidebar sent mouse to a live occupant: %+v", fe.Mice)
			}
			press(m, "x")
			if len(fe.Keys) != 0 {
				t.Fatalf("empty sidebar sent key to a live occupant: %+v", fe.Keys)
			}
			if m.focusedSlot != protocol.SurfaceSidebarList || m.activeDrag != "" {
				t.Fatalf("empty sidebar focus %q drag %q", m.focusedSlot, m.activeDrag)
			}
		})
	}
	m := goldenModel()
	for _, want := range []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceSidebarList} {
		press(m, "tab")
		if m.focusedSlot != want {
			t.Fatalf("focus %s want %s", m.focusedSlot, want)
		}
	}
	m.activePlacement = contracts.PlacementStage
	m.recomputeLayout()
	press(m, "tab")
	if m.focusedSlot != protocol.SurfaceWorkspaceStage {
		t.Fatal("stage cycle missed workspace")
	}
}
