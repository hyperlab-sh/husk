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
	if req.Method == "popup.close" {
		r.mu.Lock()
		var modal *pane
		for _, p := range r.panes {
			if p.placement == "popup" {
				modal = p
				break
			}
		}
		r.mu.Unlock()
		if modal == nil {
			return nil, nil, false
		}
		r.closePane(modal)
		return map[string]any{"type": "ok"}, nil, true
	}
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
