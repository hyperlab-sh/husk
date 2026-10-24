package shim

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/adapters/herdr/registry"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func (r *runner) intercept(ctx context.Context, req proxy.Request) (any, *proxy.Error, bool) {
	var p struct {
		Plugin  string          `json:"plugin_id"`
		Action  string          `json:"action_id"`
		Pane    string          `json:"pane_id"`
		Title   string          `json:"title"`
		Body    string          `json:"body"`
		Context json.RawMessage `json:"context"`
	}
	switch req.Method {
	case "plugin.pane.open", "plugin.pane.focus", "plugin.pane.close", "popup.close", "notification.show", "plugin.action.invoke":
	default:
		return nil, nil, false
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return nil, &proxy.Error{Code: "invalid_params", Message: "invalid request params"}, true
	}
	switch req.Method {
	case "plugin.pane.open":
		if p.Plugin == r.m.ID {
			return nil, &proxy.Error{Code: "unsupported", Message: paneUnsupported}, true
		}
	case "plugin.pane.focus", "plugin.pane.close":
		if strings.HasPrefix(p.Pane, "husk:") {
			return nil, &proxy.Error{Code: "unsupported", Message: paneUnsupported}, true
		}
	case "popup.close":
		return nil, &proxy.Error{Code: "unsupported", Message: paneUnsupported}, true
	case "notification.show":
		trim := func(s string, n int) string {
			v := []rune(strings.TrimSpace(s))
			if len(v) > n {
				v = v[:n]
			}
			return string(v)
		}
		title, body := trim(p.Title, 80), trim(p.Body, 240)
		message := title
		if body != "" {
			if message != "" {
				message += "\n"
			}
			message += body
		}
		if err := r.o.Host.Notify(ctx, protocol.MethodToastShow, map[string]any{"message": message, "level": "info"}); err != nil {
			return nil, &proxy.Error{Code: "unavailable", Message: err.Error()}, true
		}
		return map[string]any{"type": "notification_show", "shown": true, "reason": "shown"}, nil, true
	case "plugin.action.invoke":
		plugin, action := p.Plugin, p.Action
		if plugin == "" {
			// Herdr accepts a qualified action id when plugin_id is omitted. Prefer
			// the longest registry prefix because plugin ids may themselves contain dots.
			candidates := []string{r.m.ID}
			records, _ := registry.Read(r.o.RegistryPath)
			for _, rec := range records {
				if rec.Enabled && manifest.Supports(rec.Platforms, r.o.Platform) {
					candidates = append(candidates, rec.ID)
				}
			}
			for _, id := range candidates {
				if strings.HasPrefix(action, id+".") && len(id) > len(plugin) {
					plugin = id
				}
			}
			if plugin != "" {
				action = strings.TrimPrefix(action, plugin+".")
			}
		}
		if !r.adapterPlugin(plugin) {
			return nil, nil, false
		}
		id, err := manifest.MapID(plugin)
		if err != nil {
			return nil, nil, false
		}
		result, err := r.o.Host.Call(ctx, protocol.MethodActionInvoke, map[string]any{"action": id + "." + strings.ReplaceAll(action, ":", "-"), "args": map[string]any{"context": p.Context}})
		if err != nil {
			var remote *protocol.Error
			if errors.As(err, &remote) {
				if remote.Code == protocol.CodeNotFound {
					return nil, nil, false
				}
				return nil, &proxy.Error{Code: string(remote.Code), Message: remote.Message}, true
			}
			return nil, &proxy.Error{Code: "unavailable", Message: err.Error()}, true
		}
		var started struct {
			Context json.RawMessage `json:"context"`
			Log     json.RawMessage `json:"command_log"`
			Action  json.RawMessage `json:"action_info"`
		}
		if json.Unmarshal(result, &started) != nil || started.Context == nil || started.Log == nil || started.Action == nil {
			return nil, &proxy.Error{Code: "failed", Message: "invalid adapter action reply"}, true
		}
		return map[string]any{"type": "plugin_action_invoked", "context": started.Context, "log": started.Log, "action": started.Action}, nil, true
	}
	return nil, nil, false
}
func (r *runner) adapterPlugin(id string) bool {
	if id == r.m.ID {
		return true
	}
	// Re-read for each routing decision. Atomic registry replacements and mtime
	// changes are observed without a watcher or a second catalog in the adapter.
	records, _ := registry.Read(r.o.RegistryPath)
	for _, rec := range records {
		if rec.ID == id && rec.Enabled && manifest.Supports(rec.Platforms, r.o.Platform) {
			return true
		}
	}
	return false
}
