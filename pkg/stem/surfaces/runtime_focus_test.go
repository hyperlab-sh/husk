package surfaces

import (
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"testing"
)

func TestRuntimeModalFocusAdmission(t *testing.T) {
	for _, mode := range []string{"own", "settings", "other_modal"} {
		t.Run(mode, func(t *testing.T) {
			_, e, _ := newEngine(t, Options{})
			opened, err := e.SurfaceOpen("p", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementModal, Name: "dialog"})
			if err != nil {
				t.Fatal(err)
			}
			if err = e.UpdateView(func(v *contracts.SurfaceView) {
				switch mode {
				case "own":
					v.Overlays.Modal.Plugin = "p"
					v.Overlays.Modal.Surface = opened.Surface
				case "settings":
					v.Overlays.SettingsCenter = "general"
				case "other_modal":
					v.Overlays.Modal.Plugin = "other"
					v.Overlays.Modal.Surface = protocol.SurfaceModal
				}
			}); err != nil {
				t.Fatal(err)
			}
			err = e.SurfaceFocus("p", protocol.SurfaceFocusParams{Surface: opened.Surface})
			rows := e.RuntimeSurfaces()
			if len(rows) != 1 || rows[0].Surface != opened.Surface {
				t.Fatal("modal focus changed lifetime")
			}
			if mode == "own" {
				if err != nil || rows[0].FocusRevision != 1 {
					t.Fatalf("own modal focus: %+v %v", rows, err)
				}
			} else {
				if codeOf(t, err) != protocol.CodeBusy || err.(*Error).Data["reason"] != "modal_open" || rows[0].FocusRevision != 0 {
					t.Fatalf("modal admission: %+v %v", rows, err)
				}
			}
		})
	}
}

func TestRuntimeFocus(t *testing.T) {
	_, e, _ := newEngine(t, Options{})
	for _, name := range []string{"a", "b"} {
		if _, err := e.SurfaceOpen("p", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.SurfaceFocus("p", protocol.SurfaceFocusParams{Surface: "p.a"}); err != nil {
		t.Fatal(err)
	}
	rows := e.RuntimeSurfaces()
	if len(rows) != 2 || rows[1].Surface != "p.a" || rows[1].FocusRevision != 1 {
		t.Fatalf("focus did not promote A: %+v", rows)
	}
	for _, tc := range []struct {
		caller string
		id     protocol.SurfaceID
		code   protocol.Code
	}{
		{"other", "p.a", protocol.CodeDenied}, {"p", "p.closed", protocol.CodeNotFound}, {"p", protocol.SurfaceMainStage, protocol.CodeNotFound},
	} {
		if got := codeOf(t, e.SurfaceFocus(tc.caller, protocol.SurfaceFocusParams{Surface: tc.id})); got != tc.code {
			t.Fatalf("%s %s: %s want %s", tc.caller, tc.id, got, tc.code)
		}
	}
	if err := e.CloseSurface("p.a"); err != nil {
		t.Fatal(err)
	}
	if got := codeOf(t, e.SurfaceFocus("p", protocol.SurfaceFocusParams{Surface: "p.a"})); got != protocol.CodeNotFound {
		t.Fatalf("closed focus: %s", got)
	}
}
