package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func TestReviewFinalDotNamespaceAndLegacyIDs(t *testing.T) {
	for _, tc := range []struct {
		plugin, id string
		valid      bool
	}{
		{"x", "x.button", true}, {"x.y", "x.y.button", true},
		{"bash.tv-search", "bash.tv-search.action", true}, {"husk", "husk.sidebar-list", true},
		{"x", "x.y.button", false}, {"x.y", "x.button", false},
		{"x", "x.request.phase", false}, {"x.y", "x.y.", false},
	} {
		t.Run(tc.plugin+"/"+tc.id, func(t *testing.T) {
			if err := ownID(tc.plugin, tc.id); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	r := newPublicationRegistry()
	if err := r.claimSurface("bash.tv-search", huskipc.SlotSidebarList); err != nil {
		t.Fatal(err)
	}
	if r.Occupants[huskipc.SlotSidebarList] != "bash.tv-search" || r.Slots["husk.sidebar-list"].Owner != "husk" {
		t.Fatal("legacy surface ID or internal alias regressed")
	}
	mustDeclare(t, r, "p", "p.actions")
	if _, err := r.contribute("x", huskipc.SlotContributePayload{SlotID: "p.actions", ID: "x.button", OnClick: "x.y.command"}); err == nil {
		t.Fatal("parent accepted child command namespace")
	}
	if err := declareNamed(r.Hooks, "x", "x.y.hook"); err == nil {
		t.Fatal("parent accepted child hook namespace")
	}
	if err := declareNamed(r.Events, "x", "x.y.event"); err == nil {
		t.Fatal("parent accepted child event namespace")
	}
	if err := r.declareSlot("x", huskipc.SlotDeclarePayload{ID: "x.y.slot", Kind: "inline"}); err == nil {
		t.Fatal("parent accepted child slot namespace")
	}
	_ = declareNamed(r.Hooks, "p", "p.hook")
	if _, err := r.callHook("x", huskipc.HookCallPayload{ID: "p.hook", RequestID: "x.y.request"}); err == nil {
		t.Fatal("parent accepted child request namespace")
	}
}

func TestReviewParentChildContributionOwnership(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	_, _ = addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotMainStage}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "p.actions", Kind: "inline"}}})
	_, child := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "x.y"})
	_, parent := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "x"})
	m.Update(ipcEventMsg{pluginID: "x.y", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "p.actions", ID: "x.y.button", Content: "[Child]", OnClick: "x.y.click"}}})
	parent.Reset()
	m.Update(ipcEventMsg{pluginID: "x", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "p.actions", ID: "x.y.button", Content: "[Hijack]", OnClick: "x.click"}}})
	msgs := envelopes(t, parent)
	if len(msgs) != 1 || msgs[0].ProtocolError == nil {
		t.Fatal("parent hijack must return an explicit error")
	}
	c := m.registry.Slots["p.actions"].Contributions
	if len(c) != 1 || c[0].PluginID != "x.y" || c[0].Content != "[Child]" || c[0].OnClick != "x.y.click" {
		t.Fatalf("child contribution replaced: %+v", c)
	}
	m.Update(ipcEventMsg{pluginID: "p", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: huskipc.SlotMainStage, Lines: []string{"", anchor("p.actions", "row")}}}})
	child.Reset()
	parent.Reset()
	r := m.slotRects[huskipc.SlotMainStage]
	m.Update(tea.MouseClickMsg{X: r.InnerX + 1, Y: r.InnerY + 1, Button: tea.MouseLeft})
	found := false
	for _, msg := range envelopes(t, child) {
		if msg.CommandInvoke != nil && msg.CommandInvoke.CommandID == "x.y.click" {
			found = true
		}
	}
	if !found {
		t.Fatal("click did not remain routed to authenticated child contributor")
	}
	for _, msg := range envelopes(t, parent) {
		if msg.CommandInvoke != nil {
			t.Fatal("hijacker received child command")
		}
	}
}

