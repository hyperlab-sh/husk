// Package plugin adapts contracts.PluginPort to the plugin.* wire methods.
package plugin

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Plugin adapts contracts.PluginPort for plugin.list, plugin.log, plugin.add and plugin.remove. All
// are CLI methods; a plugin caller is denied before the port is touched. The
// params and result shapes are specified in docs/docs/design/pods.md, section
// "plugin.* wire shapes".
type Plugin struct {
	Port contracts.PluginPort
}

// removeParams names the plugin to unload.
type removeParams = logParams

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
	case protocol.MethodPluginAdd:
		var fields map[string]json.RawMessage
		if bad := handlers.DecodeParams(req, &fields); bad != nil {
			return *bad
		}
		path, hasPath := fields["path"]
		dir, hasDir := fields["dir"]
		bad := func(reason string) protocol.Message {
			return handlers.ErrorReply(req, contracts.NewError(protocol.CodeInvalidParams, "give exactly one absolute path or dir", map[string]any{"reason": reason}))
		}
		if hasPath && hasDir {
			return bad("path_and_dir")
		}
		var value string
		raw := path
		if hasDir {
			raw = dir
		}
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return bad("missing_path")
		}
		if !filepath.IsAbs(value) {
			return bad("relative_path")
		}
		source := contracts.PluginSource{Path: value}
		if hasDir {
			source = contracts.PluginSource{Dir: value}
		}
		res, err := h.Port.Add(ctx, c, source)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, res)
	case protocol.MethodPluginRemove:
		var p removeParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.plugin", p.Plugin); bad != nil {
			return *bad
		}
		res, err := h.Port.Remove(ctx, c, p.Plugin)
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
