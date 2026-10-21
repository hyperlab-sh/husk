package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// pureViewModel builds a model exercising every View branch that reads
// plugin state: an occupied main stage, hotspots, modal, toast and palette.
func pureViewModel(stageMode string, overlays bool) *WorkbenchModel {
	m := newWorkbenchModel("", nil, "")
	m.logOut = nil
	m.width, m.height = 144, 40
	m.plugins["p.stage"] = &ConnectedPlugin{Info: huskipc.HelloPayload{PluginID: "p.stage"}}
	m.registry.Occupants[huskipc.SlotMainStage] = "p.stage"
	m.slotUpdates[huskipc.SlotMainStage] = &huskipc.SlotUpdatePayload{PluginID: "p.stage", Slot: huskipc.SlotMainStage, Title: "Stage", Lines: []string{"\x1b[41mplugin stage line"}}
	m.stageMode = stageMode
	m.showHotspots = true
	if overlays {
		m.plugins["p.modal"] = &ConnectedPlugin{Info: huskipc.HelloPayload{PluginID: "p.modal"}}
		m.railItems = append(m.railItems, huskipc.RailItemSpec{ID: "r.modal", PluginID: "p.modal", Icon: "m", Label: "modal", Placement: huskipc.PlacementModal})
		m.openModal("p.modal", "r.modal")
		m.slotUpdates[huskipc.SlotModalSurface] = &huskipc.SlotUpdatePayload{PluginID: "p.modal", Slot: huskipc.SlotModalSurface, Lines: []string{"\x1b[42mmodal"}}
		m.activeToast = &huskipc.ToastPayload{Title: "t", Message: "m", Level: "warn"}
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
			stagePayload := rendered.slotUpdates[huskipc.SlotMainStage]
			first := rendered.View().Content
			if second := rendered.View().Content; second != first {
				t.Fatal("two consecutive renders differ")
			}
			if rendered.slotUpdates[huskipc.SlotMainStage] != stagePayload {
				t.Fatal("View replaced the main-stage payload")
			}
			fresh.subChan = rendered.subChan // channel identity differs by construction
			// Func values are never DeepEqual; the modifier poller is injected
			// state that View does not touch.
			rendered.pollMods, fresh.pollMods = nil, nil
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
	if su := m.slotUpdates[huskipc.SlotMainStage]; su == nil || su.PluginID != "p.stage" {
		t.Fatalf("main-stage payload after explorer render = %+v", su)
	}
	m.stageMode = "plugin"
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "plugin stage line") {
		t.Fatal("plugin content lost after leaving the explorer")
	}
}
