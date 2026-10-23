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
	// Closing, when set, ends every request's context once it is done (the
	// host is shutting down), so a port waiting on a loop that no longer
	// answers returns a cancelled error at once.
	Closing context.Context
}

// retainsContext lists the methods whose port keeps the request context after
// Serve returns and unregisters when it ends: the watch methods, the only ones
// that use the Notifier (contracts.Notifier: "Only watch methods use it").
// Cancelling their context on return would silence the watch.
var retainsContext = map[string]bool{
	protocol.MethodEventWatch: true,
	protocol.MethodStateWatch: true,
}

// Serve implements server.Handler.
func (a ServerHandler) Serve(ctx context.Context, c server.Caller, req protocol.Message, n server.Notifier) protocol.Message {
	if a.Closing != nil {
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		// Every request context is released when Serve returns, so a
		// long-lived connection sending requests all day does not accumulate
		// children on its context. Only the watches keep their context past
		// Serve; theirs ends with the connection instead.
		stop := context.AfterFunc(a.Closing, func() { cancel(contracts.ErrShutdown) })
		if retainsContext[req.Method] {
			// A watch still hears the shutdown after Serve returns; the hook
			// goes when its context ends.
			context.AfterFunc(ctx, func() { stop() })
		} else {
			defer cancel(nil)
			defer stop()
		}
	}
	return a.Handler.Serve(ctx, FromServerCaller(c), req, n)
}

// FromServerCaller converts the Server's caller attribution to a
// contracts.Caller: identical fields, kind by kind. A kind the Server does
// not attribute — the zero kind — stays the zero kind, so Admit denies it
// (D36) instead of handing it CLI privileges.
func FromServerCaller(c server.Caller) contracts.Caller {
	out := contracts.Caller{PluginID: c.PluginID, ConnID: c.ConnID}
	switch c.Kind {
	case server.CallerPlugin:
		out.Kind = contracts.CallerPlugin
	case server.CallerCLI:
		out.Kind = contracts.CallerCLI
	}
	return out
}
