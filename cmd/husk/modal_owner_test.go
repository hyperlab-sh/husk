package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func TestModalOwnerSingleSourceOfTruth(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	if mo := m.modalOwner(); mo.Open() || mo.PluginID != "" || mo.RailID != "" {
		t.Fatalf("fresh model modal = %+v, want closed", mo)
	}
	m.registry.Occupants[huskipc.SlotModalSurface] = "p"
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
	if got := m.surfaceOwner(huskipc.SlotModalSurface); got != "p" {
		t.Fatalf("closed modal surface owner = %q, want the registry occupant p", got)
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

// The modal surface is written only by its registry occupant, whichever plugin's
// rail item opened the modal.
func TestModalSurfaceWritesFollowOccupantNotOpener(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "occ", Slots: []huskipc.SlotID{huskipc.SlotModalSurface}})
	_, opener := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "opener"})
	m.openModal("opener", "r.opener")
	update := func(id, line string) {
		m.Update(ipcEventMsg{pluginID: id, env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: huskipc.SlotModalSurface, Lines: []string{line}}}})
	}
	update("opener", "from-opener")
	if got := m.slotUpdates[huskipc.SlotModalSurface]; got != nil {
		t.Fatalf("non-occupant opener wrote the modal surface: %+v", got)
	}
	var rejected bool
	for _, e := range envelopes(t, opener) {
		if e.Type == "protocol_error" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("non-occupant opener was not told its write was rejected")
	}
	update("occ", "from-occupant")
	if got := m.slotUpdates[huskipc.SlotModalSurface]; got == nil || got.PluginID != "occ" || len(got.Lines) != 1 || got.Lines[0] != "from-occupant" {
		t.Fatalf("occupant write while another plugin's modal is open = %+v", got)
	}
}

func TestNoStaleModalContentAfterOpenerDisconnects(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "occ", Slots: []huskipc.SlotID{huskipc.SlotModalSurface}})
	cpOpener, _ := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "opener"})
	m.openModal("opener", "r.opener")
	m.Update(ipcEventMsg{pluginID: "opener", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: huskipc.SlotModalSurface, Lines: []string{"stale"}}}})
	m.disconnectPublications(cpOpener)
	if m.modalOwner().Open() {
		t.Fatal("modal stayed open after its opener disconnected")
	}
	if got := m.slotUpdates[huskipc.SlotModalSurface]; got != nil {
		t.Fatalf("stale modal content survived: %+v", got)
	}
	m.openModal("occ", "r.occ")
	m.recomputeLayout()
	if out := ansi.Strip(m.View().Content); strings.Contains(out, "stale") {
		t.Fatal("occupant's modal drew the disconnected opener's content")
	}
}

func TestOverlappingSurfacesClickReachesTopmostOwnerOnly(t *testing.T) {
	for i := 0; i < 50; i++ {
		m := newWorkbenchModel("", nil, t.TempDir())
		m.width, m.height = 120, 30
		_, canvas := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "canvas", Slots: []huskipc.SlotID{huskipc.SlotFullCanvas}})
		_, stage := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "stage", Slots: []huskipc.SlotID{huskipc.SlotMainStage}})
		r := SlotRect{X: 30, Y: 10, Width: 20, Height: 6, InnerX: 30, InnerY: 10, InnerW: 20, InnerH: 6}
		m.slotRects = map[huskipc.SlotID]SlotRect{huskipc.SlotFullCanvas: r, huskipc.SlotMainStage: r}
		m.Update(tea.MouseClickMsg{X: 35, Y: 12, Button: tea.MouseLeft})
		if m.focusedSlot != huskipc.SlotFullCanvas {
			t.Fatalf("iteration %d: focusedSlot = %q, want full-canvas", i, m.focusedSlot)
		}
		count := func(buf *bytes.Buffer, _ string) int {
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
