package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func addTestPlugin(m *WorkbenchModel, hello huskipc.HelloPayload) (*ConnectedPlugin, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	cp := &ConnectedPlugin{Info: hello, Writer: huskipc.NewConnWriter(buf)}
	m.Update(pluginRegisteredMsg{plugin: cp})
	return cp, buf
}

func envelopes(t *testing.T, buf *bytes.Buffer) []huskipc.Envelope {
	t.Helper()
	var out []huskipc.Envelope
	if err := huskipc.ReadLoop(bytes.NewReader(buf.Bytes()), func(e huskipc.Envelope) { out = append(out, e) }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPublishedHelloAtomicityAndLegacySlots(t *testing.T) {
	m := newWorkbenchModel("", nil)
	a, buf := addTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "a.action", Kind: "inline"}}, Hooks: []huskipc.HookDeclarePayload{{ID: "a.hook"}}, Events: []huskipc.EventDeclarePayload{{ID: "a.event"}}})
	if m.plugins["a"] != a || m.slotOwners[huskipc.SlotSidebarList] != "a" || m.registry.Slots["a.action"] == nil || m.registry.Hooks["a.hook"] == nil || m.registry.Events["a.event"] == nil {
		t.Fatal("hello did not register all publications")
	}
	msgs := envelopes(t, buf)
	if len(msgs) != 2 || msgs[0].Type != "welcome" || len(msgs[1].SlotContributions.Contributions) != 0 {
		t.Fatalf("bad welcome/initial contributions: %+v", msgs)
	}
	_, bad := addTestPlugin(m, huskipc.HelloPayload{PluginID: "b", Slots: []huskipc.SlotID{huskipc.SlotMainStage}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "b.ok", Kind: "inline"}, {ID: "a.foreign", Kind: "inline"}}})
	if m.plugins["b"] != nil || m.slotOwners[huskipc.SlotMainStage] != "" || m.registry.Slots["b.ok"] != nil {
		t.Fatal("failed hello was not rolled back")
	}
	if messages := envelopes(t, bad); len(messages) != 1 || messages[0].Type != "protocol_error" {
		t.Fatal("missing registration error")
	}
	// Duplicate connections cannot overwrite the live plugin or remove its state.
	duplicate, _ := addTestPlugin(m, huskipc.HelloPayload{PluginID: "a"})
	m.Update(pluginDisconnectedMsg{plugin: duplicate})
	m.Update(ipcEventMsg{pluginID: "a", connection: duplicate, env: huskipc.Envelope{Type: "event_declare", EventDeclare: &huskipc.EventDeclarePayload{ID: "a.spoof"}}})
	if m.plugins["a"] != a || m.registry.Events["a.spoof"] != nil {
		t.Fatal("stale connection altered live plugin")
	}
	_, reserved := addTestPlugin(m, huskipc.HelloPayload{PluginID: "husk"})
	if messages := envelopes(t, reserved); len(messages) != 1 || messages[0].Type != "protocol_error" {
		t.Fatal("host identity accepted")
	}
	// Existing bundled plugins use dotted IDs; do not break their legacy Hello.
	legacy, legacyOut := addTestPlugin(m, huskipc.HelloPayload{PluginID: "bash.tv-search", Slots: []huskipc.SlotID{huskipc.SlotModalSurface}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "bash.tv-search.action", Kind: "inline"}}})
	if m.plugins["bash.tv-search"] != legacy || envelopes(t, legacyOut)[0].Type != "welcome" {
		t.Fatal("legacy dotted plugin ID rejected")
	}
}

