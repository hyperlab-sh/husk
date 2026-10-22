// Package plugin adapts contracts.PluginPort to the plugin.* wire methods.
package plugin

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Plugin adapts contracts.PluginPort for plugin.list and plugin.log. Both are
// CLI methods; a plugin caller is denied before the port is touched.
type Plugin struct {
	Port contracts.PluginPort
}

// logParams names the plugin whose host-side stderr log is wanted.
type logParams struct {
	Plugin string `json:"plugin"`
}

// logResult carries the log file path.
type logResult struct {
	Path string `json:"path"`
}

// Serve implements contracts.Handler.
func (h Plugin) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodPluginList:
		infos, err := h.Port.List(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if infos == nil {
			infos = []contracts.PluginInfo{}
		}
		return handlers.Reply(req, infos)
	case protocol.MethodPluginLog:
		var p logParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.plugin", p.Plugin); bad != nil {
			return *bad
		}
		path, err := h.Port.Log(ctx, c, p.Plugin)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, logResult{Path: path})
	}
	return handlers.UnknownMethod(req)
}
