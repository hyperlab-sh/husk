package services

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Keys adapts contracts.KeybindingPort for keys.list. The listing is caller-
// scoped by the port: users see the merged keymap, a plugin sees only its own
// bindings, and nothing here widens that.
type Keys struct {
	Port    contracts.KeybindingPort
	Limiter contracts.Limiter
}

// Serve implements contracts.Handler.
func (h Keys) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	if req.Method != protocol.MethodKeysList {
		return handlers.UnknownMethod(req)
	}
	// One budget with the other plugin-callable Services methods.
	if deny := admitRateLimited(req, c, h.Limiter); deny != nil {
		return *deny
	}
	res, err := h.Port.List(ctx, c)
	if err != nil {
		return handlers.ErrorReply(req, err)
	}
	return handlers.Reply(req, res)
}
