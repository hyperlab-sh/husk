package main

import (
	"io"
	"os"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// demoFrameSpecs is the frame declaration used by --demo-frame: a built-in
// demo plugin asking for border + label + close on every renderable surface.
var demoFrameSpecs = map[huskipc.SlotID]huskipc.FrameSpec{
	huskipc.SlotMainStage:      {Border: true, Label: "Demo Frame", Close: true},
	huskipc.SlotModalSurface:   {Border: true, Label: "Demo Frame", Close: true},
	huskipc.SlotFullCanvas:     {Border: true, Label: "Demo Frame", Close: true},
	huskipc.SlotWorkspaceStage: {Border: true, Label: "Demo Frame", Close: true},
}

// setupDemoFrame registers the built-in demo plugin (no external process) and
// activates the requested rail placement, if any.
func (m *WorkbenchModel) setupDemoFrame(railMode string) {
	demoLines := []string{
		"This surface is a blank canvas; all chrome you see is a frame.",
		"",
		"The plugin declared: \"frame\": {\"border\": true, \"label\": \"Demo Frame\"}",
		"Husk draws the border, the label, and the ✕ close button.",
		"Clicking ✕ closes the surface exactly like esc.",
	}
	newDemoUpdate := func(slot huskipc.SlotID) *huskipc.SlotUpdatePayload {
		return &huskipc.SlotUpdatePayload{
			PluginID: "husk.demo-frame",
			Slot:     slot,
			Title:    "Demo Frame",
			Lines:    demoLines,
		}
	}

	m.plugins["husk.demo-frame"] = &ConnectedPlugin{
		Info: huskipc.HelloPayload{
			PluginID:    "husk.demo-frame",
			Name:        "Demo Frame",
			Version:     "0.1.0",
			Language:    "go",
			PID:         os.Getpid(),
			Description: "Built-in demo declaring a border+label+close frame",
			Slots:       []huskipc.SlotID{huskipc.SlotMainStage},
			Frames:      demoFrameSpecs,
		},
		Writer:    huskipc.NewConnWriter(io.Discard),
		Connected: time.Now(),
	}
	m.registry.Occupants[huskipc.SlotMainStage] = "husk.demo-frame"
	m.slotUpdates[huskipc.SlotMainStage] = newDemoUpdate(huskipc.SlotMainStage)

	switch railMode {
	case "modal":
		m.openModal("husk.demo-frame", "rail.demo-frame")
		m.slotUpdates[huskipc.SlotModalSurface] = newDemoUpdate(huskipc.SlotModalSurface)
	case "full-canvas":
		m.activeRailID = "rail.demo-frame"
		m.activePlacement = huskipc.PlacementFullCanvas
		m.focusedSlot = huskipc.SlotFullCanvas
		m.slotUpdates[huskipc.SlotFullCanvas] = newDemoUpdate(huskipc.SlotFullCanvas)
	case "stage":
		m.activeRailID = "rail.demo-frame"
		m.activePlacement = huskipc.PlacementStage
		m.focusedSlot = huskipc.SlotWorkspaceStage
		m.slotUpdates[huskipc.SlotWorkspaceStage] = newDemoUpdate(huskipc.SlotWorkspaceStage)
	}
	m.recomputeLayout()
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
