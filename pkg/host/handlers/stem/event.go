package stem

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Event adapts contracts.EventPort for event.emit and event.watch. Watch
// registers the connection's notifier with the port; the watch reply is
// empty — delivery is one event.deliver notification per matching emit on
// the same connection (protocol.md), with no baseline payload.
type Event struct {
	Port contracts.EventPort
}

type emitParams struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

type eventWatchParams struct {
	Pattern string `json:"pattern"`
}

// Serve implements contracts.Handler.
func (h Event) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, n contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodEventEmit:
		var p emitParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		data, bad := rawObject(req, "params.data", p.Data, false)
		if bad != nil {
			return *bad
		}
		if err := h.Port.Emit(ctx, c, p.Name, data); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodEventWatch:
		var p eventWatchParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.pattern", p.Pattern); bad != nil {
			return *bad
		}
		if err := h.Port.Watch(ctx, c, p.Pattern, n); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	}
	return handlers.UnknownMethod(req)
}