func TestReviewParentCleanupKeepsChildSubscriptions(t *testing.T) {
	r := newPublicationRegistry()
	mustDeclare(t, r, "p", "p.actions")
	mustDeclare(t, r, "x.y", "x.y.actions")
	mustContribute(t, r, "x", "p.actions", "x.button", "parent")
	mustContribute(t, r, "x.y", "p.actions", "x.y.button", "child")
	for _, publications := range []map[string]*namedPublication{r.Hooks, r.Events} {
		_ = declareNamed(publications, "p", "p.notice")
		_ = subscribe(publications, "x", "p.notice")
		_ = subscribe(publications, "x.y", "p.notice")
		_ = subscribe(publications, "x.y", "p.notice")
		if len(publications["p.notice"].Subscribers) != 2 {
			t.Fatal("subscriptions must deduplicate by exact authenticated identity")
		}
	}
	r.removePlugin("x")
	if r.Slots["x.y.actions"] == nil {
		t.Fatal("parent cleanup removed child's declaration")
	}
	cs := r.Slots["p.actions"].Contributions
	if len(cs) != 1 || cs[0].PluginID != "x.y" {
		t.Fatal("parent cleanup removed child's contribution")
	}
	for _, publications := range []map[string]*namedPublication{r.Hooks, r.Events} {
		subs := publications["p.notice"].Subscribers
		if len(subs) != 1 || subs[0] != "x.y" {
			t.Fatal("parent cleanup removed child's subscription")
		}
	}
}

func TestReviewContributionUpsertChecksAuthenticatedOwner(t *testing.T) {
	r := newPublicationRegistry()
	mustDeclare(t, r, "p", "p.actions")
	// Even inconsistent/pre-existing state cannot be reassigned by a matching ID.
	old := huskipc.SlotContribution{PluginID: "x.y", SlotContributePayload: huskipc.SlotContributePayload{SlotID: "p.actions", ID: "x.button", Content: "original", OnClick: "x.y.click"}}
	r.Slots["p.actions"].Contributions = []huskipc.SlotContribution{old}
	if out, err := r.contribute("x", huskipc.SlotContributePayload{SlotID: "p.actions", ID: "x.button", Content: "hijack"}); err == nil || len(out) != 0 {
		t.Fatal("cross-owner upsert was accepted")
	}
	if r.Slots["p.actions"].Contributions[0] != old {
		t.Fatal("rejected upsert mutated ownership/content")
	}
}

func TestReviewDisconnectRecomputesFramelessGeometry(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	a, _ := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotMainStage}, Frames: map[huskipc.SlotID]frame.Spec{huskipc.SlotMainStage: {Border: true}}})
	_, b := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
	before := m.slotRects[huskipc.SlotMainStage]
	if before.InnerW != before.Width-2 {
		t.Fatal("fixture is not framed")
	}
	b.Reset()
	m.Update(pluginDisconnectedMsg{plugin: a})
	after := m.slotRects[huskipc.SlotMainStage]
	if after.InnerW != after.Width || after.InnerH != after.Height || after.InnerX != after.X || after.InnerY != after.Y {
		t.Fatalf("stale frame geometry after exit: %+v", after)
	}
	rows := strings.Split(ansi.Strip(m.renderSurface(huskipc.SlotMainStage, "", "")), "\n")
	if len(rows) != after.Height || ansi.StringWidth(rows[0]) != after.Width {
		t.Fatal("blank surface shrank after framed owner exited")
	}
	found := false
	for _, msg := range envelopes(t, b) {
		if msg.SlotResize != nil {
			size := msg.SlotResize.SlotSizes[huskipc.SlotMainStage]
			found = size.Width == after.Width && size.Height == after.Height
		}
	}
	if !found {
		t.Fatal("surviving plugin did not receive recomputed slot sizes")
	}
}

func TestReviewPassiveContributionPreservesPublisherMouse(t *testing.T) {
	for _, surface := range []huskipc.SlotID{huskipc.SlotMainStage, huskipc.SlotModalSurface} {
		t.Run(string(surface), func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			_, a := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{surface}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "a.status", Kind: "inline"}}})
			_, b := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
			m.Update(ipcEventMsg{pluginID: "b", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "a.status", ID: "b.status", Content: "[Ready]"}}})
			m.Update(ipcEventMsg{pluginID: "a", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: surface, Lines: []string{"", "agent " + anchor("a.status", "one")}, HitTargets: []huskipc.RowHitTarget{{Line: 1, ID: "one", Kind: "row", Action: "select", Payload: map[string]string{"key": "value"}}}}}})
			if surface == huskipc.SlotModalSurface {
				m.activeModalPlugin, m.activeModalRailID = "a", "demo"
				m.recomputeLayout()
			}
			r := m.slotRects[surface]
			if surface == huskipc.SlotModalSurface {
				r = m.modalRect
			}
			a.Reset()
			b.Reset()
			m.Update(tea.MouseClickMsg{X: r.InnerX + 7, Y: r.InnerY + 1, Button: tea.MouseLeft})
			found := false
			for _, msg := range envelopes(t, a) {
				if mouse := msg.SlotMouse; mouse != nil {
					found = mouse.Slot == surface && mouse.X == 7 && mouse.Y == 1 && mouse.Button == "left"
					if mouse.HitID != "one" || mouse.HitKind != "row" || mouse.HitAction != "select" || len(mouse.HitPayload) != 1 || mouse.HitPayload["key"] != "value" {
						t.Fatal("publisher row hit metadata lost")
					}
				}
			}
			if !found {
				t.Fatal("passive contribution swallowed publisher's mouse event")
			}
			for _, msg := range envelopes(t, b) {
				if msg.CommandInvoke != nil {
					t.Fatal("passive contribution invoked a command")
				}
			}
		})
	}
}

