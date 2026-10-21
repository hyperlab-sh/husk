package main

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func addTestPlugin(m *WorkbenchModel, id string, frames map[huskipc.SlotID]frame.Spec, items ...huskipc.RailItemSpec) {
	var wire map[huskipc.SlotID]huskipc.FrameSpec
	if frames != nil {
		wire = make(map[huskipc.SlotID]huskipc.FrameSpec, len(frames))
		for slot, s := range frames {
			wire[slot] = huskipc.FrameSpec(s)
		}
	}
	m.plugins[id] = &ConnectedPlugin{
		Info:   huskipc.HelloPayload{PluginID: id, Frames: wire},
		Writer: huskipc.NewConnWriter(io.Discard),
	}
	for _, ri := range items {
		ri.PluginID = id
		m.railItems = append(m.railItems, ri)
	}
}

func framedRailItem(id string, spec frame.Spec, placement huskipc.RailPlacement) huskipc.RailItemSpec {
	wire := huskipc.FrameSpec(spec)
	return huskipc.RailItemSpec{ID: id, Icon: "i", Label: id, Placement: placement, Frame: &wire}
}

// A modal declaring a frame only through its rail item must get its chrome
// (regression: frameFor used to consult only the canvas/stage rail item).
func TestFrameForModalRailItemFrameOnly(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	addTestPlugin(m, "p.modal", nil, framedRailItem("r.modal", frame.Spec{Border: true, Label: "L", Close: true}, huskipc.PlacementModal))
	m.openModal("p.modal", "r.modal")
	m.recomputeLayout()

	got := m.frameFor(huskipc.SlotModalSurface)
	if !got.Border || got.Label != "L" || !got.Close {
		t.Fatalf("modal frame from rail item = %+v, want border+label+close", got)
	}
}

// An unframed modal opened over a framed canvas must not inherit the canvas
// frame; the framed canvas keeps its own.
func TestFrameForUnframedModalOverFramedCanvas(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	addTestPlugin(m, "p.canvas", nil, framedRailItem("r.canvas", frame.Spec{Border: true, Close: true}, huskipc.PlacementFullCanvas))
	addTestPlugin(m, "p.modal", nil, framedRailItem("r.modal2", frame.Spec{}, huskipc.PlacementModal))
	m.activeRailID = "r.canvas"
	m.activePlacement = huskipc.PlacementFullCanvas
	m.openModal("p.modal", "r.modal2")
	m.recomputeLayout()

	if got := (m.frameFor(huskipc.SlotModalSurface)); got.Enabled() {
		t.Fatalf("unframed modal inherited frame %+v from framed canvas", got)
	}
	if got := m.frameFor(huskipc.SlotFullCanvas); !got.Border || !got.Close {
		t.Fatalf("canvas frame = %+v, want border+close", got)
	}
}

// hello "frames" takes precedence over the rail item's "frame".
func TestFrameForHelloFramesPrecedence(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	hello := frame.Spec{Border: true, Label: "from-hello"}
	addTestPlugin(m, "p.modal", map[huskipc.SlotID]frame.Spec{huskipc.SlotModalSurface: hello},
		framedRailItem("r.modal3", frame.Spec{Label: "from-rail"}, huskipc.PlacementModal))
	m.openModal("p.modal", "r.modal3")
	m.recomputeLayout()

	got := m.frameFor(huskipc.SlotModalSurface)
	if got.Label != "from-hello" || !got.Border {
		t.Fatalf("frame = %+v, want hello declaration to win", got)
	}
}

// A visible modal must appear in currentSlotSizes with its frame-aware inner
// size, track terminal resizes, and disappear when closed.
func TestCurrentSlotSizesModalLifecycle(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	addTestPlugin(m, "p.modal", nil, framedRailItem("r.framed", frame.Spec{Border: true, Close: true}, huskipc.PlacementModal))
	m.openModal("p.modal", "r.framed")
	m.width, m.height = 120, 40
	m.recomputeLayout()

	sizes := m.currentSlotSizes()
	sz, ok := sizes[huskipc.SlotModalSurface]
	if !ok || !sz.Visible {
		t.Fatalf("open modal missing from slot sizes: %+v", sz)
	}
	if want := m.modalRect.InnerW; sz.Width != want {
		t.Fatalf("framed modal width = %d, want inner %d", sz.Width, want)
	}
	if sz.Width >= m.modalRect.Width {
		t.Fatalf("framed modal width %d not inset from outer %d", sz.Width, m.modalRect.Width)
	}

	// resize while the modal is active: the size payload must follow
	m.width, m.height = 84, 24
	m.recomputeLayout()
	sz = m.currentSlotSizes()[huskipc.SlotModalSurface]
	if sz.Width != m.modalRect.InnerW || sz.Height != m.modalRect.InnerH {
		t.Fatalf("modal size after resize = %dx%d, want inner %dx%d",
			sz.Width, sz.Height, m.modalRect.InnerW, m.modalRect.InnerH)
	}

	// closed modal is absent
	m.closeModal()
	if _, ok := m.currentSlotSizes()[huskipc.SlotModalSurface]; ok {
		t.Fatal("closed modal still reported in slot sizes")
	}

	// an unframed modal reports the full rect
	addTestPlugin(m, "p.modal2", nil, framedRailItem("r.plain", frame.Spec{}, huskipc.PlacementModal))
	m.openModal("p.modal2", "r.plain")
	m.recomputeLayout()
	sz = m.currentSlotSizes()[huskipc.SlotModalSurface]
	if sz.Width != m.modalRect.Width || sz.Height != m.modalRect.Height {
		t.Fatalf("unframed modal size = %dx%d, want full %dx%d",
			sz.Width, sz.Height, m.modalRect.Width, m.modalRect.Height)
	}
}

