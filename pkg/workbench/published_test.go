package workbench

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPublishedClickRoutesToContributor(t *testing.T) {
	m := newWorkbenchModel()
	_, a := addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}, PublishedSlots: []slotDecl{{ID: "a.action", Kind: "inline"}}})
	_, bOut := addPublicationTestPlugin(m, hello{PluginID: "b"})
	contribute(m, "a.action", contribution{PluginID: "b", ID: "b.inspect", Content: "[Inspect]", OnClick: "b.inspect"})
	m.content("a", protocol.SurfaceUpdateParams{PluginID: "spoof", Surface: protocol.SurfaceMainStage, Lines: []string{"agent " + anchor("a.action", "agent-1")}})
	if m.slotUpdates[protocol.SurfaceMainStage].PluginID != "a" {
		t.Fatal("slot_update identity not normalized")
	}
	a.Reset()
	bOut.Reset()
	rect := m.slotRects[protocol.SurfaceMainStage]
	m.Update(tea.MouseClickMsg{X: rect.InnerX + 7, Y: rect.InnerY, Button: tea.MouseLeft})
	msgs := envelopes(t, bOut)
	var commands []wireMsg
	for _, msg := range msgs {
		if msg.Type == "invoke" {
			commands = append(commands, msg)
		}
	}
	if len(commands) != 1 || commands[0].CommandInvoke.CommandID != "b.inspect" || commands[0].CommandInvoke.Args["row_key"] != "agent-1" {
		t.Fatalf("click not routed to contributor: %+v", msgs)
	}
	if m.activeDrag != "" || m.focusedSlot != protocol.SurfaceMainStage {
		t.Fatal("first-row contribution click must focus its surface, not start a drag")
	}
	for _, msg := range envelopes(t, a) {
		if msg.Type == "slot_mouse" {
			t.Fatal("contribution click leaked to publisher")
		}
	}
	m.stageMode = "hotspots"
	bOut.Reset()
	if m.clickContribution(protocol.SurfaceMainStage, 7, 0, rect.InnerW, rect.InnerH) || bOut.Len() != 0 {
		t.Fatal("hidden stage contribution remained clickable")
	}
	m.stageMode = "plugin"
	// Disconnect removes the hit immediately, without waiting for A to repaint.
	delete(m.plugins, "b") // what a refresh does when B leaves
	bOut.Reset()
	if m.clickContribution(protocol.SurfaceMainStage, 7, 0, rect.InnerW, rect.InnerH) || bOut.Len() != 0 {
		t.Fatal("disconnected contribution still clickable")
	}
}

func TestPublishedBlankFirstRowDoesNotStartDividerDrag(t *testing.T) {
	m := newWorkbenchModel()
	_, _ = addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}, PublishedSlots: []slotDecl{{ID: "a.action", Kind: "inline"}}})
	_, b := addPublicationTestPlugin(m, hello{PluginID: "b"})
	contribute(m, "a.action", contribution{PluginID: "b", ID: "b.button", Content: "[Inspect]", OnClick: "b.inspect"})
	m.content("a", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{"agent " + anchor("a.action", "one")}})
	r := m.slotRects[protocol.SurfaceMainStage]
	b.Reset()
	m.Update(tea.MouseClickMsg{X: r.InnerX + 20, Y: r.InnerY, Button: tea.MouseLeft})
	if m.activeDrag != "" {
		t.Fatalf("blank first-row cell of an unbordered surface started a drag: %q", m.activeDrag)
	}
	for _, msg := range envelopes(t, b) {
		if msg.CommandInvoke != nil {
			t.Fatal("blank cell invoked a contribution command")
		}
	}
}

