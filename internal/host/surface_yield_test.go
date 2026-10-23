package host

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

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
