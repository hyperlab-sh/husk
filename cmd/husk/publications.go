package main

import (
	"fmt"
	"io"

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
	}
}

// These fake plugins use the same Hello, IPC routing and rendering paths as
// external processes, without opening sockets or starting child processes.
func (m *WorkbenchModel) seedPubslotsDemo() {
	for _, hello := range []huskipc.HelloPayload{
		{PluginID: "a", Name: "Row publisher", Slots: []huskipc.SlotID{huskipc.SlotMainStage}, PublishedSlots: []huskipc.SlotDeclarePayload{
			{ID: "a.agent-line-action", Title: "Row actions", Kind: "inline"},
			{ID: "a.summary", Title: "Summary region", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, X: 0, Y: 5, Width: 28, Height: 3}},
		}},
		{PluginID: "b", Name: "Action contributor"},
	} {
		m.Update(pluginRegisteredMsg{&ConnectedPlugin{Info: hello, Writer: huskipc.NewConnWriter(io.Discard)}})
	}
	for _, contribution := range []huskipc.SlotContributePayload{
		{SlotID: "a.agent-line-action", ID: "b.action", Content: "\x1b[32m[Inspect]\x1b[0m", OnClick: "b.inspect"},
		{SlotID: "a.summary", ID: "b.summary", Content: "Published region from B\nClick Inspect on any row", OnClick: "b.inspect"},
	} {
		m.Update(ipcEventMsg{pluginID: "b", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &contribution}})
	}
	m.Update(ipcEventMsg{pluginID: "a", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{
		PluginID: "a", Slot: huskipc.SlotMainStage, Title: "Published slots demo",
		Lines: []string{"Plugin A rows; Plugin B actions", "agent one  \x1b]husk;slot=a.agent-line-action;key=one\x1b\\", "agent two  \x1b]husk;slot=a.agent-line-action;key=two\x1b\\"},
	}}})
}

// clickContribution re-renders from current state rather than trusting a stale
// frame hit map after a resize, registry mutation or plugin disconnect.
func (m *WorkbenchModel) clickContribution(surface huskipc.SlotID, x, y, width, height int) bool {
	if surface == huskipc.SlotMainStage && m.stageMode == "hotspots" {
		return false // architecture view hides the publisher's content
	}
	su := m.slotUpdates[surface]
	if su == nil {
		return false
	}
	_, hits := m.registry.renderPublished(surface, m.slotOwners[surface], su.Lines, width, height)
	for i := len(hits) - 1; i >= 0; i-- {
		h := hits[i]
		if h.Y == y && x >= h.X && x < h.X+h.Width {
			if cp := m.plugins[h.PluginID]; cp != nil && h.CommandID != "" {
				_ = cp.Writer.Send(huskipc.Envelope{Type: "command_invoke", CommandInvoke: &huskipc.CommandInvokePayload{CommandID: h.CommandID, Args: map[string]string{"slot_id": h.SlotID, "contribution_id": h.ContributionID, "row_key": h.RowKey}}})
			}
			return true
		}
	}
	return false
}

// Modal content starts after its title row and must not click through to a
// surface behind the overlay. Geometry matches overlayModalPlugin.
func (m *WorkbenchModel) handlePublishedModalMouse(x, y int, left, wheelDown, wheelUp bool) bool {
	w, h := min(92, m.width-10), min(24, m.height-6)
	startX, startY := max(4, (m.width-w)/2), max(2, (m.height-h)/2)
	if x < startX || x >= startX+w || y < startY || y >= startY+h {
		return false
	}
	x, y = x-startX-2, y-startY-2
	innerW, innerH := w-4, h-3
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
		_ = cp.Writer.Send(huskipc.Envelope{Type: "slot_mouse", SlotMouse: &huskipc.SlotMousePayload{Slot: huskipc.SlotModalSurface, X: x, Y: y, Button: button}})
	}
	return true
}
