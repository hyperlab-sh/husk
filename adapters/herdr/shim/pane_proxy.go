package shim

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func paneProxyError(err *protocol.Error) *proxy.Error {
	if err == nil {
		return nil
	}
	code := string(err.Code)
	if err.Code == protocol.CodeBusy {
		var data struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(err.Data, &data)
		switch data.Reason {
		case "modal_open":
			code = "ui_busy"
		case "too_many_surfaces", "too_many_commands":
			code = "limit_reached"
		}
	}
	return &proxy.Error{Code: code, Message: err.Message}
}

func (r *runner) interceptPane(ctx context.Context, req proxy.Request) (any, *proxy.Error, bool) {
	if req.Method == "plugin.pane.open" {
		var args paneArgs
		if err := json.Unmarshal(req.Params, &args); err != nil {
			return nil, &proxy.Error{Code: "invalid_params", Message: err.Error()}, true
		}
		if args.Plugin == r.m.ID {
			result, failure := r.openPane(ctx, strings.ReplaceAll(args.Entrypoint, ":", "-"), args, req.ID)
			return result, paneProxyError(failure), true
		}
		if !r.adapterPlugin(args.Plugin) {
			return nil, nil, false
		}
		id, err := manifest.MapID(args.Plugin)
		if err != nil {
			return nil, nil, false
		}
		// Forward all placement/context fields; the target shim owns admission and
		// translates the reply into the same schema-backed pane record.
		var params map[string]any
		_ = json.Unmarshal(req.Params, &params)
		raw, err := r.o.Host.Call(ctx, protocol.MethodActionInvoke, map[string]any{"action": id + ".pane-" + strings.ReplaceAll(args.Entrypoint, ":", "-"), "args": params})
		if err != nil {
			var failure *protocol.Error
			if errors.As(err, &failure) && failure.Code == protocol.CodeNotFound {
				return nil, nil, false
			}
			return nil, paneProxyError(wireFailure(err)), true
		}
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil {
			return nil, &proxy.Error{Code: "failed", Message: "invalid adapter pane reply"}, true
		}
		return result, nil, true
	}
	var params struct {
		Pane string `json:"pane_id"`
	}
	if json.Unmarshal(req.Params, &params) != nil {
		return nil, &proxy.Error{Code: "invalid_params", Message: "invalid pane params"}, true
	}
	r.mu.Lock()
	var p *pane
	if req.Method == "popup.close" {
		for _, candidate := range r.panes {
			if candidate.placement == "popup" {
				p = candidate
				break
			}
		}
	} else {
		p = r.panes[protocol.SurfaceID(strings.TrimPrefix(params.Pane, "husk:"))]
	}
	r.mu.Unlock()
	if req.Method == "popup.close" {
		if p == nil {
			return nil, nil, false
		}
		r.closePane(p)
		return map[string]any{"type": "ok"}, nil, true
	}
	if !strings.HasPrefix(params.Pane, "husk:") {
		return nil, nil, false
	}
	if p == nil {
		return nil, &proxy.Error{Code: "not_found", Message: "runtime pane not found"}, true
	}
	if req.Method == "plugin.pane.focus" {
		if _, err := r.o.Host.Call(ctx, protocol.MethodSurfaceFocus, protocol.SurfaceFocusParams{Surface: p.id}); err != nil {
			return nil, paneProxyError(wireFailure(err)), true
		}
		return p.result("plugin_pane_focused", r.m.ID), nil, true
	}
	r.closePane(p)
	return map[string]any{"type": "plugin_pane_closed", "pane_id": params.Pane}, nil, true
}
