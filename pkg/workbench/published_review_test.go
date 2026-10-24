package workbench

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestReviewParentChildContributionOwnership(t *testing.T) {
	m := newWorkbenchModel()
	_, _ = addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}, PublishedSlots: []slotDecl{{ID: "p.actions", Kind: "inline"}}})
	_, child := addPublicationTestPlugin(m, hello{PluginID: "x.y"})
	_, parent := addPublicationTestPlugin(m, hello{PluginID: "x"})
	contribute(m, "p.actions", contribution{PluginID: "x.y", ID: "x.y.button", Content: "[Child]", OnClick: "x.y.click"})
	parent.Reset()
	c := m.catalog.Slots["p.actions"].Contributions
	if len(c) != 1 || c[0].PluginID != "x.y" || c[0].Content != "[Child]" || c[0].OnClick != "x.y.click" {
		t.Fatalf("child contribution replaced: %+v", c)
	}
	m.content("p", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{"", anchor("p.actions", "row")}})
	child.Reset()
	parent.Reset()
	r := m.slotRects[protocol.SurfaceMainStage]
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

func TestReviewDepartureRecomputesFramelessGeometry(t *testing.T) {
	m := newWorkbenchModel()
	addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}, Frames: map[protocol.SurfaceID]protocol.FrameSpec{protocol.SurfaceMainStage: {Border: true}}})
	addPublicationTestPlugin(m, hello{PluginID: "b"})
	before := m.slotRects[protocol.SurfaceMainStage]
	if before.InnerW != before.Width-2 {
		t.Fatal("fixture is not framed")
	}
	// What a refresh does when A exits: its plugin, occupancy and frames go.
	delete(m.plugins, "a")
	m.catalog = newSurfaceCatalog()
	m.recomputeLayout()
	after := m.slotRects[protocol.SurfaceMainStage]
	if after.InnerW != after.Width || after.InnerH != after.Height || after.InnerX != after.X || after.InnerY != after.Y {
		t.Fatalf("stale frame geometry after exit: %+v", after)
	}
	rows := drawnRect(t, after, func(c *canvas) { m.drawSurface(c, protocol.SurfaceMainStage, "", "") })
	if len(rows) != after.Height || ansi.StringWidth(rows[0]) != after.Width {
		t.Fatal("blank surface shrank after framed owner exited")
	}
}

func TestReviewPassiveContributionPreservesPublisherMouse(t *testing.T) {
	for _, surface := range []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceModal} {
		t.Run(string(surface), func(t *testing.T) {
			m := newWorkbenchModel()
			_, a := addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{surface}, PublishedSlots: []slotDecl{{ID: "a.status", Kind: "inline"}}})
			_, b := addPublicationTestPlugin(m, hello{PluginID: "b"})
			contribute(m, "a.status", contribution{PluginID: "b", ID: "b.status", Content: "[Ready]"})
			m.content("a", protocol.SurfaceUpdateParams{Surface: surface, Lines: []string{"", "agent " + anchor("a.status", "one")}, HitTargets: []protocol.RowHitTarget{{Line: 1, ID: "one", Kind: "row", Action: "select", Payload: map[string]string{"key": "value"}}}})
			if surface == protocol.SurfaceModal {
				m.openModal("a", "demo")
				m.recomputeLayout()
			}
			r := m.slotRects[surface]
			if surface == protocol.SurfaceModal {
				r = m.modalRect
			}
			a.Reset()
			b.Reset()
			m.Update(tea.MouseClickMsg{X: r.InnerX + 7, Y: r.InnerY + 1, Button: tea.MouseLeft})
			found := false
			for _, msg := range envelopes(t, a) {
				if mouse := msg.SlotMouse; mouse != nil {
					found = mouse.Surface == surface && mouse.X == 7 && mouse.Y == 1 && mouse.Button == "left"
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
			m := newWorkbenchModel()
			_, a := addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceModal}, Frames: map[protocol.SurfaceID]protocol.FrameSpec{protocol.SurfaceModal: {Border: true}}})
			m.content("a", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceModal, Lines: []string{"", "", "ordinary publisher row"}, HitTargets: []protocol.RowHitTarget{{Line: 2, ID: "one", Kind: "row", Action: "select", Payload: map[string]string{"key": "value"}}}})
			m.openModal("a", "demo")
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
			if mouse.Surface != protocol.SurfaceModal || mouse.X != 5 || mouse.Y != 2 || mouse.Button != tc.name || mouse.HitID != "one" || mouse.HitKind != "row" || mouse.HitAction != "select" || len(mouse.HitPayload) != 1 || mouse.HitPayload["key"] != "value" {
				t.Fatalf("modal metadata lost: %+v", mouse)
			}
		})
	}
}

func TestReviewRegionRendersBeforeSlotUpdate(t *testing.T) {
	for _, surface := range []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceModal} {
		for _, border := range []bool{false, true} {
			name := string(surface) + "/blank"
			if border {
				name = string(surface) + "/bordered"
			}
			t.Run(name, func(t *testing.T) {
				m := newWorkbenchModel()
				_, _ = addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{surface}, Frames: map[protocol.SurfaceID]protocol.FrameSpec{surface: {Border: border}}, PublishedSlots: []slotDecl{{ID: "a.region", Kind: "region", Rect: &contracts.PublishedRect{Surface: surface, X: 2, Y: 2, Width: 12, Height: 1}}}})
				_, b := addPublicationTestPlugin(m, hello{PluginID: "b"})
				contribute(m, "a.region", contribution{PluginID: "b", ID: "b.button", Content: "[Action]", OnClick: "b.action"})
				if surface == protocol.SurfaceModal {
					m.openModal("a", "demo")
					m.recomputeLayout()
				}
				if m.slotUpdates[surface] != nil {
					t.Fatal("fixture must not send slot_update")
				}
				if !strings.Contains(ansi.Strip(m.View().Content), "[Action]") {
					t.Fatal("declared region invisible before first slot_update")
				}
				r := m.slotRects[surface]
				if surface == protocol.SurfaceModal {
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
