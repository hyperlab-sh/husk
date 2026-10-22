package handlers

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// ServerHandler adapts a contracts.Handler — the Mux, or any single adapter —
// to the Server's Handler shape, so port adapters and the Server stay
// independent types. The Server attributes only CLI and plugin callers; a
// Stem caller never crosses the socket, so FromServerCaller never produces
// one. server.Notifier already satisfies contracts.Notifier, and the
// connection's notifier outlives the request, which is what the watch methods
// need.
type ServerHandler struct {
	Handler contracts.Handler
}

// Serve implements server.Handler.
func (a ServerHandler) Serve(ctx context.Context, c server.Caller, req protocol.Message, n server.Notifier) protocol.Message {
	return a.Handler.Serve(ctx, FromServerCaller(c), req, n)
}

// FromServerCaller converts the Server's caller attribution to a
// contracts.Caller: identical fields, kind by kind.
func FromServerCaller(c server.Caller) contracts.Caller {
	out := contracts.Caller{PluginID: c.PluginID, ConnID: c.ConnID}
	if c.Kind == server.CallerPlugin {
		out.Kind = contracts.CallerPlugin
	} else {
		out.Kind = contracts.CallerCLI
	}
	return out
}
