package workbench

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestRuntimeCanvasRecapturesUnderlyingFocus(t *testing.T) {
	for _, path := range []string{"Home_new_canvas", "Home_restore", "rail_restore"} {
		t.Run(path, func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			x := contracts.RailItem{Plugin: "rail", ID: "rail.x", Placement: contracts.PlacementStage}
			y := contracts.RailItem{Plugin: "rail", ID: "rail.y", Placement: contracts.PlacementFullCanvas}
			m.plugins["rail"] = true
			m.railItems = []contracts.RailItem{x, y}
			m.activateRailItem(x)
			openRuntimeFixture(m, fe, "p.a", protocol.SurfacePlacementCanvas)
			m.activateHomeWorkbench()
			want := protocol.SurfaceSidebarList
			switch path {
			case "Home_new_canvas":
				openRuntimeFixture(m, fe, "p.b", protocol.SurfacePlacementCanvas)
				m.closeRuntime("p.b")
			case "Home_restore":
				m.restoreRuntime()
			case "rail_restore":
				m.activateRailItem(y)
				m.restoreRuntime()
				want = protocol.SurfaceFullCanvas
			}
			m.closeRuntime("p.a")
			if m.focusedSlot != want || !slices.Contains(m.surfaceView().Visible, m.focusedSlot) {
				t.Fatalf("focus=%s want shown %s, view=%+v", m.focusedSlot, want, m.surfaceView())
			}
		})
	}
}

func TestRuntimeCanvasRailSelectionDoesNotToggleUnderlyingItem(t *testing.T) {
	for _, placement := range []contracts.RailPlacement{contracts.PlacementStage, contracts.PlacementFullCanvas} {
		t.Run(string(placement), func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			ri := contracts.RailItem{Plugin: "rail", ID: "rail.x", Placement: placement}
			m.plugins["rail"] = true
			m.railItems = []contracts.RailItem{ri}
			m.activateRailItem(ri)
			openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
			clickRail := func() {
				t.Helper()
				rr := m.slotRects[protocol.SurfaceActivityRail]
				for _, b := range m.railButtons() {
					if b.kind == "plugin" && b.item.ID == ri.ID {
						m.handleMouse(tea.MouseClickMsg{X: rr.X, Y: rr.Y + b.startY, Button: tea.MouseLeft})
						return
					}
				}
				t.Fatal("rail item has no button")
			}
			clickRail()
			if !m.runtimeHidden || m.runtimeCanvas() != "" || m.activeRailID != ri.ID || m.activePlacement != placement || !slices.Contains(m.surfaceView().Visible, m.focusedSlot) {
				t.Fatalf("rail selection toggled away: view=%+v focus=%s hidden=%v", m.surfaceView(), m.focusedSlot, m.runtimeHidden)
			}
			// Once the rail item really is shown, its ordinary toggle still works.
			clickRail()
			if m.activeRailID != "" {
				t.Fatal("shown rail item no longer toggles Home")
			}
		})
	}
}

func TestRuntimeFrameLabelDoesNotUseOpenTitle(t *testing.T) {
	for _, placement := range []protocol.SurfacePlacement{protocol.SurfacePlacementModal, protocol.SurfacePlacementCanvas} {
		t.Run(string(placement), func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			id := protocol.SurfaceID("p.surface")
			openRuntimeFixture(m, fe, id, placement)
			fe.Runtime[0].Title = "Open metadata"
			fe.Runtime[0].Frame.Label = "Declared label"
			m.slotUpdates[id].Title = ""
			m.syncRuntime()
			if got := m.frameFor(id).Label; got != "Declared label" {
				t.Fatalf("frame label=%q", got)
			}
			drawn := ansi.Strip(m.View().Content)
			if !strings.Contains(drawn, "Declared label") || strings.Contains(drawn, "Open metadata") {
				t.Fatal("open metadata replaced the declared frame label")
			}
		})
	}
}

func TestRuntimeCanvasViewKeepsUnderlyingPlacement(t *testing.T) {
	for _, placement := range []contracts.RailPlacement{"", contracts.PlacementStage, contracts.PlacementFullCanvas} {
		t.Run("under_"+string(placement), func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			if placement != "" {
				ri := contracts.RailItem{Plugin: "rail", ID: "rail.x", Placement: placement}
				m.plugins["rail"] = true
				m.railItems = []contracts.RailItem{ri}
				m.activateRailItem(ri)
			}
			before := m.surfaceView().Placement
			openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
			view := m.surfaceView()
			if view.Placement != before || !slices.Contains(view.Visible, protocol.SurfaceID("p.canvas")) {
				t.Fatalf("view=%+v underlying=%+v", view, before)
			}
			if len(fe.Runtime) != 1 || fe.Runtime[0].Placement != protocol.SurfacePlacementCanvas {
				t.Fatal("runtime declaration lost placement")
			}
		})
	}
}

