package services

import (
	"context"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Profile adapts contracts.ProfilePort for the profile.* methods. Both are
// CLI methods; a plugin caller is denied before the port is touched.
type Profile struct {
	Port contracts.ProfilePort
}

// profileUseParams: "" selects no profile, so the name is required to be
// present, not non-empty.
type profileUseParams struct {
	Name *string `json:"name"`
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
		return handlers.Reply(req, struct {
			Profiles []contracts.ProfileInfo `json:"profiles"`
		}{profiles})
	case protocol.MethodProfileUse:
		var p profileUseParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if p.Name == nil {
			return handlers.InvalidParams(req, "params.name", "is required")
		}
		res, err := h.Port.Use(ctx, c, *p.Name)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if res.Reports == nil {
			res.Reports = []contracts.ReloadReport{}
		}
		return handlers.Reply(req, res)
	}
	return handlers.UnknownMethod(req)
}
