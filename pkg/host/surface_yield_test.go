package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

func TestSurfaceYieldSocketNotifications(t *testing.T) {
	for _, borrower := range []bool{false, true} {
		name := "immediate return"
		if borrower {
			name = "handoff and return"
		}
		t.Run(name, func(t *testing.T) {
			paths := []string{alphaPlugin(t)}
			if borrower {
				paths = append(paths, writeFrontendPlugin(t, "beta", []string{"sidebar-list"}, nil))
			}
			h, f, fe := bootFrontendWith(t, nil, paths...)
			var a, b recNotifier
			h.loop(func() {
				s, err := fe.OwnSlices()
				if err != nil {
					t.Error(err)
					return
				}
				h.root.engine.WatchPlugin("alpha", &a)
				h.root.engine.WatchPlugin("beta", &b)
				if _, err := s.Geometry.Set(geometryDoc(30, 10)); err != nil {
					t.Error(err)
				}
			})
			w := dial(t, h.root.srv.SocketPath())
			if err := w.c.SetDeadline(time.Now().Add(failBound)); err != nil {
				t.Fatal(err)
			}
			writer := protocol.NewWriter(w.c)
			for _, raw := range []string{
				`{"surface":"sidebar-list","yield":true}`,
				`{"surface":"modal-surface","yield":true}`,
			} {
				if err := writer.Send(protocol.Message{Method: protocol.MethodSurfaceClose, Token: f.proc("alpha").Token(), Params: json.RawMessage(raw)}); err != nil {
					t.Fatal(err)
				}
			}
			// Notifications on a connection run in order. The invalid modal
			// follows the valid yield and supplies a wire-level completion
			// barrier without relying on request-worker ordering or sleeps.
			var msg protocol.Message
			if err := json.NewDecoder(w.r).Decode(&msg); err != nil {
				t.Fatal(err)
			}
			if msg.ID != nil || msg.Method != protocol.MethodProtocolError || msg.Error != nil {
				t.Fatalf("invalid yield response = %+v, want protocol.error notification", msg)
			}
			var notice protocol.ProtocolErrorParams
			if err := json.Unmarshal(msg.Params, &notice); err != nil {
				t.Fatal(err)
			}
			if notice.Code != protocol.CodeInvalidParams || notice.Method != protocol.MethodSurfaceClose || !strings.Contains(notice.Message, "yield") {
				t.Fatalf("invalid modal yield = %+v, want invalid_params naming yield", notice)
			}
			h.loop(func() {
				res, _ := decodeSurfaces(fe.Snapshot())
				want, pushes := "alpha", a.resized(t)
				count := 2
				if borrower {
					want, pushes, count = "beta", b.resized(t), 1
				}
				if got := res.Occupants[protocol.SurfaceSidebarList].Plugin; got != want {
					t.Errorf("after socket yield occupant = %q, want %q", got, want)
				}
				if len(pushes) != count {
					t.Errorf("socket yield resize pushes = %+v, want %d", pushes, count)
				}
				var claims map[protocol.SurfaceID]struct {
					Plugin string `json:"plugin"`
				}
				if err := json.Unmarshal(fe.Snapshot().Slices["alpha.occupants"].Value, &claims); err != nil {
					t.Error(err)
					return
				}
				if claims[protocol.SurfaceSidebarList].Plugin != "alpha" {
					t.Error("socket yield retracted alpha's claim")
				}
			})
			if borrower {
				h.mustRemove("beta")
				h.loop(func() {
					res, _ := decodeSurfaces(fe.Snapshot())
					if res.Occupants[protocol.SurfaceSidebarList].Plugin != "alpha" {
						t.Error("socket yielder did not regain occupancy")
					}
					if got := len(a.resized(t)); got != 2 {
						t.Errorf("return resize count = %d, want 2", got)
					}
				})
			}
		})
	}
}

func TestMarkSizesDirtyUsesFlushSnapshot(t *testing.T) {
	r := &Root{
		st: state.New(state.Options{}),
		lastSizes: map[protocol.SurfaceID]pushedSize{
			protocol.SurfaceMainStage:   {Plugin: "alpha"},
			protocol.SurfaceSidebarList: {Plugin: "beta"},
		},
	}
	snap := &state.Snapshot{Slices: map[string]state.Entry{
		state.SurfacesNamespace: {Value: json.RawMessage(`{"occupants":{"sidebar-list":{"surface":"sidebar-list","plugin":"beta"}}}`)},
	}}
	r.markSizesDirty([]state.Change{{Namespace: state.SurfacesNamespace}}, snap)
	if !r.sizesDirty {
		t.Error("surface flush did not dirty sizes")
	}
	if _, ok := r.lastSizes[protocol.SurfaceMainStage]; ok {
		t.Error("vacated surface stayed cached despite flush snapshot")
	}
	if _, ok := r.lastSizes[protocol.SurfaceSidebarList]; !ok {
		t.Error("occupied surface lost its cache entry")
	}
}

func TestSurfaceYieldResizedAndRemoval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		borrower bool
		remove   bool
	}{
		{"immediate return is resized", false, false},
		{"handoff and return are resized", true, false},
		{"removed yielder never returns", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{alphaPlugin(t)}
			if tc.borrower {
				paths = append(paths, writeFrontendPlugin(t, "beta", []string{"sidebar-list"}, nil))
			}
			h, _, fe := bootFrontendWith(t, nil, paths...)
			var a, b recNotifier
			h.loop(func() {
				s, err := fe.OwnSlices()
				if err != nil {
					t.Error(err)
					return
				}
				h.root.engine.WatchPlugin("alpha", &a)
				h.root.engine.WatchPlugin("beta", &b)
				if _, err := s.Geometry.Set(geometryDoc(30, 10)); err != nil {
					t.Error(err)
				}
			})
			h.loop(func() {
				if err := h.root.engine.SurfaceClose("alpha", protocol.SurfaceCloseParams{Surface: protocol.SurfaceSidebarList, Yield: true}); err != nil {
					t.Error(err)
				}
			})
			h.loop(func() {
				res, _ := decodeSurfaces(fe.Snapshot())
				want := "alpha"
				if tc.borrower {
					want = "beta"
				}
				if res.Occupants[protocol.SurfaceSidebarList].Plugin != want {
					t.Errorf("occupants after yield = %+v, want %s", res.Occupants, want)
				}
				pushes := a.resized(t)
				if tc.borrower {
					pushes = b.resized(t)
				}
				count := 2
				if tc.borrower {
					count = 1
				}
				if len(pushes) != count {
					t.Errorf("resize pushes after yield = %+v, want %d", pushes, count)
					return
				}
				if size := pushes[count-1][0]; size.Width != 30 || size.Height != 10 {
					t.Errorf("resize = %+v, want 30x10", size)
				}
			})
			if !tc.borrower {
				return
			}
			if tc.remove {
				h.mustRemove("alpha")
			}
			h.loop(func() {
				if err := h.root.engine.SurfaceClose("beta", protocol.SurfaceCloseParams{Surface: protocol.SurfaceSidebarList}); err != nil {
					t.Error(err)
				}
			})
			h.loop(func() {
				res, _ := decodeSurfaces(fe.Snapshot())
				want, count := "alpha", 2
				if tc.remove {
					want, count = "", 1
				}
				if got := res.Occupants[protocol.SurfaceSidebarList].Plugin; got != want {
					t.Errorf("after borrower closes occupant = %q, want %q", got, want)
				}
				if got := len(a.resized(t)); got != count {
					t.Errorf("alpha resize count = %d, want %d", got, count)
				}
			})
		})
	}
}
