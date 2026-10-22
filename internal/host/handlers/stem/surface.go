package stem

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Surface adapts contracts.SurfacePort for surface.update, surface.close,
// slot.contribute and toast.show. The port owns the surface/slot/toast
// prefix family; the adapter owns each method's field requirements.
type Surface struct {
	Port contracts.SurfacePort
}

// Serve implements contracts.Handler.
func (h Surface) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodSurfaceUpdate:
		var p protocol.SurfaceUpdateParams
		if bad := decodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := validSurface(req, p.Surface); bad != nil {
			return *bad
		}
		if p.Lines == nil {
			bad := handlers.InvalidParams(req, "params.lines", "is required")
			return bad
		}
		if err := h.Port.SurfaceUpdate(ctx, c, p); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodSurfaceClose:
		var p protocol.SurfaceCloseParams
		if bad := decodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := validSurface(req, p.Surface); bad != nil {
			return *bad
		}
		if err := h.Port.SurfaceClose(ctx, c, p); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodSlotContribute:
		var p protocol.SlotContributeParams
		if bad := decodeParams(req, &p); bad != nil {
			return *bad
		}
		for _, field := range []struct{ name, value string }{
			{"params.slot_id", p.SlotID},
			{"params.id", p.ID},
			{"params.content", p.Content},
		} {
			if bad := requireString(req, field.name, field.value); bad != nil {
				return *bad
			}
		}
		if err := h.Port.SlotContribute(ctx, c, p); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodToastShow:
		var p protocol.ToastShowParams
		if bad := decodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := requireString(req, "params.level", p.Level); bad != nil {
			return *bad
		}
		if bad := requireString(req, "params.title", p.Title); bad != nil {
			return *bad
		}
		if err := h.Port.ToastShow(ctx, c, p); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	}
	return handlers.UnknownMethod(req)
}

// validSurface rejects a surface id that is not one of the named host
// surfaces in pkg/protocol.
func validSurface(req protocol.Message, surface protocol.SurfaceID) *protocol.Message {
	if !surface.Valid() {
		bad := handlers.InvalidParams(req, "params.surface", "must be a named host surface")
		return &bad
	}
	return nil
}
