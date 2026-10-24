package workbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
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
	engine  *surfaces.Engine
	viewErr error
}

func (f *runtimeSettingsFE) RuntimeSurfaces() []contracts.RuntimeSurface {
	return f.engine.RuntimeSurfaces()
}
func (f *runtimeSettingsFE) CloseSurface(id protocol.SurfaceID) error {
	return f.engine.CloseSurface(id)
}
func (f *runtimeSettingsFE) UpdateSurfaceView(fn func(*contracts.SurfaceView)) error {
	if f.viewErr != nil {
		return f.viewErr
	}
	if err := f.engine.UpdateView(fn); err != nil {
		return err
	}
	return f.fakeFE.UpdateSurfaceView(fn)
}

type runtimeModalNotices struct{ messages []protocol.Message }

func (n *runtimeModalNotices) Notify(msg protocol.Message) error {
	n.messages = append(n.messages, msg)
	return nil
}

func TestRuntimeModalClosesWhenSettingsViewWasUnpublished(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "saved", Owner: "husk"})
	editGeneral(m, "husk.value", "unsaved")
	center := m.settings
	before := *center.general.fields["husk.value"]
	deps := runtimeSettingsDeps{}
	st := state.New(state.Options{})
	engine, err := surfaces.New(st, surfaces.Options{Clock: deps, Logger: deps, Liveness: deps, Publications: deps, Declarations: deps})
	if err != nil {
		t.Fatal(err)
	}
	front := &runtimeSettingsFE{fakeFE: fe, engine: engine, viewErr: errors.New("view publication failed")}
	m.fe = front
	var logs bytes.Buffer
	m.logOut = &logs
	m.attachSlices()
	m.settle()
	if len(fe.Views) != 0 {
		t.Fatal("failed view publication unexpectedly reached the host")
	}
	var notices runtimeModalNotices
	engine.WatchPlugin("p", &notices)
	opened, err := engine.SurfaceOpen("p", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementModal, Name: "popup"})
	if err != nil {
		t.Fatal(err)
	}
	m.settle()
	if m.settings != center || m.modalOwner().Open() || len(m.runtime) != 0 || len(engine.RuntimeSurfaces()) != 0 {
		t.Fatal("unpublished Settings overlay allowed two live modals")
	}
	field := center.general.fields["husk.value"]
	if m.settingsTab() != "general" || !center.dirty() || field.text != before.text || field.revision != before.revision || !reflect.DeepEqual(field.baseline, before.baseline) {
		t.Fatal("defensive modal close changed Settings edits")
	}
	var res surfaces.Surfaces
	if err := json.Unmarshal(st.Snapshot().Slices[state.SurfacesNamespace].Value, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Runtime) != 0 || res.Occupants[opened.Surface].Plugin != "" {
		t.Fatal("defensive close retained runtime State")
	}
	if len(notices.messages) != 1 || notices.messages[0].Method != protocol.MethodSurfaceResized {
		t.Fatalf("close notices=%+v", notices.messages)
	}
	var resized protocol.SurfaceResizedParams
	if err := json.Unmarshal(notices.messages[0].Params, &resized); err != nil {
		t.Fatal(err)
	}
	if len(resized.Surfaces) != 1 || resized.Surfaces[0].Surface != opened.Surface || resized.Surfaces[0].Visible {
		t.Fatalf("final resize=%+v", resized)
	}
	if !strings.Contains(logs.String(), string(opened.Surface)) || !strings.Contains(logs.String(), "Settings") {
		t.Fatal("defensive modal dismissal was not logged")
	}
	front.viewErr = nil
	m.settle()
	if len(fe.Views) == 0 || fe.Views[len(fe.Views)-1].Overlays.SettingsCenter != "general" {
		t.Fatal("Settings overlay did not recover after view publication resumed")
	}
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
