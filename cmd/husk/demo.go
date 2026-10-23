package main

import (
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// demoFrameSpecs is the frame declaration used by --demo-frame: a built-in
// demo plugin asking for border + label + close on every renderable surface.
var demoFrameSpecs = map[protocol.SurfaceID]protocol.FrameSpec{
	protocol.SurfaceMainStage:      {Border: true, Label: "Demo Frame", Close: true},
	protocol.SurfaceModal:          {Border: true, Label: "Demo Frame", Close: true},
	protocol.SurfaceFullCanvas:     {Border: true, Label: "Demo Frame", Close: true},
	protocol.SurfaceWorkspaceStage: {Border: true, Label: "Demo Frame", Close: true},
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
	newDemoUpdate := func(slot protocol.SurfaceID) *protocol.SurfaceUpdateParams {
		return &protocol.SurfaceUpdateParams{
			PluginID: "husk.demo-frame",
			Surface:  slot,
			Title:    "Demo Frame",
			Lines:    demoLines,
		}
	}

	m.plugins["husk.demo-frame"] = true
	m.catalog.Frames["husk.demo-frame"] = demoFrameSpecs
	m.catalog.Occupants[protocol.SurfaceMainStage] = "husk.demo-frame"
	m.slotUpdates[protocol.SurfaceMainStage] = newDemoUpdate(protocol.SurfaceMainStage)

	switch railMode {
	case "modal":
		m.openModal("husk.demo-frame", "rail.demo-frame")
		m.slotUpdates[protocol.SurfaceModal] = newDemoUpdate(protocol.SurfaceModal)
	case "full-canvas":
		m.activeRailID = "rail.demo-frame"
		m.activePlacement = contracts.PlacementFullCanvas
		m.focusedSlot = protocol.SurfaceFullCanvas
		m.slotUpdates[protocol.SurfaceFullCanvas] = newDemoUpdate(protocol.SurfaceFullCanvas)
	case "stage":
		m.activeRailID = "rail.demo-frame"
		m.activePlacement = contracts.PlacementStage
		m.focusedSlot = protocol.SurfaceWorkspaceStage
		m.slotUpdates[protocol.SurfaceWorkspaceStage] = newDemoUpdate(protocol.SurfaceWorkspaceStage)
	}
	m.recomputeLayout()
}

// The pubslots demo fills the catalog directly, the way a refresh from a host
// would, without sockets or child processes.
func (m *WorkbenchModel) seedPubslotsDemo() {
	m.plugins["a"], m.plugins["b"] = true, true
	m.catalog.Occupants[protocol.SurfaceMainStage] = "a"
	m.catalog.Slots["a.agent-line-action"] = &publishedSlot{
		ID: "a.agent-line-action", Owner: "a", Kind: "inline",
		Contributions: []contribution{{PluginID: "b", ID: "b.action", Content: "\x1b[32m[Inspect]\x1b[0m", OnClick: "b.inspect"}},
	}
	m.catalog.Slots["a.summary"] = &publishedSlot{
		ID: "a.summary", Owner: "a", Kind: "region",
		Rect:          &contracts.PublishedRect{Surface: protocol.SurfaceMainStage, X: 0, Y: 5, Width: 28, Height: 3},
		Contributions: []contribution{{PluginID: "b", ID: "b.summary", Content: "Published region from B\nClick Inspect on any row", OnClick: "b.inspect"}},
	}
	m.slotUpdates[protocol.SurfaceMainStage] = &protocol.SurfaceUpdateParams{
		PluginID: "a", Surface: protocol.SurfaceMainStage, Title: "Published slots demo",
		Lines: []string{"Plugin A rows; Plugin B actions", "agent one  \x1b]husk;slot=a.agent-line-action;key=one\x1b\\", "agent two  \x1b]husk;slot=a.agent-line-action;key=two\x1b\\"},
	}
	m.recomputeLayout()
}