func TestRuntimeCanvasFrameCloseHandsOffAndRestoresFocus(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	openRuntimeFixture(m, fe, "p.a", protocol.SurfacePlacementCanvas)
	openRuntimeFixture(m, fe, "p.b", protocol.SurfacePlacementCanvas)
	for _, id := range []protocol.SurfaceID{"p.b", "p.a"} {
		r := m.slotRects[id]
		m.handleMouse(tea.MouseClickMsg{X: r.X + r.Width - 2, Y: r.Y, Button: tea.MouseLeft})
		if slices.ContainsFunc(fe.Runtime, func(r contracts.RuntimeSurface) bool { return r.Surface == id }) {
			t.Fatalf("close button retained %s in runtime state", id)
		}
		want := protocol.SurfaceID("p.a")
		if id == "p.a" {
			want = protocol.SurfaceSidebarList
		}
		if m.focusedSlot != want || !slices.Contains(m.surfaceView().Visible, want) {
			t.Fatalf("focus=%s want shown %s", m.focusedSlot, want)
		}
	}
	if len(fe.Runtime) != 0 || m.runtimeCanvas() != "" {
		t.Fatal("final close retained runtime surfaces")
	}
}

func TestRuntimeCanvasFooterShowsOnlyDockKeysThatChangeTheFrame(t *testing.T) {
	for _, p := range []contracts.RailPlacement{"", contracts.PlacementStage, contracts.PlacementFullCanvas} {
		for _, left := range []bool{false, true} {
			for _, canvas := range []bool{false, true} {
				name := string(p) + "/" + map[bool]string{false: "nosidebar", true: "sidebar"}[left] + "/" + map[bool]string{false: "plain", true: "canvas"}[canvas]
				t.Run(name, func(t *testing.T) {
					m := goldenModel()
					fe := withFE(m)
					m.activePlacement = p
					m.showSidebar = left
					panel := m.showSidePane
					// Home also returns to the default placement, so the baseline is that placement.
					twin := goldenModel()
					twin.showSidebar = left
					twin.recomputeLayout()
					baseline := docksHint(twin)
					if canvas {
						openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
					}
					m.recomputeLayout()
					if (m.runtimeCanvas() != "") != canvas {
						t.Fatalf("runtime canvas=%q want present=%v", m.runtimeCanvas(), canvas)
					}
					shown := strings.Fields(docksHint(m))
					if canvas && len(shown) != 0 {
						t.Fatalf("docks hint under a runtime canvas: %q", shown)
					}
					if !canvas && p != contracts.PlacementFullCanvas && len(shown) == 0 {
						t.Fatal("no dock keys shown without a canvas")
					}
					for _, key := range shown {
						before := m.View().Content
						press(m, key)
						if after := m.View().Content; after == before {
							t.Errorf("footer shows %q but pressing it leaves the frame unchanged", key)
						}
						m.showSidebar, m.showSidePane = left, panel
						m.recomputeLayout()
					}
					if canvas {
						// Under a shown canvas no dock is drawn: the keys leave the frame unchanged.
						for _, id := range []string{"husk.sidebar.toggle", "husk.sidepanel.toggle", "husk.docks.cycle"} {
							key := m.firstKey(id)
							before := m.View().Content
							press(m, key)
							if after := m.View().Content; after != before {
								t.Errorf("%s (%q) changes the frame under a runtime canvas", id, key)
							}
							m.showSidebar, m.showSidePane = left, panel
							m.recomputeLayout()
						}
						// Hiding the stack (Home) does not close it; the docks are drawn again.
						m.activateHomeWorkbench()
						if m.runtimeCanvas() != "" || len(m.runtime) == 0 {
							t.Fatalf("Home should hide, not close: canvas=%q stack=%d", m.runtimeCanvas(), len(m.runtime))
						}
						m.recomputeLayout()
						if got := docksHint(m); got != baseline {
							t.Fatalf("hidden canvas: docks %q want baseline %q", got, baseline)
						}
						m.restoreRuntime()
						m.recomputeLayout()
						if got := docksHint(m); got != "" {
							t.Fatalf("restored canvas: docks %q want empty", got)
						}
					}
				})
			}
		}
	}
}
