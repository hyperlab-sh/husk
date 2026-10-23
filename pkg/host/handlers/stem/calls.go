package stem

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Calls adapts contracts.CallsPort for the action.* methods: action.invoke
// runs one declared action, action.list returns the caller's catalog.
type Calls struct {
	Port contracts.CallsPort
}

type invokeParams struct {
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args"`
}

// Serve implements contracts.Handler.
func (h Calls) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodActionInvoke:
		var p invokeParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.action", p.Action); bad != nil {
			return *bad
		}
		args, bad := rawObject(req, "params.args", p.Args, false)
		if bad != nil {
			return *bad
		}
		res, err := h.Port.ActionInvoke(ctx, c, p.Action, args)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return replyRaw(req, res)
	case protocol.MethodActionList:
		entries, err := h.Port.ActionList(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if entries == nil {
			entries = []contracts.ActionInfo{}
		}
		return handlers.Reply(req, entries)
	}
	return handlers.UnknownMethod(req)
}
