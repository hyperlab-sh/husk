package main

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

type pluginDisconnectedMsg struct{ plugin *ConnectedPlugin }

func (m *WorkbenchModel) deliverPublications(deliveries []registryDelivery) {
	for _, d := range deliveries {
		if cp := m.plugins[d.PluginID]; cp != nil {
			_ = cp.Writer.Send(d.Envelope)
		}
	}
}

// Hello registration is atomic from the perspective of other plugins. A failed
// declaration rolls back this new plugin before it is added to m.plugins.
func (m *WorkbenchModel) registerPublications(hello huskipc.HelloPayload) (err error) {
	id := hello.PluginID
	if !validPluginID(id) || id == "husk" {
		return fmt.Errorf("invalid or reserved plugin_id")
	}
	if m.plugins[id] != nil {
		return fmt.Errorf("plugin_id is already connected")
	}
	defer func() {
		if err != nil {
			m.registry.removePlugin(id)
		}
	}()
	for _, s := range hello.Slots {
		if err = m.registry.claimSurface(id, s); err != nil {
			return err
		}
	}
	for _, s := range hello.PublishedSlots {
		if err = m.registry.declareSlot(id, s); err != nil {
			return err
		}
	}
	for _, h := range hello.Hooks {
		if err = declareNamed(m.registry.Hooks, id, h.ID); err != nil {
			return err
		}
	}
	for _, e := range hello.Events {
		if err = declareNamed(m.registry.Events, id, e.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *WorkbenchModel) rejectPublication(cp *ConnectedPlugin, typ string, err error) {
	_ = cp.Writer.Send(huskipc.Envelope{Type: "protocol_error", ProtocolError: &huskipc.ProtocolErrorPayload{MessageType: typ, Message: err.Error()}})
}

func (m *WorkbenchModel) routePublication(plugin string, env huskipc.Envelope) bool {
	var deliveries []registryDelivery
	var err error
	switch env.Type {
	case "slot_declare":
		if env.SlotDeclare == nil {
			err = fmt.Errorf("slot_declare payload required")
			break
		}
		err = m.registry.declareSlot(plugin, *env.SlotDeclare)
		if err == nil {
			deliveries = []registryDelivery{m.registry.slotDelivery(env.SlotDeclare.ID)}
		}
	case "slot_contribute":
		if env.SlotContribute == nil {
			err = fmt.Errorf("slot_contribute payload required")
			break
		}
		deliveries, err = m.registry.contribute(plugin, *env.SlotContribute)
	case "hook_declare":
		if env.HookDeclare == nil {
			err = fmt.Errorf("hook_declare payload required")
			break
		}
		err = declareNamed(m.registry.Hooks, plugin, env.HookDeclare.ID)
	case "hook_subscribe":
		if env.HookSubscribe == nil {
			err = fmt.Errorf("hook_subscribe payload required")
			break
		}
		err = subscribe(m.registry.Hooks, plugin, env.HookSubscribe.ID)
	case "hook_call":
		if env.HookCall == nil {
			err = fmt.Errorf("hook_call payload required")
			break
		}
		deliveries, err = m.registry.callHook(plugin, *env.HookCall)
	case "hook_result":
		if env.HookResult == nil {
			err = fmt.Errorf("hook_result payload required")
			break
		}
		deliveries, err = m.registry.hookResult(plugin, *env.HookResult)
	case "event_declare":
		if env.EventDeclare == nil {
			err = fmt.Errorf("event_declare payload required")
			break
		}
		err = declareNamed(m.registry.Events, plugin, env.EventDeclare.ID)
	case "event_subscribe":
		if env.EventSubscribe == nil {
			err = fmt.Errorf("event_subscribe payload required")
			break
		}
		err = subscribe(m.registry.Events, plugin, env.EventSubscribe.ID)
	case "event_emit":
		if env.EventEmit == nil {
			err = fmt.Errorf("event payload required")
			break
		}
		deliveries, err = m.registry.emitEvent(plugin, *env.EventEmit)
	default:
		return false
	}
	if err != nil {
		m.rejectPublication(m.plugins[plugin], env.Type, err)
	} else {
		m.deliverPublications(deliveries)
	}
	return true
}

func (m *WorkbenchModel) disconnectPublications(cp *ConnectedPlugin) {
	id := cp.Info.PluginID
	if m.plugins[id] != cp {
		return
	} // stale EOF cannot remove a newer connection
	delete(m.plugins, id)
	for slot, owner := range m.slotOwners {
		if owner == id {
			delete(m.slotUpdates, slot)
		}
	}
	m.deliverPublications(m.registry.removePlugin(id))
	kept := m.railItems[:0]
	for _, item := range m.railItems {
		if item.PluginID != id {
			kept = append(kept, item)
		}
	}
	m.railItems = kept
	if m.activeModalPlugin == id {
		m.activeModalPlugin = ""
		m.activeModalRailID = ""
	}
	activeExists := false
	for _, item := range m.railItems {
		if item.ID == m.activeRailID {
			activeExists = true
		}
	}
	if m.activeRailID != "" && !activeExists {
		m.activateHomeWorkbench()
		return
	}
	// Removing Hello frame declarations changes inner geometry even when the
	// disconnected occupant never owned a rail item.
	m.recomputeLayout()
	m.broadcastSlotResize()
}

// publishedContent composes regions even before the publisher's first frame.
func (m *WorkbenchModel) publishedContent(surface huskipc.SlotID, width, height int) ([]string, []contributionHit) {
	var source []string
	if su := m.slotUpdates[surface]; su != nil {
		source = su.Lines
	}
	return m.registry.renderPublished(surface, m.slotOwners[surface], source, width, height)
}

// clickContributionAt lets visible content win over overlapping resize hit
// zones on borderless surfaces. Borders and blank cells remain draggable.
func (m *WorkbenchModel) clickContributionAt(x, y int) bool {
	for slot, rect := range m.slotRects {
		if slot == huskipc.SlotActivityRail {
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
		case huskipc.SlotSidebarList, huskipc.SlotMainStage, huskipc.SlotSidePanel, huskipc.SlotWorkspaceStage, huskipc.SlotFullCanvas:
			m.focusedSlot = slot
			m.broadcastSlotResize()
		}
		return true
	}
	return false
}

// clickContribution re-renders from current state rather than trusting a stale
// frame hit map after a resize, registry mutation or plugin disconnect.
func (m *WorkbenchModel) clickContribution(surface huskipc.SlotID, x, y, width, height int) bool {
	if surface == huskipc.SlotMainStage && m.stageMode == "hotspots" {
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
	_, hits := m.publishedContent(surface, width, height)
	for i := len(hits) - 1; i >= 0; i-- {
		h := hits[i]
		if h.Y == y && x >= h.X && x < h.X+h.Width {
			cp := m.plugins[h.PluginID]
			if cp == nil || h.CommandID == "" {
				return false
			}
			_ = cp.Writer.Send(huskipc.Envelope{Type: "command_invoke", CommandInvoke: &huskipc.CommandInvokePayload{CommandID: h.CommandID, Args: map[string]string{"slot_id": h.SlotID, "contribution_id": h.ContributionID, "row_key": h.RowKey}}})
			return true
		}
	}
	return false
}

// publisherMousePayload resolves row metadata in content-relative coordinates
// for every publisher input path, including modal clicks and wheel events.
func (m *WorkbenchModel) publisherMousePayload(surface huskipc.SlotID, x, y int, button string) *huskipc.SlotMousePayload {
	payload := &huskipc.SlotMousePayload{Slot: surface, X: x, Y: y, Button: button}
	if su := m.slotUpdates[surface]; su != nil {
		for _, hit := range su.HitTargets {
			if hit.Line == y {
				payload.HitID, payload.HitKind, payload.HitAction, payload.HitPayload = hit.ID, hit.Kind, hit.Action, hit.Payload
				break
			}
		}
	}
	return payload
}

// Modal content uses the same frame-aware inner rect as rendering. Borders
// consume one cell per edge; labels/close glyphs never add a content title row.
func (m *WorkbenchModel) handlePublishedModalMouse(x, y int, left, wheelDown, wheelUp bool) bool {
	rect := m.modalRect
	if x < rect.X || x >= rect.X+rect.Width || y < rect.Y || y >= rect.Y+rect.Height {
		return false
	}
	x, y = x-rect.InnerX, y-rect.InnerY
	innerW, innerH := rect.InnerW, rect.InnerH
	if x < 0 || y < 0 || x >= innerW || y >= innerH {
		return true
	}
	if left && m.clickContribution(huskipc.SlotModalSurface, x, y, innerW, innerH) {
		return true
	}
	button := "left"
	if wheelDown {
		button = "wheel_down"
	} else if wheelUp {
		button = "wheel_up"
	}
	if cp := m.plugins[m.slotOwners[huskipc.SlotModalSurface]]; cp != nil {
		_ = cp.Writer.Send(huskipc.Envelope{Type: "slot_mouse", SlotMouse: m.publisherMousePayload(huskipc.SlotModalSurface, x, y, button)})
	}
	return true
}
