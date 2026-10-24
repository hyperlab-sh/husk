package host

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The host deliberately refuses terminal queue offers to a process without
// TrySend. Give this fixture a recording, nonblocking terminal queue.
type runtimeLinkProcess struct{ *fakeProc }

func (p runtimeLinkProcess) TrySend(message protocol.Message) error { return p.Send(message) }

func TestRuntimeTerminalLinkDelivery(t *testing.T) {
	for _, placement := range []protocol.SurfacePlacement{protocol.SurfacePlacementModal, protocol.SurfacePlacementCanvas} {
		for _, source := range []string{"detected", "osc8"} {
			t.Run(string(placement)+"_"+source, func(t *testing.T) {
				noLeaks(t)
				f := newFakes()
				hook, ready := readyHook("")
				dir := writePlugin(t, "alpha", false)
				h, err := boot(t, func(c *contracts.Config) { pluginSettings(c)["paths"] = []string{dir} }, withHook(hook), WithFactory(func(spec manifest.Spec, options wrapper.Options) (wrapper.Process, error) {
					process, err := f.factory(spec, options)
					if err != nil {
						return nil, err
					}
					return runtimeLinkProcess{process.(*fakeProc)}, nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				runHost(t, ready, h)
				<-f.started
				fe := h.root.Frontend()
				w := dial(t, h.root.srv.SocketPath())
				token := f.proc("alpha").Token()
				call := func(method string, params any) protocol.Message {
					t.Helper()
					reply := runtimeSocketCall(t, w, method, token, params)
					if reply.Error != nil {
						t.Fatal(reply.Error)
					}
					return reply
				}
				reply := call(protocol.MethodSurfaceOpen, protocol.SurfaceOpenParams{Placement: placement, Name: "screen"})
				var opened protocol.SurfaceOpenResult
				if err := json.Unmarshal(reply.Result, &opened); err != nil {
					t.Fatal(err)
				}
				h.loop(func() {
					s, err := fe.OwnSlices()
					if err != nil {
						t.Error(err)
						return
					}
					if _, err = s.Geometry.Set(geometryOf(map[string][2]int{string(opened.Surface): {40, 4}})); err != nil {
						t.Error(err)
					}
					if err = fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { v.Visible = []protocol.SurfaceID{opened.Surface} }); err != nil {
						t.Error(err)
					}
				})
				call(protocol.MethodTerminalAttach, protocol.TerminalAttachParams{Surface: opened.Surface})
				text, url := "https://example.test", "https://example.test"
				if source == "osc8" {
					url = "custom://target"
					text = "\x1b]8;;" + url + "\x1b\\https://example.test\x1b]8;;\x1b\\"
				}
				data := "\x1b[?1000h\x1b[?1006h\r\n  " + text
				call(protocol.MethodTerminalWrite, protocol.TerminalWriteParams{Surface: opened.Surface, Data: base64.StdEncoding.EncodeToString([]byte(data))})
				h.loop(func() {
					mouse := uv.Mouse{X: 5, Y: 1, Button: uv.MouseLeft, Mod: uv.ModCtrl}
					fe.SendTerminalMouse(opened.Surface, uv.MouseClickEvent(mouse))
					fe.SendTerminalMouse(opened.Surface, uv.MouseReleaseEvent(mouse))
				})
				// Detach joins the encoder drain before inspecting consumed input.
				call(protocol.MethodTerminalDetach, protocol.TerminalDetachParams{Surface: opened.Surface})
				p := f.proc("alpha")
				p.mu.Lock()
				defer p.mu.Unlock()
				var links []protocol.TerminalLinkParams
				for _, message := range p.sent {
					switch message.Method {
					case protocol.MethodTerminalInput:
						t.Fatal("runtime link gesture encoded terminal input")
					case protocol.MethodTerminalLink:
						var link protocol.TerminalLinkParams
						if err := json.Unmarshal(message.Params, &link); err != nil {
							t.Fatal(err)
						}
						links = append(links, link)
					}
				}
				want := protocol.TerminalLinkParams{Surface: opened.Surface, URL: url, Row: 1, Col: 5}
				if len(links) != 1 || links[0] != want {
					t.Fatalf("runtime link deliveries = %+v, want %+v", links, want)
				}
			})
		}
	}
}
