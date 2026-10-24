package host

import (
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"testing"
)

func TestRuntimeFocusSocket(t *testing.T) {
	noLeaks(t)
	h, f, fe := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
	w := dial(t, h.root.srv.SocketPath())
	token := f.proc("alpha").Token()
	for _, name := range []string{"a", "b"} {
		reply := runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, token, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: name})
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	reply := runtimeSocketCall(t, w, protocol.MethodSurfaceFocus, token, protocol.SurfaceFocusParams{Surface: "alpha.a"})
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	h.loop(func() {
		rows := fe.RuntimeSurfaces()
		if len(rows) != 2 || rows[1].Surface != "alpha.a" || rows[1].FocusRevision != 1 {
			t.Errorf("focus did not reach engine: %+v", rows)
		}
	})
	for _, tc := range []struct {
		token string
		id    protocol.SurfaceID
		code  protocol.Code
	}{
		{token, "alpha.closed", protocol.CodeNotFound}, {"", "alpha.a", protocol.CodeDenied},
	} {
		reply = runtimeSocketCall(t, w, protocol.MethodSurfaceFocus, tc.token, protocol.SurfaceFocusParams{Surface: tc.id})
		if reply.Error == nil || reply.Error.Code != tc.code {
			t.Fatalf("focus refusal=%+v want %s", reply, tc.code)
		}
	}
}
