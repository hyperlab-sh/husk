package host

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/pkg/substrate/state/surfaces"
)

func TestRuntimeSocketSettingsModalAdmission(t *testing.T) {
	noLeaks(t)
	h, f, fe := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
	w := dial(t, h.root.srv.SocketPath())
	token := f.proc("alpha").Token()
	for i, tab := range []string{"general", "keys", "theme", "store", "installed"} {
		h.loop(func() {
			if err := fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { v.Overlays.SettingsCenter = tab }); err != nil {
				t.Error(err)
			}
		})
		reply := runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, token, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementModal, Name: "dialog"})
		if reply.Error == nil || reply.Error.Code != protocol.CodeBusy {
			t.Fatalf("%s modal refusal=%+v", tab, reply.Error)
		}
		var data struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(reply.Error.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Reason != "modal_open" {
			t.Fatalf("busy reason=%q", data.Reason)
		}
		name := fmt.Sprintf("screen%d", i)
		reply = runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, token, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: name})
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
		h.loop(func() {
			var res statessurfaces.Surfaces
			if err := json.Unmarshal(fe.Snapshot().Slices[state.SurfacesNamespace].Value, &res); err != nil {
				t.Error(err)
				return
			}
			if res.Overlays.SettingsCenter != tab || res.Overlays.Modal.Plugin != "" || res.Runtime["alpha.dialog"].Plugin != "" || len(res.Runtime) != i+1 || res.Runtime[protocol.SurfaceID("alpha."+name)].Plugin != "alpha" {
				t.Errorf("Settings/modal/canvas state=%+v", res)
			}
		})
	}
	// Refusal consumes neither the name nor the singleton after Settings closes.
	h.loop(func() {
		if err := fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { v.Overlays.SettingsCenter = "" }); err != nil {
			t.Error(err)
		}
	})
	reply := runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, token, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementModal, Name: "dialog"})
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
}