func TestPublishedClickRoutesToContributor(t *testing.T) {
	m := newWorkbenchModel("", nil)
	_, a := addTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotMainStage}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "a.action", Kind: "inline"}}})
	b, bOut := addTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
	m.Update(ipcEventMsg{pluginID: "b", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "a.action", ID: "b.inspect", Content: "[Inspect]", OnClick: "b.inspect"}}})
	m.Update(ipcEventMsg{pluginID: "a", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{PluginID: "spoof", Slot: huskipc.SlotMainStage, Lines: []string{"agent " + anchor("a.action", "agent-1")}}}})
	if m.slotUpdates[huskipc.SlotMainStage].PluginID != "a" {
		t.Fatal("slot_update identity not normalized")
	}
	a.Reset()
	bOut.Reset()
	rect := m.slotRects[huskipc.SlotMainStage]
	m.Update(tea.MouseMsg{X: rect.InnerX + 7, Y: rect.InnerY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	msgs := envelopes(t, bOut)
	var commands []huskipc.Envelope
	for _, msg := range msgs {
		if msg.Type == "command_invoke" {
			commands = append(commands, msg)
		}
	}
	if len(commands) != 1 || commands[0].CommandInvoke.CommandID != "b.inspect" || commands[0].CommandInvoke.Args["row_key"] != "agent-1" {
		t.Fatalf("click not routed to contributor: %+v", msgs)
	}
	for _, msg := range envelopes(t, a) {
		if msg.Type == "slot_mouse" {
			t.Fatal("contribution click leaked to publisher")
		}
	}
	m.stageMode = "hotspots"
	bOut.Reset()
	if m.clickContribution(huskipc.SlotMainStage, 7, 0, rect.InnerW, rect.InnerH) || bOut.Len() != 0 {
		t.Fatal("hidden stage contribution remained clickable")
	}
	m.stageMode = "plugin"
	// Disconnect removes the hit immediately, without waiting for A to repaint.
	m.Update(pluginDisconnectedMsg{plugin: b})
	bOut.Reset()
	if m.clickContribution(huskipc.SlotMainStage, 7, 0, rect.InnerW, rect.InnerH) || bOut.Len() != 0 {
		t.Fatal("disconnected contribution still clickable")
	}
}

func TestPublishedRegionAndModalClicks(t *testing.T) {
	for _, surface := range []huskipc.SlotID{huskipc.SlotMainStage, huskipc.SlotModalSurface} {
		t.Run(string(surface), func(t *testing.T) {
			m := newWorkbenchModel("", nil)
			_, _ = addTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{surface}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "a.region", Kind: "region", Rect: &huskipc.PublishedRect{Surface: surface, X: 2, Y: 1, Width: 6, Height: 2}}}})
			_, b := addTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
			m.Update(ipcEventMsg{pluginID: "b", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "a.region", ID: "b.button", Content: "[Click]", OnClick: "b.click"}}})
			m.Update(ipcEventMsg{pluginID: "a", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: surface, Lines: []string{"publisher"}}}})
			b.Reset()
			var x, y int
			if surface == huskipc.SlotModalSurface {
				m.activeModalRailID = "demo"
				m.activeModalPlugin = "a"
				w, h := min(92, m.width-10), min(24, m.height-6)
				x = max(4, (m.width-w)/2) + 2 + 3
				y = max(2, (m.height-h)/2) + 2 + 1
			} else {
				rect := m.slotRects[surface]
				x = rect.InnerX + 3
				y = rect.InnerY + 1
			}
			m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			found := false
			for _, msg := range envelopes(t, b) {
				if msg.Type == "command_invoke" && msg.CommandInvoke.CommandID == "b.click" {
					found = true
				}
			}
			if !found {
				t.Fatal("region click missed contributor")
			}
		})
	}
}

func TestPublishedRoutingAndMalformedPayloads(t *testing.T) {
	m := newWorkbenchModel("", nil)
	_, a := addTestPlugin(m, huskipc.HelloPayload{PluginID: "a"})
	_, b := addTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
	send := func(plugin string, e huskipc.Envelope) { m.Update(ipcEventMsg{pluginID: plugin, env: e}) }
	send("a", huskipc.Envelope{Type: "hook_declare", HookDeclare: &huskipc.HookDeclarePayload{ID: "a.hook"}})
	send("b", huskipc.Envelope{Type: "hook_subscribe", HookSubscribe: &huskipc.HookSubscribePayload{ID: "a.hook"}})
	a.Reset()
	b.Reset()
	send("a", huskipc.Envelope{Type: "hook_call", HookCall: &huskipc.HookCallPayload{ID: "a.hook", RequestID: "a.request"}})
	if messages := envelopes(t, b); len(messages) != 1 || messages[0].Type != "hook_call" {
		t.Fatal("hook call not routed")
	}
	send("b", huskipc.Envelope{Type: "hook_result", HookResult: &huskipc.HookResultPayload{ID: "a.hook", RequestID: "a.request"}})
	if messages := envelopes(t, a); len(messages) != 1 || messages[0].HookResult.PluginID != "b" {
		t.Fatal("result not routed")
	}
	send("a", huskipc.Envelope{Type: "event_declare", EventDeclare: &huskipc.EventDeclarePayload{ID: "a.event"}})
	send("b", huskipc.Envelope{Type: "event_subscribe", EventSubscribe: &huskipc.EventSubscribePayload{ID: "a.event"}})
	b.Reset()
	send("a", huskipc.Envelope{Type: "event_emit", EventEmit: &huskipc.EventPayload{Topic: "a.event"}})
	if messages := envelopes(t, b); len(messages) != 1 || messages[0].EventEmit.Topic != "a.event" {
		t.Fatal("event not routed")
	}
	for _, typ := range []string{"slot_declare", "slot_contribute", "hook_declare", "hook_subscribe", "hook_call", "hook_result", "event_declare", "event_subscribe", "event_emit"} {
		b.Reset()
		send("b", huskipc.Envelope{Type: typ})
		if messages := envelopes(t, b); len(messages) != 1 || messages[0].Type != "protocol_error" {
			t.Fatalf("malformed %s not rejected", typ)
		}
	}
}