func TestReviewModalOrdinaryRowsPreserveMouseMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		button tea.MouseButton
	}{
		{"left", tea.MouseLeft}, {"wheel_up", tea.MouseWheelUp}, {"wheel_down", tea.MouseWheelDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			_, a := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotModalSurface}, Frames: map[huskipc.SlotID]frame.Spec{huskipc.SlotModalSurface: {Border: true}}})
			m.Update(ipcEventMsg{pluginID: "a", env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{Slot: huskipc.SlotModalSurface, Lines: []string{"", "", "ordinary publisher row"}, HitTargets: []huskipc.RowHitTarget{{Line: 2, ID: "one", Kind: "row", Action: "select", Payload: map[string]string{"key": "value"}}}}}})
			m.activeModalPlugin, m.activeModalRailID = "a", "demo"
			m.recomputeLayout()
			r := m.modalRect
			a.Reset()
			event := tea.Mouse{X: r.InnerX + 5, Y: r.InnerY + 2, Button: tc.button}
			if tc.button == tea.MouseLeft {
				m.Update(tea.MouseClickMsg(event))
			} else {
				m.Update(tea.MouseWheelMsg(event))
			}
			msgs := envelopes(t, a)
			if len(msgs) != 1 || msgs[0].SlotMouse == nil {
				t.Fatalf("missing ordinary modal mouse event: %+v", msgs)
			}
			mouse := msgs[0].SlotMouse
			if mouse.Slot != huskipc.SlotModalSurface || mouse.X != 5 || mouse.Y != 2 || mouse.Button != tc.name || mouse.HitID != "one" || mouse.HitKind != "row" || mouse.HitAction != "select" || len(mouse.HitPayload) != 1 || mouse.HitPayload["key"] != "value" {
				t.Fatalf("modal metadata lost: %+v", mouse)
			}
		})
	}
}

func TestReviewRegionRendersBeforeSlotUpdate(t *testing.T) {
	for _, surface := range []huskipc.SlotID{huskipc.SlotMainStage, huskipc.SlotModalSurface} {
		for _, border := range []bool{false, true} {
			name := string(surface) + "/blank"
			if border {
				name = string(surface) + "/bordered"
			}
			t.Run(name, func(t *testing.T) {
				m := newWorkbenchModel("", nil, t.TempDir())
				_, _ = addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{surface}, Frames: map[huskipc.SlotID]frame.Spec{surface: {Border: border}}, PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "a.region", Kind: "region", Rect: &huskipc.PublishedRect{Surface: surface, X: 2, Y: 2, Width: 12, Height: 1}}}})
				_, b := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "b"})
				m.Update(ipcEventMsg{pluginID: "b", env: huskipc.Envelope{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "a.region", ID: "b.button", Content: "[Action]", OnClick: "b.action"}}})
				if surface == huskipc.SlotModalSurface {
					m.activeModalPlugin, m.activeModalRailID = "a", "demo"
					m.recomputeLayout()
				}
				if m.slotUpdates[surface] != nil {
					t.Fatal("fixture must not send slot_update")
				}
				if !strings.Contains(ansi.Strip(m.View().Content), "[Action]") {
					t.Fatal("declared region invisible before first slot_update")
				}
				r := m.slotRects[surface]
				if surface == huskipc.SlotModalSurface {
					r = m.modalRect
				}
				b.Reset()
				m.Update(tea.MouseClickMsg{X: r.InnerX + 3, Y: r.InnerY + 2, Button: tea.MouseLeft})
				count := 0
				for _, msg := range envelopes(t, b) {
					if msg.CommandInvoke != nil && msg.CommandInvoke.CommandID == "b.action" {
						count++
					}
				}
				if count != 1 {
					t.Fatal("declared region not clickable before first slot_update")
				}
			})
		}
	}
}
