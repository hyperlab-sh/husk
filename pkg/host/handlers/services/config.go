package services

import (
	"context"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Config adapts contracts.ConfigPort for config.reload. It is a CLI method;
// a plugin caller is denied before the port is touched. The port (and the
// coordinator behind it) runs the reload through its loop seam, so Serve only
// encodes the resulting per-service reports.
type Config struct {
	Port contracts.ConfigPort
}

type configReloadResult struct {
	Reports []contracts.ReloadReport `json:"reports"`
}

// Serve implements contracts.Handler.
func (h Config) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	if req.Method != protocol.MethodConfigReload {
		return handlers.UnknownMethod(req)
	}
	reports, err := h.Port.Reload(ctx, c)
	if err != nil {
		return handlers.ErrorReply(req, err)
	}
	if reports == nil {
		reports = []contracts.ReloadReport{}
	}
	return handlers.Reply(req, configReloadResult{Reports: reports})
}
