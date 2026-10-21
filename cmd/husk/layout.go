package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// Minimum terminal size the workbench layout is designed for. Below it the
// host renders a too-small screen instead of a garbled, overflowing frame.
const (
	minTermWidth  = 84
	minTermHeight = 24
)

// tooSmall reports whether the real terminal is below the supported minimum.
func (m *WorkbenchModel) tooSmall() bool {
	return m.width < minTermWidth || m.height < minTermHeight
}

// renderTooSmall returns a frame of exactly m.height lines, each exactly
// m.width cells wide, telling the user the terminal is too small. Message
// variants shrink with the available width and are truncated as a last resort,
// so no line ever exceeds the real terminal.
func (m *WorkbenchModel) renderTooSmall() string {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return ""
	}
	have := fmt.Sprintf("%dx%d", w, h)
	need := fmt.Sprintf("%dx%d", minTermWidth, minTermHeight)
	variants := []string{
		fmt.Sprintf("terminal too small (need %s, have %s)", need, have),
		fmt.Sprintf("too small (need %s, have %s)", need, have),
		fmt.Sprintf("need %s, have %s", need, have),
		fmt.Sprintf("%s < %s", have, need),
		have,
	}
	msg := variants[len(variants)-1]
	for _, v := range variants {
		if ansi.StringWidth(v) <= w {
			msg = v
			break
		}
	}
	msg = ansi.Truncate(msg, w, "")
	pad := (w - ansi.StringWidth(msg)) / 2
	line := strings.Repeat(" ", pad) + msg
	line += strings.Repeat(" ", w-ansi.StringWidth(line))
	blank := strings.Repeat(" ", w)
	lines := make([]string, h)
	for i := range lines {
		lines[i] = blank
	}
	lines[(h-1)/2] = line
	return strings.Join(lines, "\n")
}

func (m *WorkbenchModel) recomputeLayout() {
	totalW := max(minTermWidth, m.width)
	bodyH := max(20, m.height-2)

	m.slotRects = make(map[huskipc.SlotID]SlotRect)

	modalW := min(92, m.width-10)
	modalH := min(24, m.height-6)
	m.modalRect = m.frameRect(huskipc.SlotModalSurface, max(4, (m.width-modalW)/2), max(2, (m.height-modalH)/2), modalW, modalH)

	railW := activityRailWidth
	m.slotRects[huskipc.SlotActivityRail] = m.frameRect(huskipc.SlotActivityRail, 0, 1, railW, bodyH)

	remW := max(40, totalW-railW)

	if m.activePlacement == huskipc.PlacementFullCanvas {
		m.slotRects[huskipc.SlotFullCanvas] = m.frameRect(huskipc.SlotFullCanvas, railW, 1, remW, bodyH)
		return
	}

	leftW := 0
	if m.showSidebar {
		leftW = min(m.sidebarWidth, remW/2)
		navH := max(3, m.sidebarNavH)
		footH := max(3, m.sidebarFootH)
		if navH+footH+6 > bodyH {
			navH = 4
			footH = 4
		}
		listH := max(6, bodyH-navH-footH)

		m.slotRects[huskipc.SlotSidebarNavigation] = m.frameRect(huskipc.SlotSidebarNavigation, railW, 1, leftW, navH)
		m.slotRects[huskipc.SlotSidebarList] = m.frameRect(huskipc.SlotSidebarList, railW, 1+navH, leftW, listH)
		m.slotRects[huskipc.SlotSidebarFooter] = m.frameRect(huskipc.SlotSidebarFooter, railW, 1+navH+listH, leftW, footH)
	}

	if m.activePlacement == huskipc.PlacementStage {
		stageW := max(30, remW-leftW)
		m.slotRects[huskipc.SlotWorkspaceStage] = m.frameRect(huskipc.SlotWorkspaceStage, railW+leftW, 1, stageW, bodyH)
		return
	}

	rightW := 0
	if m.showSidePane && remW-leftW >= 72 {
		rightW = min(m.sidePaneW, (remW-leftW)/2)
	}
	centerW := max(30, remW-leftW-rightW)

	hdrH := max(3, m.mainHeaderH)
	if hdrH+8 > bodyH {
		hdrH = 4
	}
	stageH := max(8, bodyH-hdrH)
	m.slotRects[huskipc.SlotMainHeader] = m.frameRect(huskipc.SlotMainHeader, railW+leftW, 1, centerW, hdrH)
	m.slotRects[huskipc.SlotMainStage] = m.frameRect(huskipc.SlotMainStage, railW+leftW, 1+hdrH, centerW, stageH)

	if rightW > 0 {
		m.slotRects[huskipc.SlotSidePanel] = m.frameRect(huskipc.SlotSidePanel, railW+leftW+centerW, 1, rightW, bodyH)
	}
}

