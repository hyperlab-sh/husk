package host

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
