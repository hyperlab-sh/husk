package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Minimum terminal size the workbench layout is designed for. Below it the
// host renders a too-small screen instead of a garbled, overflowing frame.
const (
	minTermWidth = 84
	// minPanelCentreWidth is the centre column width, beside the sidebar,
	// below which the side panel is not drawn.
	minPanelCentreWidth = 72
	minTermHeight       = 24
)

// surfaceGutter is the number of blank columns between the centre column
// (main header and stage) and the side panel, so text clipped at the right
// edge of the centre never touches the panel's first column. It is carved from
// the centre; sidePaneW is unchanged. The gutter is inert (it belongs to no
// slot rect, so clicks and wheel events on it reach no surface) and the
// sidepanel-width divider band spans the centre's right edge, the gutter and
// the panel's left edge. The sidebar needs none:
// its rects are the sidebar width exactly (the resize drag relies on that) and
// its lines are short row labels.
const surfaceGutter = 1

// tooSmall reports whether the real terminal is below the supported minimum.
func (m *WorkbenchModel) tooSmall() bool {
	return m.width < minTermWidth || m.height < minTermHeight
}

// layoutFrozen reports whether layout and slot_resize traffic must stay
// untouched: the terminal size is known and below the minimum. An unknown
// (zero) size is not frozen so startup and headless paths still lay out.
func (m *WorkbenchModel) layoutFrozen() bool {
	return m.width > 0 && m.height > 0 && m.tooSmall()
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
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, msg)
}

// Side docks are preferences, independent of surface occupancy.
func (m *WorkbenchModel) leftSideShown() bool {
	return m.showSidebar
}

func (m *WorkbenchModel) rightSideShown() bool {
	return m.showSidePane
}

// sideWidths derives the body width right of the rail and the capped sidebar
// width shared by layout and panel-fit hints.
func sideWidths(width, sidebarWidth int, sidebarShown bool) (remW, leftW int) {
	remW = max(40, max(minTermWidth, width)-activityRailWidth)
	if sidebarShown {
		leftW = min(sidebarWidth, remW/2)
	}
	return remW, leftW
}

func panelFitsIn(remW, leftW int) bool {
	return remW-leftW >= minPanelCentreWidth
}

// panelFits answers whether showing the panel would change the frame, including
// when its dock is hidden. Layout passes its shared widths to panelFitsIn.
func panelFits(width, sidebarWidth int, sidebarShown bool) bool {
	remW, leftW := sideWidths(width, sidebarWidth, sidebarShown)
	return panelFitsIn(remW, leftW)
}

// dropVanishedSides cancels a drag on a side column that is no longer shown
// (its dock was hidden, its placement changed, or the panel no longer fits).
func (m *WorkbenchModel) dropVanishedSides() {
	left := m.leftSideShown() && m.activePlacement != contracts.PlacementFullCanvas
	right := m.rightSideShown() && m.activePlacement == "" && panelFits(m.width, m.sidebarWidth, m.leftSideShown())
	if !left && strings.HasPrefix(m.activeDrag, "sidebar-") || !right && m.activeDrag == "sidepanel-width" {
		m.activeDrag, m.hoverDivider = "", ""
	}
}

// leaveUndrawnSides moves focus off a side slot that was on screen in prev
// (the rects before this layout) and is not drawn now: its dock was hidden,
// or the terminal got too narrow for the panel. A slot that never had a rect
// keeps focus. The slot we left is remembered and restored when it is drawn
// again, unless focus has moved on since.
func (m *WorkbenchModel) leaveUndrawnSides(prev map[protocol.SurfaceID]SlotRect) {
	switch m.focusedSlot {
	case protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList, protocol.SurfaceSidebarFooter, protocol.SurfaceSidePanel:
	default:
		return
	}
	_, hadFocused := prev[m.focusedSlot]
	_, drawn := m.slotRects[m.focusedSlot]
	if hadFocused && !drawn {
		m.clearPending()
		m.focusFallback, m.focusedSlot = m.focusedSlot, m.centreSlot()
	}
}

// centreSlot is the surface focus falls back to when a side column goes.
func (m *WorkbenchModel) centreSlot() protocol.SurfaceID {
	if m.activePlacement == contracts.PlacementStage {
		return protocol.SurfaceWorkspaceStage
	}
	return protocol.SurfaceMainStage
}

// restoreFocusFallback gives focus back to the slot it was pushed off, once
// that slot is actually drawn (it has a rect in the freshly built layout) and
// focus has not moved on. A stage placement never draws the side panel.
func (m *WorkbenchModel) restoreFocusFallback() {
	fb := m.focusFallback
	if fb == "" || m.focusedSlot != m.centreSlot() {
		return
	}
	if _, drawn := m.slotRects[fb]; !drawn {
		return
	}
	m.clearPending()
	m.focusedSlot, m.focusFallback = fb, ""
}

func (m *WorkbenchModel) recomputeLayout() {
	if m.layoutFrozen() {
		return
	}
	m.dropVanishedSides()
	prev := m.slotRects
	m.placeSlots()
	m.leaveUndrawnSides(prev)
	m.restoreFocusFallback()
}

