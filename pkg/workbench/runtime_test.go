package workbench

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func openRuntimeFixture(m *workbenchModel, fe *fakeFE, id protocol.SurfaceID, placement protocol.SurfacePlacement) {
	plugin, _, _ := strings.Cut(string(id), ".")
	r := contracts.RuntimeSurface{Surface: id, Plugin: plugin, Placement: placement, Title: "Runtime " + string(placement), Frame: &protocol.FrameSpec{Border: true, Label: "Runtime", Close: true}}
	fe.Runtime = append(fe.Runtime, r)
	addPlugin(m, plugin, map[protocol.SurfaceID][]string{id: {"runtime content " + string(id)}})
	m.syncRuntime()
}

func restorePaletteRow(m *workbenchModel) bool {
	return slices.ContainsFunc(m.allCommands(), func(c paletteCommand) bool { return c.ID == "husk.surfaces.restore" })
}

func TestRuntimeCanvasStackLifecycle(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	ri := contracts.RailItem{Plugin: "rail", ID: "rail.stage", Placement: contracts.PlacementStage}
	m.plugins["rail"] = true
	m.railItems = []contracts.RailItem{ri}
	m.activateRailItem(ri)
	for _, id := range []protocol.SurfaceID{"a.one", "b.two", "c.three"} {
		openRuntimeFixture(m, fe, id, protocol.SurfacePlacementCanvas)
	}
	shown := func(want protocol.SurfaceID) {
		t.Helper()
		if m.runtimeCanvas() != want || m.focusedSlot != want {
			t.Fatalf("shown=%s focus=%s want %s", m.runtimeCanvas(), m.focusedSlot, want)
		}
		if _, ok := m.slotRects[want]; !ok {
			t.Fatal("shown canvas has no geometry")
		}
	}
	shown("c.three")
	m.closeRuntime("b.two")
	shown("c.three")
	if len(fe.Runtime) != 2 {
		t.Fatal("hidden close did not remove exactly one")
	}
	m.closeRuntime("c.three")
	shown("a.one")
	m.closeRuntime("a.one")
	if m.activeRailID != ri.ID || m.activePlacement != ri.Placement || m.focusedSlot != protocol.SurfaceWorkspaceStage || m.runtimeCanvas() != "" {
		t.Fatal("last close did not restore rail selection and focus")
	}
}

func TestRuntimeHomeHideRestoreAndPalette(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	m.restoreRuntime()
	if restorePaletteRow(m) || m.runtimeCanvas() != "" {
		t.Fatal("empty restore changed layout or palette")
	}
	openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
	if restorePaletteRow(m) {
		t.Fatal("restore row present while stack shown")
	}
	m.activateHomeWorkbench()
	if !m.runtimeHidden || m.runtimeCanvas() != "" || len(fe.Runtime) != 1 || m.catalog.Occupants["p.canvas"] != "p" || !restorePaletteRow(m) {
		t.Fatal("Home closed runtime or failed to advertise restore")
	}
	m.restoreRuntime()
	if m.runtimeCanvas() != "p.canvas" || m.focusedSlot != "p.canvas" || restorePaletteRow(m) {
		t.Fatal("restore did not show/focus top and remove palette row")
	}
	before, _ := json.Marshal(m.surfaceView())
	m.restoreRuntime()
	after, _ := json.Marshal(m.surfaceView())
	if string(before) != string(after) {
		t.Fatal("restore while shown is not a no-op")
	}
	ri := contracts.RailItem{Plugin: "p", ID: "p.rail", Placement: contracts.PlacementStage}
	m.railItems = []contracts.RailItem{ri}
	m.activateRailItem(ri)
	if !m.runtimeHidden || len(fe.Runtime) != 1 || m.activeRailID != ri.ID {
		t.Fatal("rail activation did not hide without closing")
	}
	openRuntimeFixture(m, fe, "p.new", protocol.SurfacePlacementCanvas)
	if m.runtimeCanvas() != "p.new" || m.runtimeHidden {
		t.Fatal("new open did not re-show hidden stack")
	}
}

func TestRuntimeModalOuterBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		w, h          int
		width, height protocol.SurfaceDimension
		wantW, wantH  int
	}{
		{"default", 144, 40, nil, nil, 92, 24},
		{"at_terminal_minimum", 30, 12, nil, nil, 20, 6},
		{"zero", 144, 40, protocol.SurfaceDimension(`0`), protocol.SurfaceDimension(`0`), 20, 6},
		{"negative", 144, 40, protocol.SurfaceDimension(`-40`), protocol.SurfaceDimension(`-8`), 20, 6},
		{"percent_minimum", 144, 40, protocol.SurfaceDimension(`"0%"`), protocol.SurfaceDimension(`"0%"`), 20, 6},
		{"percent_maximum", 144, 40, protocol.SurfaceDimension(`"999%"`), protocol.SurfaceDimension(`"999%"`), 134, 34},
		{"minimum", 144, 40, protocol.SurfaceDimension(`1`), protocol.SurfaceDimension(`1`), 20, 6},
		{"maximum", 144, 40, protocol.SurfaceDimension(`999`), protocol.SurfaceDimension(`999`), 134, 34},
		{"percent", 144, 40, protocol.SurfaceDimension(`"60%"`), protocol.SurfaceDimension(`"50%"`), 86, 20},
		{"tiny", 25, 10, protocol.SurfaceDimension(`1`), protocol.SurfaceDimension(`1`), 25, 10},
		{"tiny_width", 29, 40, nil, nil, 29, 40},
		{"tiny_height", 144, 11, nil, nil, 144, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			openRuntimeFixture(m, fe, "p.popup", protocol.SurfacePlacementModal)
			m.width, m.height = tc.w, tc.h
			m.runtime[0].Width, m.runtime[0].Height = tc.width, tc.height
			r := m.runtimeModalRect("p.popup")
			if r.Width != tc.wantW || r.Height != tc.wantH || r.InnerW != max(1, tc.wantW-2) || r.InnerH != max(1, tc.wantH-2) || r.X < 0 || r.Y < 0 {
				t.Fatalf("outer/content geometry=%+v", r)
			}
		})
	}
}

func TestRuntimeModalInputAndUserClose(t *testing.T) {
	for _, path := range []string{"key", "frame", "settings", "home"} {
		t.Run(path, func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			openRuntimeFixture(m, fe, "p.popup", protocol.SurfacePlacementModal)
			if ctx := m.keyContext(); ctx.FocusSurface != "p.popup" || ctx.FocusOwner != "p" {
				t.Fatalf("context=%+v", ctx)
			}
			m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
			if len(fe.Keys) == 0 || fe.Keys[len(fe.Keys)-1].P.Surface != "p.popup" {
				t.Fatal("runtime modal key routed to fixed surface")
			}
			switch path {
			case "key":
				m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			case "frame":
				r := m.modalRect
				m.handleMouse(tea.MouseClickMsg{X: r.X + r.Width - 2, Y: r.Y, Button: tea.MouseLeft})
			case "settings":
				if _, err := m.openSettings(nil); err != nil {
					t.Fatal(err)
				}
			case "home":
				m.activateHomeWorkbench()
			}
			if m.modalOwner().Open() || len(fe.Runtime) != 0 {
				t.Fatal("user close path left runtime modal open")
			}
		})
	}
}

func TestGoldenFrameRuntimeModal(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	openRuntimeFixture(m, fe, "p.popup", protocol.SurfacePlacementModal)
	frame := runGolden(t, m)
	requireFrameGeometry(t, frame)
	if !strings.Contains(ansi.Strip(frame), "runtime content p.popup") {
		t.Fatal("runtime modal content missing")
	}
	golden.RequireEqual(t, frame)
}
func TestGoldenFrameRuntimeCanvas(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
	frame := runGolden(t, m)
	requireFrameGeometry(t, frame)
	if !strings.Contains(ansi.Strip(frame), "runtime content p.canvas") {
		t.Fatal("runtime canvas content missing")
	}
	golden.RequireEqual(t, frame)
}
