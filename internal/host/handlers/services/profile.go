package services

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Profile adapts contracts.ProfilePort for the profile.* methods. Both are
// CLI methods; a plugin caller is denied before the port is touched.
type Profile struct {
	Port contracts.ProfilePort
}

type profileUseParams struct {
	Name string `json:"name"`
}

// Serve implements contracts.Handler.
func (h Profile) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodProfileList:
		profiles, err := h.Port.List(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if profiles == nil {
			profiles = []contracts.ProfileInfo{}
		}
		return handlers.Reply(req, profiles)
	case protocol.MethodProfileUse:
		var p profileUseParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		if err := h.Port.Use(ctx, c, p.Name); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	}
	return handlers.UnknownMethod(req)
}
