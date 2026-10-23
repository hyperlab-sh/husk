package host

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalSocketFloodAndDetach(t *testing.T) {
	noLeaks(t)
	h, f, fe := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
	h.loop(func() {
		s, err := fe.OwnSlices()
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = s.Geometry.Set(geometryDoc(1024, 3)); err != nil {
			t.Error(err)
		}
	})
	h.loop(func() {})
	w := dial(t, h.root.srv.SocketPath())
	writer := protocol.NewWriter(w.c)
	decoder := json.NewDecoder(w.r)
	token := f.proc("alpha").Token()
	call := func(method string, p any) {
		t.Helper()
		id := protocol.NewStringID("terminal")
		raw, _ := json.Marshal(p)
		if err := writer.Send(protocol.Message{ID: &id, Method: method, Token: token, Params: raw}); err != nil {
			t.Fatal(err)
		}
		var reply protocol.Message
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.Error != nil {
			t.Fatalf("%s: %s", method, reply.Error.Code)
		}
	}
	const id = protocol.SurfaceSidebarList
	call(protocol.MethodTerminalAttach, protocol.TerminalAttachParams{Surface: id})
	var want strings.Builder
	// The reference transport's acknowledged window is one. More than the
	// notification-admission cap crosses this real socket without losing bytes.
	for i := range 256 {
		chunk := fmt.Sprintf("%03d", i)
		want.WriteString(chunk)
		call(protocol.MethodTerminalWrite, protocol.TerminalWriteParams{Surface: id, Data: base64.StdEncoding.EncodeToString([]byte(chunk))})
	}
	h.loop(func() {
		screen := uv.NewScreenBuffer(1024, 3)
		fe.TerminalView(id).Draw(screen, uv.Rect(0, 0, 1024, 3))
		var got strings.Builder
		for x := range want.Len() {
			got.WriteString(screen.CellAt(x, 0).Content)
		}
		if got.String() != want.String() {
			t.Error("socket flood lost or reordered program bytes")
		}
	})
	call(protocol.MethodTerminalDetach, protocol.TerminalDetachParams{Surface: id})
	h.loop(func() {
		if fe.TerminalView(id) != nil {
			t.Error("detach did not discard emulator")
		}
	})
	call(protocol.MethodSurfaceUpdate, protocol.SurfaceUpdateParams{Surface: id, Lines: []string{"line mode restored"}})
}

func TestTerminalSocketWriteParamErrors(t *testing.T) {
	noLeaks(t)
	h, f, _ := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
	w := dial(t, h.root.srv.SocketPath())
	writer, decoder := protocol.NewWriter(w.c), json.NewDecoder(w.r)
	id := protocol.NewStringID("write")
	token := f.proc("alpha").Token()
	oversized := base64.StdEncoding.EncodeToString(make([]byte, protocol.MaxTerminalBytes+1))
	for _, tc := range []struct{ name, raw string }{
		{"mistyped", `{"surface":42}`},
		{"base64", `{"surface":"sidebar-list","data":"!"}`},
		{"missing_surface", `{"data":"eA=="}`},
		{"oversized", `{"surface":"sidebar-list","data":"` + oversized + `"}`},
	} {
		for _, notification := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/notification=%t", tc.name, notification), func(t *testing.T) {
				req := protocol.Message{ID: &id, Method: protocol.MethodTerminalWrite, Token: token, Params: json.RawMessage(tc.raw)}
				if notification {
					req.ID = nil
				}
				if err := w.c.SetDeadline(time.Now().Add(failBound)); err != nil {
					t.Fatal(err)
				}
				if err := writer.Send(req); err != nil {
					t.Fatal(err)
				}
				var reply protocol.Message
				if err := decoder.Decode(&reply); err != nil {
					t.Fatal(err)
				}
				if !notification {
					if reply.ID == nil || reply.ID.String() != id.String() || reply.Error == nil || reply.Error.Code != protocol.CodeInvalidParams {
						t.Fatal("invalid write request did not receive invalid_params")
					}
					return
				}
				if reply.ID != nil || reply.Method != protocol.MethodProtocolError || reply.Error != nil {
					t.Fatal("invalid write notification did not receive protocol.error")
				}
				var notice protocol.ProtocolErrorParams
				if err := json.Unmarshal(reply.Params, &notice); err != nil {
					t.Fatal(err)
				}
				if notice.Code != protocol.CodeInvalidParams || notice.Method != protocol.MethodTerminalWrite {
					t.Fatalf("notification error: %s for %s", notice.Code, notice.Method)
				}
			})
		}
	}
	// The connection survives bad params. Valid writes to unattached or foreign
	// surfaces retain their acknowledged log-and-drop policy.
	for _, surface := range []protocol.SurfaceID{protocol.SurfaceSidebarList, protocol.SurfaceMainStage} {
		raw, _ := json.Marshal(protocol.TerminalWriteParams{Surface: surface, Data: "eA=="})
		if err := w.c.SetDeadline(time.Now().Add(failBound)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Send(protocol.Message{ID: &id, Method: protocol.MethodTerminalWrite, Token: token, Params: raw}); err != nil {
			t.Fatal(err)
		}
		var reply protocol.Message
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.Error != nil || string(reply.Result) != "{}" {
			t.Fatal("valid refused write lost its empty acknowledgement")
		}
	}
}
