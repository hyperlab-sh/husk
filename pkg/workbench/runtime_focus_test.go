package workbench

import (
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"os"
	"testing"
)

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
