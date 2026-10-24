package workbench

import (
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"os"
	"testing"
)

func TestRuntimeModalFocusPreservesLifetimeAndStack(t *testing.T) {
	for _, settings := range []bool{false, true} {
		t.Run(map[bool]string{false: "focused", true: "settings_race"}[settings], func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
			openRuntimeFixture(m, fe, "q.canvas", protocol.SurfacePlacementCanvas)
			m.hideRuntime()
			openRuntimeFixture(m, fe, "p.popup", protocol.SurfacePlacementModal)
			before := m.modal
			focus := m.runtimeFocus
			if settings {
				// Model the interval before Settings publishes its overlay. Normal
				// sequential openSettings closes the modal first and cannot hit this race.
				m.settings = &settingsCenter{tab: "general"}
			}
			fe.Runtime[2].FocusRevision++
			if !m.syncRuntime() {
				t.Fatal("modal activation was not observed")
			}
			if m.modal != before || len(fe.Runtime) != 3 {
				t.Fatal("modal activation replaced or closed the existing popup")
			}
			if !m.runtimeHidden || topCanvas(m.runtime) != "q.canvas" || m.runtimeFocus != focus {
				t.Fatal("modal focus reset the canvas stack")
			}
			if !settings && m.keyContext().FocusSurface != "p.popup" {
				t.Fatal("modal did not retain keyboard focus")
			}
		})
	}
}

func TestRuntimeFocusReorder(t *testing.T) {
	for _, mode := range []string{"shown", "hidden", "settings"} {
		t.Run(mode, func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			openRuntimeFixture(m, fe, "p.canvas", protocol.SurfacePlacementCanvas)
			openRuntimeFixture(m, fe, "q.canvas", protocol.SurfacePlacementCanvas)
			if mode != "shown" {
				m.activateHomeWorkbench()
			}
			if mode == "settings" {
				if _, err := m.openSettings(nil); err != nil {
					t.Fatal(err)
				}
			}
			a := fe.Runtime[0]
			a.FocusRevision++
			fe.Runtime = []contracts.RuntimeSurface{fe.Runtime[1], a}
			if !m.syncRuntime() || m.runtimeCanvas() != "p.canvas" || m.focusedSlot != "p.canvas" {
				t.Fatal("focus did not show A")
			}
			if (m.settings != nil) != (mode == "settings") {
				t.Fatal("focus changed Settings lifetime")
			}
			if mode == "shown" {
				frame := runGolden(t, m)
				want, err := os.ReadFile("testdata/TestGoldenFrameRuntimeCanvas.golden")
				if err != nil {
					t.Fatal(err)
				}
				if frame != string(want) {
					t.Fatal("focused canvas differs from opening the same canvas golden")
				}
			}
			// Refocusing the existing top also restores it after Home.
			if m.settings != nil {
				m.closeSettings()
			}
			m.activateHomeWorkbench()
			fe.Runtime[1].FocusRevision++
			m.syncRuntime()
			if m.runtimeHidden || m.runtimeCanvas() != "p.canvas" {
				t.Fatal("top focus failed to restore")
			}
		})
	}
}
