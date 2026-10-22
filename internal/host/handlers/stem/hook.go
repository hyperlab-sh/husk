package stem

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Hook adapts contracts.HookPort for hook.call. The port blocks until every
// subscriber has answered or timed out and returns their results; the wire
// shape is one reply carrying all outcomes, per the protocol spec.
type Hook struct {
	Port contracts.HookPort
}

type hookCallParams struct {
	Hook string          `json:"hook"`
	Data json.RawMessage `json:"data"`
}

// hookOutcome is one subscriber's answer on the wire: data on success, or an
// error object with its code and message (for example the synthesized
// timeout). The reply is {"results":[...]}.
type hookOutcome struct {
	Plugin string          `json:"plugin_id"`
	Data   json.RawMessage `json:"data,omitempty"`
	Err    *protocol.Error `json:"error,omitempty"`
}

type hookCallResult struct {
	Results []hookOutcome `json:"results"`
}

// Serve implements contracts.Handler.
func (h Hook) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	if req.Method != protocol.MethodHookCall {
		return handlers.UnknownMethod(req)
	}
	var p hookCallParams
	if bad := decodeParams(req, &p); bad != nil {
		return *bad
	}
	if bad := requireString(req, "params.hook", p.Hook); bad != nil {
		return *bad
	}
	data, bad := rawObject(req, "params.data", p.Data, false)
	if bad != nil {
		return *bad
	}
	results, err := h.Port.HookCall(ctx, c, p.Hook, data)
	if err != nil {
		return handlers.ErrorReply(req, err)
	}
	out := make([]hookOutcome, 0, len(results))
	for _, r := range results {
		o := hookOutcome{Plugin: r.Plugin, Data: r.Data}
		if r.Err != nil {
			o.Err = &protocol.Error{Code: protocol.Code(r.Err.Code()), Message: r.Err.Error()}
			if !o.Err.Code.Valid() {
				o.Err.Code = protocol.CodeInternal
			}
		}
		out = append(out, o)
	}
	return handlers.Reply(req, hookCallResult{Results: out})
}
