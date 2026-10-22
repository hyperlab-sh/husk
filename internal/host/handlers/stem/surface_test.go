package stem

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The surface family is plugin-only: the CLI, Stem and the unattributed
// zero kind are denied before the port.
func TestSurfaceAdmissionDenied(t *testing.T) {
	for name, caller := range map[string]contracts.Caller{"cli": cli, "stem": stemSide, "unattributed": nobody} {
		t.Run(name+" update", func(t *testing.T) {
			rec := fakes.NewRecorder()
			reply := Surface{Port: fakes.NewSurfaces(rec)}.Serve(ctx, caller,
				req(protocol.MethodSurfaceUpdate, `{"surface":"sidebar-list","lines":["Files"]}`), nil)
			wantCode(t, reply, protocol.CodeDenied)
			if calls := rec.Methods(); len(calls) != 0 {
				t.Fatalf("denied caller reached the port: %v", calls)
			}
		})
	}
}

func TestSurfaceUpdate(t *testing.T) {
	t.Run("happy path passes decoded params", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewSurfaces(rec)
		reply := Surface{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodSurfaceUpdate, `{"surface":"sidebar-list","title":"Files","lines":["a.go","b.go"]}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if string(reply.Result) != "{}" {
			t.Fatalf("result = %s", reply.Result)
		}
		calls := rec.Calls()
		if len(calls) != 1 || calls[0].Method != "Surfaces.SurfaceUpdate" || calls[0].Args[0] != protocol.SurfaceSidebarList {
			t.Fatalf("calls = %v", calls[0].Args)
		}
	})
	t.Run("unknown surface", func(t *testing.T) {
		reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin,
			req(protocol.MethodSurfaceUpdate, `{"surface":"magic","lines":[]}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("missing lines", func(t *testing.T) {
		reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin,
			req(protocol.MethodSurfaceUpdate, `{"surface":"modal-surface"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("port error maps", func(t *testing.T) {
		port := fakes.NewSurfaces(nil)
		port.Err = contracts.NewError(protocol.CodeDenied, "surface is not the caller's", nil)
		reply := Surface{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodSurfaceUpdate, `{"surface":"modal-surface","lines":[]}`), nil)
		wantCode(t, reply, protocol.CodeDenied)
	})
}

func TestSurfaceClose(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewSurfaces(rec)
		reply := Surface{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodSurfaceClose, `{"surface":"modal-surface"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); calls[0].Args[0] != protocol.SurfaceModal {
			t.Fatalf("args = %v", calls[0].Args)
		}
	})
	t.Run("unknown surface", func(t *testing.T) {
		reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin,
			req(protocol.MethodSurfaceClose, `{"surface":"nope"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
}

func TestSlotContribute(t *testing.T) {
	valid := `{"slot_id":"sidebar.item","id":"files","content":"Files","on_click":"open"}`
	t.Run("happy path passes params through", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewSurfaces(rec)
		reply := Surface{Port: port}.Serve(ctx, plugin, req(protocol.MethodSlotContribute, valid), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); calls[0].Method != "Surfaces.SlotContribute" || calls[0].Args[0] != "sidebar.item" {
			t.Fatalf("calls = %v", calls[0].Args)
		}
	})
	for field, params := range map[string]string{
		"params.slot_id": `{"id":"files","content":"Files"}`,
		"params.id":      `{"slot_id":"sidebar.item","content":"Files"}`,
	} {
		t.Run("missing "+field, func(t *testing.T) {
			reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin, req(protocol.MethodSlotContribute, params), nil)
			wantCode(t, reply, protocol.CodeInvalidParams)
		})
	}
	t.Run("empty content reaches the port", func(t *testing.T) {
		// D42: content is not a required field; an empty content is how a
		// contribution gets withdrawn, so the port owns that meaning.
		rec := fakes.NewRecorder()
		port := fakes.NewSurfaces(rec)
		reply := Surface{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodSlotContribute, `{"slot_id":"sidebar.item","id":"files"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); len(calls) != 1 || calls[0].Method != "Surfaces.SlotContribute" {
			t.Fatalf("calls = %v", calls)
		}
	})
}

func TestToastShow(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewSurfaces(rec)
		reply := Surface{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodToastShow, `{"level":"info","title":"Saved","message":"all files"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); calls[0].Args[0] != "Saved" {
			t.Fatalf("args = %v", calls[0].Args)
		}
	})
	t.Run("missing level and title reach the port", func(t *testing.T) {
		// D42: level and title are not marked required by any spec; the port
		// owns the defaults, so the adapter passes them through.
		rec := fakes.NewRecorder()
		reply := Surface{Port: fakes.NewSurfaces(rec)}.Serve(ctx, plugin,
			req(protocol.MethodToastShow, `{"message":"m"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); len(calls) != 1 || calls[0].Method != "Surfaces.ToastShow" {
			t.Fatalf("calls = %v", calls)
		}
	})
}

func TestSurfaceUnknownMethod(t *testing.T) {
	reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin, req("surface.paint", ""), nil)
	wantCode(t, reply, protocol.CodeUnknownMethod)
}
