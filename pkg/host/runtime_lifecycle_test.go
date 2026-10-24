package host

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/pkg/substrate/state/surfaces"
)

func runtimeSocketCall(t *testing.T, w wireConn, method, token string, params any) protocol.Message {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	id := protocol.NewStringID("runtime-lifecycle")
	if err := protocol.NewWriter(w.c).Send(protocol.Message{ID: &id, Method: method, Token: token, Params: raw}); err != nil {
		t.Fatal(err)
	}
	line, err := w.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestRuntimeSurfaceSocketPodExitAndRemove(t *testing.T) {
	for _, path := range []string{"process_exit", "plugin.remove"} {
		t.Run(path, func(t *testing.T) {
			noLeaks(t)
			h, f, fe := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
			w := dial(t, h.root.srv.SocketPath())
			p := f.proc("alpha")
			opened := runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, p.Token(), protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: "screen"})
			if opened.Error != nil {
				t.Fatal(opened.Error)
			}
			h.loop(func() {
				if rows := fe.RuntimeSurfaces(); len(rows) != 1 || rows[0].Surface != "alpha.screen" {
					t.Errorf("open not committed: %+v", rows)
				}
			})
			switch path {
			case "process_exit":
				if err := p.Kill(); err != nil {
					t.Fatal(err)
				}
				<-p.Done()
				// The fake wrapper, unlike the OS wrapper, does not emit its own exit.
				// Post the completed process's notice through the real Pods lifecycle.
				p.opts.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Failed, LastError: "process killed"}})
			case "plugin.remove":
				reply := runtimeSocketCall(t, w, protocol.MethodPluginRemove, "", map[string]any{"plugin": "alpha"})
				if reply.Error != nil {
					t.Fatal(reply.Error)
				}
			}
			h.loop(func() {
				var res statessurfaces.Surfaces
				if err := json.Unmarshal(fe.Snapshot().Slices[state.SurfacesNamespace].Value, &res); err != nil {
					t.Error(err)
					return
				}
				if len(res.Runtime) != 0 || res.Occupants["alpha.screen"].Plugin != "" || len(fe.RuntimeSurfaces()) != 0 {
					t.Errorf("%s retained runtime surfaces: %+v", path, res)
				}
			})
		})
	}
}

func TestRuntimeSurfaceSocketAdmissions(t *testing.T) {
	noLeaks(t)
	h, f, fe := bootFrontendWith(t, nil, writePlugin(t, "alpha", false))
	w := dial(t, h.root.srv.SocketPath())
	token := f.proc("alpha").Token()
	for _, p := range []protocol.SurfaceOpenParams{
		{Placement: protocol.SurfacePlacementCanvas, Name: strings.Repeat("x", 33)},
		{Placement: protocol.SurfacePlacementModal, Name: "popup", Title: strings.Repeat("界", 81)},
	} {
		reply := runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, token, p)
		if reply.Error == nil || reply.Error.Code != protocol.CodeInvalidParams {
			t.Fatalf("invalid open reply=%+v", reply.Error)
		}
	}
	for i := 0; i < 9; i++ {
		reply := runtimeSocketCall(t, w, protocol.MethodSurfaceOpen, token, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: fmt.Sprintf("s%d", i)})
		if i < 8 && reply.Error != nil {
			t.Fatal(reply.Error)
		}
		if i == 8 && (reply.Error == nil || reply.Error.Code != protocol.CodeBusy) {
			t.Fatalf("ninth open reply=%+v", reply.Error)
		}
	}
	h.loop(func() {
		if len(fe.RuntimeSurfaces()) != 8 {
			t.Error("refused opens changed capacity")
		}
	})
}