// frameRect builds a SlotRect whose inner (content) area is inset by one cell
// per axis only when the surface's declared frame has a border.
func (m *WorkbenchModel) frameRect(slotID huskipc.SlotID, x, y, w, h int) SlotRect {
	r := SlotRect{
		X: x, Y: y, Width: w, Height: h,
		InnerX: x, InnerY: y, InnerW: max(1, w), InnerH: max(1, h),
	}
	if m.frameFor(slotID).Border {
		ix, iy, iw, ih := frame.InnerRect(x, y, w, h, m.frameFor(slotID))
		r.InnerX, r.InnerY, r.InnerW, r.InnerH = ix, iy, iw, ih
	}
	return r
}

// frameFor resolves the frame spec for a surface: the demo override first,
// then the occupying plugin's hello "frames" declaration, then the "frame"
// field of the rail item that opened the surface (the active modal item for
// the modal surface, the active canvas/stage item otherwise). Absent means a
// blank canvas.
func (m *WorkbenchModel) frameFor(slotID huskipc.SlotID) frame.Spec {
	if m.demoFrame {
		if s, ok := demoFrameSpecs[slotID]; ok {
			return s
		}
	}
	owner := ""
	railID := ""
	switch slotID {
	case huskipc.SlotModalSurface:
		owner = m.activeModalPlugin
		railID = m.activeModalRailID
	case huskipc.SlotFullCanvas, huskipc.SlotWorkspaceStage:
		railID = m.activeRailID
	}
	if railID != "" {
		if ri, ok := m.railItemByID(railID); ok {
			owner = ri.PluginID
		}
	}
	if owner == "" {
		owner = m.slotOwners[slotID]
	}
	if owner != "" {
		if cp, ok := m.plugins[owner]; ok {
			if s, ok := cp.Info.Frames[slotID]; ok {
				return s
			}
		}
	}
	if railID != "" {
		if ri, ok := m.railItemByID(railID); ok && ri.Frame != nil {
			return *ri.Frame
		}
	}
	return frame.Spec{}
}

// railItemByID finds a registered rail item by ID.
func (m *WorkbenchModel) railItemByID(id string) (huskipc.RailItemSpec, bool) {
	if id == "" {
		return huskipc.RailItemSpec{}, false
	}
	for _, ri := range m.railItems {
		if ri.ID == id {
			return ri, true
		}
	}
	return huskipc.RailItemSpec{}, false
}

func (m *WorkbenchModel) activeRailItem() (huskipc.RailItemSpec, bool) {
	return m.railItemByID(m.activeRailID)
}

func (m *WorkbenchModel) currentSlotSizes() map[huskipc.SlotID]huskipc.SlotSize {
	out := make(map[huskipc.SlotID]huskipc.SlotSize)
	for id, r := range m.slotRects {
		out[id] = huskipc.SlotSize{
			Width:   r.InnerW,
			Height:  r.InnerH,
			Focused: m.focusedSlot == id,
			Visible: true,
		}
	}
	// A visible modal is a surface too: report its (frame-aware) inner size so
	// slot_resize after a terminal resize reaches the modal plugin. Absent
	// while no modal is open.
	if m.activeModalPlugin != "" {
		out[huskipc.SlotModalSurface] = huskipc.SlotSize{
			Width:   m.modalRect.InnerW,
			Height:  m.modalRect.InnerH,
			Focused: m.focusedSlot == huskipc.SlotModalSurface,
			Visible: true,
		}
	}
	return out
}

func (m *WorkbenchModel) broadcastSlotResize() {
	sizes := m.currentSlotSizes()
	for _, cp := range m.plugins {
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "slot_resize",
			SlotResize: &huskipc.SlotResizePayload{
				SlotSizes:   sizes,
				FocusedSlot: m.focusedSlot,
			},
		})
	}
}
