package stem

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Session adapts contracts.SessionPort: session.ping, whose reply the port
// builds from instance identity.
type Session struct {
	Port contracts.SessionPort
}

// Serve implements contracts.Handler.
func (h Session) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	if req.Method != protocol.MethodSessionPing {
		return handlers.UnknownMethod(req)
	}
	res, err := h.Port.Ping(ctx, c)
	if err != nil {
		return handlers.ErrorReply(req, err)
	}
	return handlers.Reply(req, res)
}
