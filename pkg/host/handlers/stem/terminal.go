package stem

import (
	"context"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Terminal adapts the plugin-only terminal byte stream methods.
type Terminal struct {
	Port contracts.TerminalPort
}

// Serve implements contracts.Handler.
func (h Terminal) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodTerminalAttach:
		var p protocol.TerminalAttachParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if err := p.Validate(); err != nil {
			return handlers.InvalidParams(req, "params", err.Error())
		}
		result, err := h.Port.TerminalAttach(ctx, c, p)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, result)
	case protocol.MethodTerminalWrite:
		var p protocol.TerminalWriteParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if _, err := p.Decode(); err != nil {
			return handlers.InvalidParams(req, "params", err.Error())
		}
		if err := h.Port.TerminalWrite(ctx, c, p); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodTerminalDetach:
		var p protocol.TerminalDetachParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := validSurface(req, p.Surface); bad != nil {
			return *bad
		}
		if err := h.Port.TerminalDetach(ctx, c, p); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	}
	return handlers.UnknownMethod(req)
}