func TestPublishedRegionAndModalClicks(t *testing.T) {
	for _, surface := range []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceModal} {
		for _, tc := range []struct {
			name string
			spec frame.Spec
		}{
			{"blank", frame.Spec{}},
			{"bordered", frame.Spec{Border: true, Label: "Publisher", Close: true}},
			{"borderless-chrome", frame.Spec{Label: "Publisher", Close: true}},
		} {
			t.Run(string(surface)+"/"+tc.name, func(t *testing.T) {
				m := newWorkbenchModel()
				m.width, m.height = 84, 24
				_, a := addPublicationTestPlugin(m, hello{
					PluginID: "a", Slots: []protocol.SurfaceID{surface}, Frames: map[protocol.SurfaceID]protocol.FrameSpec{surface: protocol.FrameSpec(tc.spec)},
					PublishedSlots: []slotDecl{
						{ID: "a.region", Kind: "region", Rect: &contracts.PublishedRect{Surface: surface, X: 2, Y: 1, Width: 6, Height: 1}},
						{ID: "a.inline", Kind: "inline"},
					},
				})
				_, b := addPublicationTestPlugin(m, hello{PluginID: "b"})
				contribute(m, "a.region", contribution{PluginID: "b", ID: "b.button", Content: "[Click]", OnClick: "b.click"})
				contribute(m, "a.inline", contribution{PluginID: "b", ID: "b.inline", Content: "[Run]", OnClick: "b.run"})
				m.content("a", protocol.SurfaceUpdateParams{Surface: surface, Lines: []string{"publisher", "", "", "agent " + anchor("a.inline", "agent-1")}})
				if surface == protocol.SurfaceModal {
					m.openModal("a", "demo")
					m.recomputeLayout()
				}
				rect := m.slotRects[surface]
				if surface == protocol.SurfaceModal {
					rect = m.modalRect
				}
				view := ansi.Strip(m.View().Content)
				if !strings.Contains(view, "[Click") || !strings.Contains(view, "[Run]") {
					t.Fatal("contributions not rendered inside frame")
				}
				a.Reset()
				b.Reset()
				for _, cell := range [][2]int{{3, 1}, {6, 3}} {
					m.Update(tea.MouseClickMsg{X: rect.InnerX + cell[0], Y: rect.InnerY + cell[1], Button: tea.MouseLeft})
				}
				var commands []*cmdInvoke
				for _, msg := range envelopes(t, b) {
					if msg.CommandInvoke != nil {
						commands = append(commands, msg.CommandInvoke)
					}
				}
				if len(commands) != 2 || commands[0].CommandID != "b.click" || commands[1].CommandID != "b.run" || commands[1].Args["row_key"] != "agent-1" {
					t.Fatalf("frame-aware contribution clicks missed: %+v", commands)
				}
				for _, msg := range envelopes(t, a) {
					if msg.SlotMouse != nil {
						t.Fatal("contribution click leaked to publisher")
					}
				}
				b.Reset()
				if tc.spec.Border {
					m.Update(tea.MouseClickMsg{X: rect.X, Y: rect.Y + 2, Button: tea.MouseLeft})
					for _, msg := range envelopes(t, b) {
						if msg.CommandInvoke != nil {
							t.Fatal("frame border triggered a contribution")
						}
					}
				}
				if surface == protocol.SurfaceModal && tc.spec.Close {
					x := rect.X + rect.Width - 1
					if tc.spec.Border {
						x--
					}
					m.Update(tea.MouseClickMsg{X: x, Y: rect.Y, Button: tea.MouseLeft})
					if m.modalOwner().Open() {
						t.Fatal("contribution routing swallowed frame close")
					}
				}
			})
		}
	}
}

func TestPublishedBorderlessFrameOccludesContribution(t *testing.T) {
	m := newWorkbenchModel()
	_, _ = addPublicationTestPlugin(m, hello{PluginID: "a", Slots: []protocol.SurfaceID{protocol.SurfaceModal}, Frames: map[protocol.SurfaceID]protocol.FrameSpec{protocol.SurfaceModal: {Label: "Header", Close: true}}, PublishedSlots: []slotDecl{{ID: "a.inline", Kind: "inline"}}})
	_, b := addPublicationTestPlugin(m, hello{PluginID: "b"})
	contribute(m, "a.inline", contribution{PluginID: "b", ID: "b.button", Content: strings.Repeat("X", 100), OnClick: "b.click"})
	m.content("a", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceModal, Title: "Panel", Lines: []string{anchor("a.inline", "one")}})
	m.openModal("a", "demo")
	m.recomputeLayout()
	b.Reset()
	r := m.modalRect
	if m.clickContribution(protocol.SurfaceModal, 4, 0, r.InnerW, r.InnerH) {
		t.Fatal("label-covered contribution remained clickable")
	}
	if !m.clickContribution(protocol.SurfaceModal, 5, 0, r.InnerW, r.InnerH) {
		t.Fatal("visible contribution after title override not clickable")
	}
	if m.clickContribution(protocol.SurfaceModal, r.InnerW-1, 0, r.InnerW, r.InnerH) {
		t.Fatal("close-covered contribution remained clickable")
	}
	msgs := envelopes(t, b)
	if len(msgs) != 1 || msgs[0].CommandInvoke == nil {
		t.Fatalf("unexpected frame-overlay click delivery: %+v", msgs)
	}
}
