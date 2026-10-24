package workbench

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/surfaces"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

// Keep the real admission engine and view-write seam, with unrelated process,
// clock and publication dependencies deterministic and unused.
type runtimeSettingsDeps struct{}

func (runtimeSettingsDeps) Now() time.Time              { return time.Unix(1, 0) }
func (runtimeSettingsDeps) Live(id string) bool         { return id == "p" }
func (runtimeSettingsDeps) Owner(string) (string, bool) { return "", false }
func (runtimeSettingsDeps) Printf(string, ...any)       {}
func (runtimeSettingsDeps) Contribute(string, string, surfaces.Contribution) error {
	return errors.New("unexpected contribution")
}
func (runtimeSettingsDeps) Release(string, protocol.SurfaceID) error {
	return errors.New("unexpected layout release")
}

type runtimeSettingsFE struct {
	*fakeFE
	engine *surfaces.Engine
}

func (f *runtimeSettingsFE) RuntimeSurfaces() []contracts.RuntimeSurface {
	return f.engine.RuntimeSurfaces()
}
func (f *runtimeSettingsFE) CloseSurface(id protocol.SurfaceID) error {
	return f.engine.CloseSurface(id)
}
func (f *runtimeSettingsFE) UpdateSurfaceView(fn func(*contracts.SurfaceView)) error {
	if err := f.engine.UpdateView(fn); err != nil {
		return err
	}
	return f.fakeFE.UpdateSurfaceView(fn)
}

func TestRuntimeOpenPreservesSettingsEdits(t *testing.T) {
	for _, tab := range []string{"general", "keys", "theme", "store", "installed"} {
		for _, placement := range []protocol.SurfacePlacement{protocol.SurfacePlacementModal, protocol.SurfacePlacementCanvas} {
			t.Run(tab+"_"+string(placement), func(t *testing.T) {
				m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "saved", Owner: "husk"})
				editGeneral(m, "husk.value", "unsaved")
				if _, err := m.openSettings(map[string]any{"tab": tab}); err != nil {
					t.Fatal(err)
				}
				center := m.settings
				field := center.general.fields["husk.value"]
				before := *field
				deps := runtimeSettingsDeps{}
				engine, err := surfaces.New(state.New(state.Options{}), surfaces.Options{Clock: deps, Logger: deps, Liveness: deps, Publications: deps, Declarations: deps})
				if err != nil {
					t.Fatal(err)
				}
				m.fe = &runtimeSettingsFE{fakeFE: fe, engine: engine}
				m.attachSlices()
				m.settle()
				if got := fe.Views[len(fe.Views)-1].Overlays.SettingsCenter; got != tab {
					t.Fatalf("Settings overlay=%q want %q", got, tab)
				}
				opened, err := engine.SurfaceOpen("p", protocol.SurfaceOpenParams{Placement: placement, Name: "surface"})
				if placement == protocol.SurfacePlacementModal {
					var refusal *surfaces.Error
					if !errors.As(err, &refusal) || refusal.Code != protocol.CodeBusy || refusal.Data["reason"] != "modal_open" {
						t.Fatalf("modal admission=%+v, %v", opened, err)
					}
					if len(engine.RuntimeSurfaces()) != 0 {
						t.Fatal("refused modal acquired runtime lifetime")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if opened.Surface != "p.surface" {
						t.Fatalf("canvas id=%s", opened.Surface)
					}
				}
				m.settle()
				if m.settings != center || m.settingsTab() != tab || !center.dirty() || field.text != before.text || field.revision != before.revision || !reflect.DeepEqual(field.baseline, before.baseline) || len(fe.SettingWrites) != 0 {
					t.Fatal("runtime open changed Settings, its tab or unsaved fields")
				}
				if m.modalOwner().Open() || m.surfaceView().Overlays.SettingsCenter != tab {
					t.Fatal("runtime open displaced the Settings overlay")
				}
				if placement == protocol.SurfacePlacementCanvas && (m.runtimeCanvas() != opened.Surface || !slices.Contains(m.surfaceView().Visible, opened.Surface)) {
					t.Fatal("canvas was not accepted behind Settings")
				}
				settingsPress(m, "f9")
				if m.settings != center || len(fe.Keys) != 0 {
					t.Fatal("Settings stopped receiving keys after runtime open")
				}
			})
		}
	}
}
