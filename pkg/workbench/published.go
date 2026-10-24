package workbench

import (
	"image"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// publishedContent composes regions even before the publisher's first frame.
func (m *workbenchModel) publishedContent(surface protocol.SurfaceID, width, height int) publishedLayout {
	var source []string
	if su := m.slotUpdates[surface]; su != nil {
		source = su.Lines
	}
	return m.catalog.layoutPublished(surface, m.surfaceOwner(surface), source, width, height)
}

// clickContributionAt lets visible content win over overlapping resize hit
// zones on borderless surfaces. Borders and blank cells remain draggable.
func (m *workbenchModel) clickContributionAt(x, y int) bool {
	for _, hit := range m.hitOrder() {
		slot, rect := hit.ID, hit.Rect
		if slot == protocol.SurfaceActivityRail {
			continue
		}
		relX, relY := x-rect.InnerX, y-rect.InnerY
		if relX < 0 || relY < 0 || relX >= rect.InnerW || relY >= rect.InnerH {
			continue
		}
		if !m.clickContribution(slot, relX, relY, rect.InnerW, rect.InnerH) {
			continue
		}
		switch slot {
		case protocol.SurfaceSidebarList, protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceWorkspaceStage, protocol.SurfaceFullCanvas:
			m.setFocus(slot)
		}
		return true
	}
	return false
}

// clickContribution re-renders from current state rather than trusting a stale
// frame hit map after a resize, registry mutation or plugin disconnect.
func (m *workbenchModel) clickContribution(surface protocol.SurfaceID, x, y, width, height int) bool {
	if surface == protocol.SurfaceMainStage && m.stageMode == "hotspots" {
		return false // architecture view hides the publisher's content
	}
	su := m.slotUpdates[surface]
	// Borderless frames overlay chrome on content row zero. Do not dispatch
	// commands for contribution cells hidden by a label or close glyph.
	if spec := m.frameFor(surface); y == 0 && !spec.Border {
		available := width
		if spec.Close {
			available--
			if x >= available {
				return false
			}
		}
		label := spec.Label
		if label != "" && su != nil && su.Title != "" {
			label = su.Title
		}
		if x < ansi.StringWidth(ansi.Truncate(label, max(0, available), "…")) {
			return false
		}
	}
	hits := m.publishedContent(surface, width, height).Hits
	for i := len(hits) - 1; i >= 0; i-- {
		h := hits[i]
		if h.Y == y && x >= h.X && x < h.X+h.Width {
			if h.CommandID == "" || !m.plugins[h.PluginID] {
				return false
			}
			m.run(h.CommandID, map[string]any{"slot_id": h.SlotID, "contribution_id": h.ContributionID, "row_key": h.RowKey})
			return true
		}
	}
	return false
}

// publisherMousePayload resolves row metadata in content-relative coordinates
// for every publisher input path, including modal clicks and wheel events.
func (m *workbenchModel) publisherMousePayload(surface protocol.SurfaceID, x, y int, button string) protocol.SurfaceMouseParams {
	payload := protocol.SurfaceMouseParams{Surface: surface, X: x, Y: y, Button: button}
	if su := m.slotUpdates[surface]; su != nil {
		for _, hit := range su.HitTargets {
			if hit.Line == y && (hit.ColStart == nil || x >= *hit.ColStart && x < *hit.ColEnd) {
				payload.HitID, payload.HitKind, payload.HitAction, payload.HitPayload = hit.ID, hit.Kind, hit.Action, hit.Payload
				break
			}
		}
	}
	return payload
}

// Modal content uses the same frame-aware inner rect as rendering. Borders
// consume one cell per edge; labels/close glyphs never add a content title row.
func (m *workbenchModel) handlePublishedModalMouse(x, y int, left, wheelDown, wheelUp bool) bool {
	rect := m.modalRect
	if !image.Pt(x, y).In(rect.Rect()) {
		return false
	}
	x, y = x-rect.InnerX, y-rect.InnerY
	innerW, innerH := rect.InnerW, rect.InnerH
	if x < 0 || y < 0 || x >= innerW || y >= innerH {
		return true
	}
	if left && m.clickContribution(protocol.SurfaceModal, x, y, innerW, innerH) {
		return true
	}
	button := "left"
	if wheelDown {
		button = "wheel_down"
	} else if wheelUp {
		button = "wheel_up"
	}
	m.sendMouse(m.surfaceOwner(protocol.SurfaceModal), m.publisherMousePayload(protocol.SurfaceModal, x, y, button))
	return true
}
