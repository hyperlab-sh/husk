package host

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/pkg/substrate/state/surfaces"
)

func TestRuntimeSurfaceSocketTerminalAndCleanup(t *testing.T) {
	noLeaks(t)
	h, f, fe := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
	w := dial(t, h.root.srv.SocketPath())
	writer, dec := protocol.NewWriter(w.c), json.NewDecoder(w.r)
	token := f.proc("alpha").Token()
	call := func(method string, p any, credential string) protocol.Message {
		t.Helper()
		raw, _ := json.Marshal(p)
		id := protocol.NewStringID("runtime")
		if err := writer.Send(protocol.Message{ID: &id, Method: method, Token: credential, Params: raw}); err != nil {
			t.Fatal(err)
		}
		var reply protocol.Message
		if err := dec.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	denied := call(protocol.MethodSurfaceOpen, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: "denied"}, "")
	if denied.Error == nil || denied.Error.Code != protocol.CodeDenied {
		t.Fatal("CLI opened runtime surface")
	}
	reply := call(protocol.MethodSurfaceOpen, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: "screen"}, token)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var opened protocol.SurfaceOpenResult
	if err := json.Unmarshal(reply.Result, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Surface != "alpha.screen" {
		t.Fatalf("id=%s", opened.Surface)
	}
	h.loop(func() {
		if rows := fe.RuntimeSurfaces(); len(rows) != 1 || rows[0].Surface != opened.Surface {
			t.Error("frontend runtime seam lost open")
		}
		s, err := fe.OwnSlices()
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = s.Geometry.Set(geometryOf(map[string][2]int{string(opened.Surface): {18, 4}})); err != nil {
			t.Error(err)
		}
		if _, err = s.Focus.Set(json.RawMessage(`{"surface":"alpha.screen"}`)); err != nil {
			t.Error(err)
		}
		if err = fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { v.Visible = []protocol.SurfaceID{opened.Surface} }); err != nil {
			t.Error(err)
		}
	})
	reply = call(protocol.MethodTerminalAttach, protocol.TerminalAttachParams{Surface: opened.Surface}, token)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var attached protocol.TerminalAttachResult
	_ = json.Unmarshal(reply.Result, &attached)
	if attached.Width != 18 || attached.Height != 4 {
		t.Fatalf("attach inner geometry=%+v", attached)
	}
	reply = call(protocol.MethodTerminalWrite, protocol.TerminalWriteParams{Surface: opened.Surface, Data: base64.StdEncoding.EncodeToString([]byte("runtime terminal"))}, token)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	h.loop(func() {
		if fe.TerminalView(opened.Surface) == nil {
			t.Error("runtime attachment missing")
		}
	})
	reply = call(protocol.MethodSurfaceClose, protocol.SurfaceCloseParams{Surface: opened.Surface, Yield: true}, token)
	if reply.Error == nil || reply.Error.Code != protocol.CodeInvalidParams {
		t.Fatal("runtime yield was accepted")
	}
	reply = call(protocol.MethodSurfaceClose, protocol.SurfaceCloseParams{Surface: opened.Surface}, token)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	h.loop(func() {
		var res statessurfaces.Surfaces
		_ = json.Unmarshal(fe.Snapshot().Slices[state.SurfacesNamespace].Value, &res)
		if len(res.Runtime) != 0 || res.Occupants[opened.Surface].Plugin != "" || len(res.Terminals) != 0 || len(res.Capture) != 0 || fe.TerminalView(opened.Surface) != nil {
			t.Error("runtime close retained occupancy, emulator or capture")
		}
	})
}