// Borderless label + close must never overflow the surface: every rendered
// row keeps the surface width (ANSI- and wide-rune-safe), and the close glyph
// occupies the rightmost cell, matching closeHitAt.
func TestRenderFrameBorderlessWidthInvariant(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	cases := []struct {
		name  string
		label string
		close bool
		w     int
	}{
		{"wide label + close", "界界界界界界", true, 8},
		{"long ascii label + close", strings.Repeat("x", 40), true, 12},
		{"label only", "annæ", false, 8},
		{"close only", "", true, 4},
		{"nothing", "", false, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rect := SlotRect{X: 0, Y: 0, Width: tc.w, Height: 3, InnerX: 0, InnerY: 0, InnerW: tc.w, InnerH: 3}
			lines := m.surfaceContentLines(nil, tc.w, 3)
			out := m.renderFrame(huskipc.SlotModalSurface, rect, frame.Spec{Label: tc.label, Close: tc.close}, tc.label, lines)
			rows := strings.Split(out, "\n")
			if len(rows) != 3 {
				t.Fatalf("row count = %d, want 3", len(rows))
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w != tc.w {
					t.Fatalf("row %d width = %d, want %d", i, w, tc.w)
				}
			}
			if tc.close {
				last := ansi.Cut(rows[0], tc.w-1, tc.w)
				if strings.TrimSpace(last) == "" {
					t.Fatalf("close glyph not in rightmost cell: %q", rows[0])
				}
			}
		})
	}
}

// A bordered surface too narrow for the close glyph must omit it rather than
// overflow the border row.
func TestRenderFrameBorderedNarrowClose(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	rect := SlotRect{X: 0, Y: 0, Width: 3, Height: 3, InnerX: 1, InnerY: 1, InnerW: 1, InnerH: 1}
	lines := m.surfaceContentLines(nil, 1, 1)
	out := m.renderFrame(huskipc.SlotMainStage, rect, frame.Spec{Border: true, Label: "Lab", Close: true}, "Lab", lines)
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("row count = %d, want 3", len(rows))
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w != 3 {
			t.Fatalf("row %d width = %d, want 3: %q", i, w, row)
		}
	}
}

// The architecture explorer always fills the main-stage slot exactly, with or
// without a plugin-declared border on the main stage.
func TestExplorerFillsMainStageSlot(t *testing.T) {
	newModel := func() *WorkbenchModel {
		m := newWorkbenchModel("", nil, "")
		m.width, m.height = 144, 40
		m.recomputeLayout()
		return m
	}
	check := func(t *testing.T, m *WorkbenchModel) {
		t.Helper()
		rect := m.slotRects[huskipc.SlotMainStage]
		rows := strings.Split(m.renderHotspotArchitectureStage(), "\n")
		if len(rows) != rect.Height {
			t.Fatalf("explorer rows = %d, want slot height %d", len(rows), rect.Height)
		}
		for i, row := range rows {
			if w := ansi.StringWidth(row); w != rect.Width {
				t.Fatalf("row %d width = %d, want slot width %d", i, w, rect.Width)
			}
		}
	}

	t.Run("unframed main stage", func(t *testing.T) { check(t, newModel()) })

	t.Run("framed main stage", func(t *testing.T) {
		m := newModel()
		addTestPlugin(m, "p.stage", map[huskipc.SlotID]frame.Spec{huskipc.SlotMainStage: {Border: true}})
		m.registry.Occupants[huskipc.SlotMainStage] = "p.stage"
		m.recomputeLayout()
		check(t, m)
	})
}

// While a modal is open, close hit testing must not fall through to the
// underlying canvas/stage close button (esc parity: only the modal goes).
func TestCloseHitAtTopmostModalOnly(t *testing.T) {
	m := newWorkbenchModel("", nil, "")
	addTestPlugin(m, "p.canvas", nil, framedRailItem("r.canvas", frame.Spec{Border: true, Close: true}, huskipc.PlacementFullCanvas))
	addTestPlugin(m, "p.modal", nil, framedRailItem("r.modalx", frame.Spec{Border: true, Close: true}, huskipc.PlacementModal))
	m.activeRailID = "r.canvas"
	m.activePlacement = huskipc.PlacementFullCanvas
	m.width, m.height = 120, 40
	m.recomputeLayout()

	// without a modal, the canvas close is clickable
	crect := m.slotRects[huskipc.SlotFullCanvas]
	cx := crect.X + crect.Width - 2
	if slot, ok := m.closeHitAt(cx, crect.Y); !ok || slot != huskipc.SlotFullCanvas {
		t.Fatalf("canvas close without modal: slot=%v ok=%v", slot, ok)
	}

	// open a framed modal: its close is clickable, the canvas close is not
	m.openModal("p.modal", "r.modalx")
	m.recomputeLayout()
	mrect := m.modalRect
	mx := mrect.X + mrect.Width - 2
	if slot, ok := m.closeHitAt(mx, mrect.Y); !ok || slot != huskipc.SlotModalSurface {
		t.Fatalf("modal close: slot=%v ok=%v", slot, ok)
	}
	if _, ok := m.closeHitAt(cx, crect.Y); ok {
		t.Fatal("click on underlying canvas close hit while modal open")
	}

	// an unframed modal blocks fall-through entirely
	m.railItems = m.railItems[:0]
	addTestPlugin(m, "p.modal2", nil, framedRailItem("r.modaly", frame.Spec{}, huskipc.PlacementModal))
	m.openModal("p.modal2", "r.modaly")
	m.recomputeLayout()
	if _, ok := m.closeHitAt(cx, crect.Y); ok {
		t.Fatal("unframed modal still lets canvas close clicks through")
	}
}