func TestPublishedStreamEOFCleanup(t *testing.T) {
	m := newWorkbenchModel("", nil)
	var wire bytes.Buffer
	writer := huskipc.NewConnWriter(&wire)
	_ = writer.Send(huskipc.Envelope{Type: "slot_declare", SlotDeclare: &huskipc.SlotDeclarePayload{ID: "anonymous.before", Kind: "inline"}})
	_ = writer.Send(huskipc.Envelope{Type: "hello", Hello: &huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotMainStage}}})
	_ = writer.Send(huskipc.Envelope{Type: "slot_declare", SlotDeclare: &huskipc.SlotDeclarePayload{ID: "a.runtime", Kind: "inline"}})
	_ = writer.Send(huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: huskipc.SlotMainStage, Lines: []string{"hello"}}})
	m.handleStream(bytes.NewReader(wire.Bytes()), io.Discard, io.NopCloser(strings.NewReader("")))
	// Transport emits register, declaration, frame and disconnect in order.
	if len(m.subChan) != 4 {
		t.Fatalf("unexpected queued messages: %d", len(m.subChan))
	}
	for i := 0; i < 3; i++ {
		m.Update(<-m.subChan)
	}
	if m.registry.Slots["a.runtime"] == nil || m.slotUpdates[huskipc.SlotMainStage] == nil {
		t.Fatal("live stream not applied")
	}
	m.Update(<-m.subChan)
	if len(m.plugins) != 0 || len(m.slotUpdates) != 0 || len(m.slotOwners) != 0 || m.registry.Slots["a.runtime"] != nil {
		t.Fatal("EOF did not clean up stream")
	}
}

func TestPublishedUnauthorizedSurfaceUpdate(t *testing.T) {
	m := newWorkbenchModel("", nil)
	_, _ = addTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotMainStage}})
	_, b := addTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
	b.Reset()
	m.Update(ipcEventMsg{pluginID: "b", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{PluginID: "a", Slot: huskipc.SlotMainStage, Lines: []string{"spoof"}}}})
	if m.slotUpdates[huskipc.SlotMainStage] != nil {
		t.Fatal("foreign surface update accepted")
	}
	if messages := envelopes(t, b); len(messages) != 1 || messages[0].Type != "protocol_error" {
		t.Fatal("missing unauthorized error")
	}
}

func TestPublishedWireThroughReadLoop(t *testing.T) {
	// Real JSON-lines decoding, rather than struct-only fixtures, covers embedded
	// contribution fields and the existing event envelope key.
	wire := `{"type":"slot_contribute","slot_contribute":{"slot_id":"a.action","id":"b.button","content":"[Run]","on_click":"b.run"}}` + "\n"
	if err := huskipc.ReadLoop(strings.NewReader(wire), func(env huskipc.Envelope) {
		if env.SlotContribute == nil || env.SlotContribute.ID != "b.button" {
			t.Fatal("wire contribution failed to decode")
		}
		b, err := json.Marshal(huskipc.SlotContribution{SlotContributePayload: *env.SlotContribute, PluginID: "b"})
		if err != nil || !bytes.Contains(b, []byte(`"slot_id":"a.action"`)) || !bytes.Contains(b, []byte(`"plugin_id":"b"`)) {
			t.Fatalf("bad contribution wire shape: %s %v", b, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
}
