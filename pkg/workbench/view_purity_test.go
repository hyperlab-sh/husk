package workbench

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// pureViewModel builds a model exercising every View branch that reads
// plugin state: an occupied main stage, hotspots, modal, toast and palette.
func pureViewModel(stageMode string, overlays bool) *WorkbenchModel {
	m := newWorkbenchModel()
	m.logOut = nil
	m.width, m.height = 144, 40
	m.plugins["p.stage"] = true
	m.catalog.Occupants[protocol.SurfaceMainStage] = "p.stage"
	m.slotUpdates[protocol.SurfaceMainStage] = &protocol.SurfaceUpdateParams{PluginID: "p.stage", Surface: protocol.SurfaceMainStage, Title: "Stage", Lines: []string{"\x1b[41mplugin stage line"}}
	m.stageMode = stageMode
	m.showHotspots = true
	if overlays {
		m.plugins["p.modal"] = true
		m.railItems = append(m.railItems, contracts.RailItem{ID: "r.modal", Plugin: "p.modal", Icon: "m", Label: "modal", Placement: contracts.PlacementModal})
		m.openModal("p.modal", "r.modal")
		m.slotUpdates[protocol.SurfaceModal] = &protocol.SurfaceUpdateParams{PluginID: "p.modal", Surface: protocol.SurfaceModal, Lines: []string{"\x1b[42mmodal"}}
		m.activeToast = &contracts.Toast{Title: "t", Message: "m", Level: "warn"}
		m.showPalette = true
		m.paletteQuery = "a"
	}
	m.recomputeLayout()
	return m
}

// View must not modify the model: rendering a model leaves it deep-equal to
// an identically built model that was never rendered.
func TestViewIsPure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stageMode string
		overlays  bool
	}{
		{"plugin stage", "plugin", false},
		{"architecture explorer", "hotspots", false},
		{"explorer under modal toast and palette", "hotspots", true},
		{"plugin stage under modal toast and palette", "plugin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, fresh := pureViewModel(tc.stageMode, tc.overlays), pureViewModel(tc.stageMode, tc.overlays)
			stagePayload := rendered.slotUpdates[protocol.SurfaceMainStage]
			first := rendered.View().Content
			if second := rendered.View().Content; second != first {
				t.Fatal("two consecutive renders differ")
			}
			if rendered.slotUpdates[protocol.SurfaceMainStage] != stagePayload {
				t.Fatal("View replaced the main-stage payload")
			}
			// Func values are never DeepEqual; the modifier poller is injected
			// state that View does not touch.
			rendered.pollMods, fresh.pollMods = nil, nil
			rendered.actions, fresh.actions = nil, nil // closures are never DeepEqual
			if !reflect.DeepEqual(rendered, fresh) {
				t.Fatalf("View mutated the model:\nrendered %+v\nfresh    %+v", *rendered, *fresh)
			}
		})
	}
}

// Toggling the architecture explorer on and off must not lose what the
// main-stage plugin last sent (the explorer used to overwrite it).
func TestExplorerKeepsMainStagePluginPayload(t *testing.T) {
	m := pureViewModel("hotspots", false)
	m.showHotspots = false // the badge label would cover the start of the plugin row
	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "HUSK WORKBENCH SLOT ARCHITECTURE") || strings.Contains(frame, "plugin stage line") {
		t.Fatal("explorer mode should show the explorer, not the plugin")
	}
	if su := m.slotUpdates[protocol.SurfaceMainStage]; su == nil || su.PluginID != "p.stage" {
		t.Fatalf("main-stage payload after explorer render = %+v", su)
	}
	m.stageMode = "plugin"
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "plugin stage line") {
		t.Fatal("plugin content lost after leaving the explorer")
	}
}