// placeSlots rebuilds slotRects and the other layout rects.
func (m *WorkbenchModel) placeSlots() {
	bodyH := max(20, m.height-2)

	m.slotRects = make(map[protocol.SurfaceID]SlotRect)

	modalW := min(92, m.width-10)
	modalH := min(24, m.height-6)
	m.modalRect = m.frameRect(protocol.SurfaceModal, max(4, (m.width-modalW)/2), max(2, (m.height-modalH)/2), modalW, modalH)

	railW := activityRailWidth
	m.slotRects[protocol.SurfaceActivityRail] = m.frameRect(protocol.SurfaceActivityRail, 0, 1, railW, bodyH)

	remW, leftW := sideWidths(m.width, m.sidebarWidth, m.leftSideShown())

	if m.activePlacement == contracts.PlacementFullCanvas {
		m.slotRects[protocol.SurfaceFullCanvas] = m.frameRect(protocol.SurfaceFullCanvas, railW, 1, remW, bodyH)
		return
	}

	if m.leftSideShown() {
		navH := max(3, m.sidebarNavH)
		footH := max(3, m.sidebarFootH)
		if navH+footH+6 > bodyH {
			navH = 4
			footH = 4
		}
		listH := max(6, bodyH-navH-footH)

		m.slotRects[protocol.SurfaceSidebarNavigation] = m.frameRect(protocol.SurfaceSidebarNavigation, railW, 1, leftW, navH)
		m.slotRects[protocol.SurfaceSidebarList] = m.frameRect(protocol.SurfaceSidebarList, railW, 1+navH, leftW, listH)
		m.slotRects[protocol.SurfaceSidebarFooter] = m.frameRect(protocol.SurfaceSidebarFooter, railW, 1+navH+listH, leftW, footH)
	}

	if m.activePlacement == contracts.PlacementStage {
		stageW := max(30, remW-leftW)
		m.slotRects[protocol.SurfaceWorkspaceStage] = m.frameRect(protocol.SurfaceWorkspaceStage, railW+leftW, 1, stageW, bodyH)
		return
	}

	rightW := 0
	if m.rightSideShown() && panelFitsIn(remW, leftW) {
		rightW = min(m.sidePaneW, (remW-leftW)/2)
	}
	centerW := max(30, remW-leftW-rightW)

	hdrH := max(3, m.mainHeaderH)
	if hdrH+8 > bodyH {
		hdrH = 4
	}
	stageH := max(8, bodyH-hdrH)
	centerRectW := centerW
	if rightW > 0 {
		centerRectW -= surfaceGutter
	}
	m.slotRects[protocol.SurfaceMainHeader] = m.frameRect(protocol.SurfaceMainHeader, railW+leftW, 1, centerRectW, hdrH)
	m.slotRects[protocol.SurfaceMainStage] = m.frameRect(protocol.SurfaceMainStage, railW+leftW, 1+hdrH, centerRectW, stageH)

	if rightW > 0 {
		m.slotRects[protocol.SurfaceSidePanel] = m.frameRect(protocol.SurfaceSidePanel, railW+leftW+centerW, 1, rightW, bodyH)
	}
}

// frameRect builds a SlotRect whose inner (content) area is inset by one cell
// per axis only when the surface's declared frame has a border.
func (m *WorkbenchModel) frameRect(slotID protocol.SurfaceID, x, y, w, h int) SlotRect {
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
// then the occupying plugin's declared surface frame, then the "frame"
// field of the rail item that opened the surface (the active modal item for
// the modal surface, the active canvas/stage item otherwise). Absent means a
// blank canvas.
func (m *WorkbenchModel) frameFor(slotID protocol.SurfaceID) frame.Spec {
	if m.demoFrame {
		if s, ok := demoFrameSpecs[slotID]; ok {
			return frame.Spec(s)
		}
	}
	owner := ""
	railID := ""
	switch slotID {
	case protocol.SurfaceModal:
		owner = m.modalOwner().PluginID
		railID = m.modalOwner().RailID
	case protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage:
		railID = m.activeRailID
	}
	if railID != "" {
		if ri, ok := m.railItemByID(railID); ok {
			owner = ri.Plugin
		}
	}
	if owner == "" {
		owner = m.surfaceOwner(slotID)
	}
	if owner != "" {
		if s, ok := m.catalog.Frames[owner][slotID]; ok {
			return frame.Spec(s)
		}
	}
	if railID != "" {
		if ri, ok := m.railItemByID(railID); ok && ri.Frame != nil {
			return frame.Spec(*ri.Frame)
		}
	}
	return frame.Spec{}
}

// slotHit is one laid-out surface in hit-test order.
type slotHit struct {
	ID   protocol.SurfaceID
	Rect SlotRect
}

// slotZ is the explicit stacking order of laid-out surfaces; higher is drawn
// above and hit first. Placement surfaces sit above the regions they replace,
// which sit above the activity rail.
func slotZ(id protocol.SurfaceID) int {
	switch id {
	case protocol.SurfaceFullCanvas:
		return 30
	case protocol.SurfaceWorkspaceStage:
		return 20
	case protocol.SurfaceActivityRail:
		return 0
	default:
		return 10
	}
}

// hitOrder lists the laid-out surfaces topmost first: descending slotZ, ties
// broken by slot ID, so overlapping rects always resolve to the same surface.
func (m *WorkbenchModel) hitOrder() []slotHit {
	hits := make([]slotHit, 0, len(m.slotRects))
	for id, r := range m.slotRects {
		hits = append(hits, slotHit{ID: id, Rect: r})
	}
	slices.SortFunc(hits, func(a, b slotHit) int {
		return cmp.Or(cmp.Compare(slotZ(b.ID), slotZ(a.ID)), cmp.Compare(a.ID, b.ID))
	})
	return hits
}

// railItemByID finds a registered rail item by ID.
func (m *WorkbenchModel) railItemByID(id string) (contracts.RailItem, bool) {
	if id == "" {
		return contracts.RailItem{}, false
	}
	for _, ri := range m.railItems {
		if ri.ID == id {
			return ri, true
		}
	}
	return contracts.RailItem{}, false
}

func (m *WorkbenchModel) activeRailItem() (contracts.RailItem, bool) {
	return m.railItemByID(m.activeRailID)
}
