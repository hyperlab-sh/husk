package stem

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

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
		"params.content": `{"slot_id":"sidebar.item","id":"files"}`,
	} {
		t.Run("missing "+field, func(t *testing.T) {
			reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin, req(protocol.MethodSlotContribute, params), nil)
			wantCode(t, reply, protocol.CodeInvalidParams)
		})
	}
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
	t.Run("missing level", func(t *testing.T) {
		reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin,
			req(protocol.MethodToastShow, `{"title":"t","message":"m"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("missing title", func(t *testing.T) {
		reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin,
			req(protocol.MethodToastShow, `{"level":"info","message":"m"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
}

func TestSurfaceUnknownMethod(t *testing.T) {
	reply := Surface{Port: fakes.NewSurfaces(nil)}.Serve(ctx, plugin, req("surface.paint", ""), nil)
	wantCode(t, reply, protocol.CodeUnknownMethod)
}
