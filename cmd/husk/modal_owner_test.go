package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func TestModalOwnerSingleSourceOfTruth(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	if mo := m.modalOwner(); mo.Open() || mo.PluginID != "" || mo.RailID != "" {
		t.Fatalf("fresh model modal = %+v, want closed", mo)
	}
	m.openModal("p", "r")
	if mo := m.modalOwner(); !mo.Open() || mo.PluginID != "p" || mo.RailID != "r" {
		t.Fatalf("after openModal modal = %+v", mo)
	}
	if got := m.surfaceOwner(huskipc.SlotModalSurface); got != "p" {
		t.Fatalf("modal surface owner = %q, want p", got)
	}
	m.closeModal()
	if m.modalOwner().Open() {
		t.Fatal("closeModal left the modal open")
	}
	if got := m.surfaceOwner(huskipc.SlotModalSurface); got != "" {
		t.Fatalf("closed modal surface owner = %q, want empty", got)
	}
}

func TestSurfaceOwnerFallsBackToRegistryOccupant(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	m.registry.Occupants[huskipc.SlotMainStage] = "occ"
	if got := m.surfaceOwner(huskipc.SlotMainStage); got != "occ" {
		t.Fatalf("stage owner = %q, want occ", got)
	}
	// An open modal owns only the modal surface.
	m.openModal("p", "r")
	if got := m.surfaceOwner(huskipc.SlotMainStage); got != "occ" {
		t.Fatalf("stage owner with modal open = %q, want occ", got)
	}
}

func TestHitOrderIsDeterministicWithOverlappingRects(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	same := SlotRect{X: 0, Y: 0, Width: 10, Height: 5, InnerX: 0, InnerY: 0, InnerW: 10, InnerH: 5}
	m.slotRects = map[huskipc.SlotID]SlotRect{
		huskipc.SlotActivityRail:   same,
		huskipc.SlotMainStage:      same,
		huskipc.SlotSidePanel:      same,
		huskipc.SlotWorkspaceStage: same,
		huskipc.SlotFullCanvas:     same,
		huskipc.SlotMainHeader:     same,
	}
	want := []huskipc.SlotID{
		huskipc.SlotFullCanvas, huskipc.SlotWorkspaceStage,
		huskipc.SlotMainHeader, huskipc.SlotMainStage, huskipc.SlotSidePanel,
		huskipc.SlotActivityRail,
	}
	for i := 0; i < 50; i++ {
		var got []huskipc.SlotID
		for _, h := range m.hitOrder() {
			got = append(got, h.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("iteration %d: hit order = %v, want %v", i, got, want)
		}
	}
}

func TestTopBarHasNoDebugCaseLabels(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	m.width, m.height = 120, 30
	m.recomputeLayout()
	check := func(name string) {
		if bar := ansi.Strip(m.renderTopWorkbenchBar()); strings.Contains(bar, "Case") {
			t.Fatalf("%s: top bar leaks debug label: %q", name, bar)
		}
	}
	check("home")
	m.activePlacement = huskipc.PlacementFullCanvas
	check("full-canvas")
	m.activePlacement = huskipc.PlacementStage
	check("stage")
	m.openModal("p", "r")
	check("modal")
}
