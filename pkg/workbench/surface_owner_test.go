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

func TestModalOwnerSingleSourceOfTruth(t *testing.T) {
	m := newModel()
	if mo := m.modalOwner(); mo.Open() || mo.PluginID != "" || mo.RailID != "" {
		t.Fatalf("fresh model modal = %+v, want closed", mo)
	}
	m.catalog.Occupants[protocol.SurfaceModal] = "p"
	m.openModal("p", "r")
	if mo := m.modalOwner(); !mo.Open() || mo.PluginID != "p" || mo.RailID != "r" {
		t.Fatalf("after openModal modal = %+v", mo)
	}
	if got := m.surfaceOwner(protocol.SurfaceModal); got != "p" {
		t.Fatalf("modal surface owner = %q, want p", got)
	}
	m.closeModal()
	if m.modalOwner().Open() {
		t.Fatal("closeModal left the modal open")
	}
	if got := m.surfaceOwner(protocol.SurfaceModal); got != "p" {
		t.Fatalf("closed modal surface owner = %q, want the catalog occupant p", got)
	}
}

func TestSurfaceOwnerFallsBackToRegistryOccupant(t *testing.T) {
	m := newModel()
	m.catalog.Occupants[protocol.SurfaceMainStage] = "occ"
	if got := m.surfaceOwner(protocol.SurfaceMainStage); got != "occ" {
		t.Fatalf("stage owner = %q, want occ", got)
	}
	// An open modal owns only the modal surface.
	m.openModal("p", "r")
	if got := m.surfaceOwner(protocol.SurfaceMainStage); got != "occ" {
		t.Fatalf("stage owner with modal open = %q, want occ", got)
	}
}

func TestHitOrderIsDeterministicWithOverlappingRects(t *testing.T) {
	m := newModel()
	same := slotRect{X: 0, Y: 0, Width: 10, Height: 5, InnerX: 0, InnerY: 0, InnerW: 10, InnerH: 5}
	m.slotRects = map[protocol.SurfaceID]slotRect{
		protocol.SurfaceActivityRail:   same,
		protocol.SurfaceMainStage:      same,
		protocol.SurfaceSidePanel:      same,
		protocol.SurfaceWorkspaceStage: same,
		protocol.SurfaceFullCanvas:     same,
		protocol.SurfaceMainHeader:     same,
	}
	want := []protocol.SurfaceID{
		protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage,
		protocol.SurfaceMainHeader, protocol.SurfaceMainStage, protocol.SurfaceSidePanel,
		protocol.SurfaceActivityRail,
	}
	for i := 0; i < 50; i++ {
		var got []protocol.SurfaceID
		for _, h := range m.hitOrder() {
			got = append(got, h.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("iteration %d: hit order = %v, want %v", i, got, want)
		}
	}
}

func TestTopBarHasNoDebugCaseLabels(t *testing.T) {
	m := newModel()
	m.width, m.height = 120, 30
	m.recomputeLayout()
	check := func(name string) {
		if bar := ansi.Strip(m.renderTopWorkbenchBar()); strings.Contains(bar, "Case") {
			t.Fatalf("%s: top bar leaks debug label: %q", name, bar)
		}
	}
	check("home")
	m.activePlacement = contracts.PlacementFullCanvas
	check("full-canvas")
	m.activePlacement = contracts.PlacementStage
	check("stage")
	m.openModal("p", "r")
	check("modal")
}

// The occupant, not the plugin whose rail item opened the modal, owns the modal
// surface: input goes to it and a frame left by a departed opener is dropped.
// (Refusing a write from a non-occupant is the Surfaces engine's job.)
func TestModalSurfaceOwnerIsOccupantNotOpener(t *testing.T) {
	m := newModel()
	addPublicationTestPlugin(m, hello{PluginID: "occ", Slots: []protocol.SurfaceID{protocol.SurfaceModal}})
	_, opener := addPublicationTestPlugin(m, hello{PluginID: "opener"})
	m.openModal("opener", "r.opener")
	if got := m.surfaceOwner(protocol.SurfaceModal); got != "occ" {
		t.Fatalf("modal surface owner = %q, want the occupant occ", got)
	}
	m.content("occ", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceModal, Lines: []string{"from-occupant"}})
	m.sendKey(m.surfaceOwner(protocol.SurfaceModal), protocol.SurfaceModal, "x")
	for _, e := range envelopes(t, opener) {
		if e.SlotKey != nil {
			t.Fatalf("opener received a key for the occupant's surface: %+v", e.SlotKey)
		}
	}
	if got := m.slotUpdates[protocol.SurfaceModal]; got == nil || got.PluginID != "occ" || got.Lines[0] != "from-occupant" {
		t.Fatalf("occupant write while another plugin's modal is open = %+v", got)
	}
}

func TestNoStaleModalContentAfterOpenerLeaves(t *testing.T) {
	m := newModel()
	addPublicationTestPlugin(m, hello{PluginID: "occ", Slots: []protocol.SurfaceID{protocol.SurfaceModal}})
	addPublicationTestPlugin(m, hello{PluginID: "opener"})
	m.openModal("opener", "r.opener")
	// A frame the opener published while it occupied the modal.
	m.slotUpdates[protocol.SurfaceModal] = &protocol.SurfaceUpdateParams{PluginID: "opener", Surface: protocol.SurfaceModal, Lines: []string{"stale"}}
	delete(m.plugins, "opener") // what a refresh does when the opener leaves
	m.pruneContent()
	m.dropGoneSurfaces()
	if m.modalOwner().Open() {
		t.Fatal("modal stayed open after its opener left")
	}
	if got := m.slotUpdates[protocol.SurfaceModal]; got != nil {
		t.Fatalf("stale modal content survived: %+v", got)
	}
	m.openModal("occ", "r.occ")
	m.recomputeLayout()
	if out := ansi.Strip(m.View().Content); strings.Contains(out, "stale") {
		t.Fatal("occupant's modal drew the departed opener's content")
	}
}

func TestOverlappingSurfacesClickReachesTopmostOwnerOnly(t *testing.T) {
	for i := 0; i < 50; i++ {
		m := newModel()
		m.width, m.height = 120, 30
		_, canvas := addPublicationTestPlugin(m, hello{PluginID: "canvas", Slots: []protocol.SurfaceID{protocol.SurfaceFullCanvas}})
		_, stage := addPublicationTestPlugin(m, hello{PluginID: "stage", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}})
		r := slotRect{X: 30, Y: 10, Width: 20, Height: 6, InnerX: 30, InnerY: 10, InnerW: 20, InnerH: 6}
		m.slotRects = map[protocol.SurfaceID]slotRect{protocol.SurfaceFullCanvas: r, protocol.SurfaceMainStage: r}
		m.Update(tea.MouseClickMsg{X: 35, Y: 12, Button: tea.MouseLeft})
		if m.focusedSlot != protocol.SurfaceFullCanvas {
			t.Fatalf("iteration %d: focusedSlot = %q, want full-canvas", i, m.focusedSlot)
		}
		count := func(buf *pluginWire, _ string) int {
			n := 0
			for _, e := range envelopes(t, buf) {
				if e.SlotMouse != nil {
					n++
				}
			}
			return n
		}
		if c, s := count(canvas, "canvas"), count(stage, "stage"); c != 1 || s != 0 {
			t.Fatalf("iteration %d: slot_mouse canvas=%d stage=%d, want 1/0", i, c, s)
		}
	}
}
