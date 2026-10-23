package main

import (
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/hyperlab-sh/husk/pkg/theme"
)

// The app background is painted: the view asks the terminal for the Canvas
// colour (Bubble Tea resets it on exit), and every cell no surface or overlay
// coloured takes Canvas too, so terminals without OSC 11 look the same. The
// slot shade is derived from that colour and differs from it in the expected
// direction.
func TestViewPaintsAppBackground(t *testing.T) {
	m := pureViewModel("plugin", false)
	canvas := m.palette.Canvas
	view := m.View()
	if !sameColor(view.BackgroundColor, canvas) {
		t.Fatalf("view background = %v, want canvas %v", view.BackgroundColor, m.palette.Canvas)
	}
	rows := frameCells(view.Content)
	for y, row := range rows {
		for x, cell := range row {
			if cell.Style.Bg == nil && cell.Width > 0 {
				t.Fatalf("cell (%d,%d) %q has the terminal default background", x, y, cell.Content)
			}
		}
	}
	// the slot content area is the derived shade, not the canvas
	rect := m.slotRects["main-stage"]
	slot := m.palette.Slot
	if got := rows[rect.InnerY+rect.InnerH-1][rect.InnerX+rect.InnerW-1].Style.Bg; !sameColor(got, slot) || sameColor(got, canvas) {
		t.Errorf("blank stage cell bg = %v, want slot %v distinct from canvas", got, m.palette.Slot)
	}
}

func TestViewTooSmallKeepsAppBackground(t *testing.T) {
	m := pureViewModel("plugin", false)
	m.width, m.height = 40, 10
	if !m.tooSmall() {
		t.Fatal("model should be too small")
	}
	if got := m.View().BackgroundColor; !sameColor(got, m.palette.Canvas) {
		t.Errorf("too-small view background = %v, want canvas", got)
	}
}

// The host's palette carries the derived slot shade. (Direction, lighter on
// dark and darker on light, is asserted in pkg/theme.)
func TestHostPaletteSlotIsDerivedFromCanvas(t *testing.T) {
	p := theme.Default()
	if theme.SlotBackground(p.Canvas) != p.Slot || p.Slot == p.Canvas {
		t.Errorf("palette slot %s is not derived from canvas %s", p.Slot, p.Canvas)
	}
}

// The modal surface is a slot too: its content area is painted, and a cell
// the plugin coloured keeps its colour. Fails if drawModal stops filling.
func TestViewPaintsModalSlotBackground(t *testing.T) {
	m := pureViewModel("plugin", true)
	m.activeToast, m.showPalette = nil, false
	rect := m.modalRect
	if rect.InnerW < 10 || rect.InnerH < 3 {
		t.Fatalf("modal inner rect too small: %+v", rect)
	}
	slot := m.palette.Slot
	rows := frameCells(m.View().Content)
	if got := rows[rect.InnerY][rect.InnerX].Style.Bg; !sameColor(got, lipgloss.Color("2")) {
		t.Errorf("plugin green background replaced: %v", got)
	}
	for _, p := range [][2]int{{rect.InnerX + 6, rect.InnerY}, {rect.InnerX, rect.InnerY + 2}, {rect.InnerX + rect.InnerW - 1, rect.InnerY + rect.InnerH - 1}} {
		if got := rows[p[1]][p[0]].Style.Bg; !sameColor(got, slot) {
			t.Errorf("modal cell %v bg = %v, want slot", p, got)
		}
	}
}

// The architecture explorer replaces the main-stage content and is a slot
// content area as well.
func TestViewPaintsExplorerSlotBackground(t *testing.T) {
	m := pureViewModel("hotspots", false)
	m.showHotspots = false
	rect := m.slotRects["main-stage"]
	slot := m.palette.Slot
	rows := frameCells(m.View().Content)
	x, y := rect.X+rect.Width-2, rect.Y+rect.Height-2 // last cell inside the explorer's own border
	if got := rows[y][x].Style.Bg; !sameColor(got, slot) {
		t.Errorf("explorer blank cell (%d,%d) bg = %v, want slot", x, y, got)
	}
}
