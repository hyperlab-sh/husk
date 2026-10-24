// Package shim runs one converted plugin over Husk stdio and the upstream API.
package shim

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// InvocationContext follows PluginInvocationContext in the committed schema.
// Unknown fields, including Husk focus and caller credentials, never enter it.
// ClickedURL and LinkHandlerID preserve caller-supplied schema fields in context
// JSON only; they do not enable handlers or export link-handler environment.
type InvocationContext struct {
	WorkspaceID       string          `json:"workspace_id,omitempty"`
	WorkspaceLabel    string          `json:"workspace_label,omitempty"`
	WorkspaceCwd      string          `json:"workspace_cwd,omitempty"`
	Worktree          json.RawMessage `json:"worktree,omitempty"`
	TabID             string          `json:"tab_id,omitempty"`
	TabLabel          string          `json:"tab_label,omitempty"`
	FocusedPaneID     string          `json:"focused_pane_id,omitempty"`
	FocusedPaneCwd    string          `json:"focused_pane_cwd,omitempty"`
	FocusedPaneAgent  string          `json:"focused_pane_agent,omitempty"`
	FocusedPaneStatus string          `json:"focused_pane_status,omitempty"`
	InvocationSource  string          `json:"invocation_source,omitempty"`
	CorrelationID     string          `json:"correlation_id,omitempty"`
	ClickedURL        string          `json:"clicked_url,omitempty"`
	LinkHandlerID     string          `json:"link_handler_id,omitempty"`
	SelectedText      *string         `json:"selected_text,omitempty"`
}

// contextFailure gives action and pane context reads the same absence contract,
// including a server lost after the successful pre-spawn ping.
func contextFailure(err error) *protocol.Error {
	var remote *upstream.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &remote) && remote.Code == "unavailable" {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error(), Data: json.RawMessage(`{"reason":"herdr_absent"}`)}
	}
	return wireFailure(err)
}

// BuildContext reads server focus, not Husk focus. Non-null caller-supplied
// schema fields override the active context just as merge_plugin_context does.
// The generic source "api" is pinned upstream context.rs:374 and :405
// at revision 5608de0608918aba2f9d501e7793b984a0ca9cfa.
func BuildContext(ctx context.Context, client *upstream.Client, correlation, source string, provided json.RawMessage) (InvocationContext, error) {
	c := InvocationContext{InvocationSource: source, CorrelationID: correlation}
	raw, err := client.Call(ctx, "pane.current", map[string]any{})
	if err != nil {
		return c, err
	}
	var p struct {
		Pane struct {
			ID        string `json:"pane_id"`
			Workspace string `json:"workspace_id"`
			Tab       string `json:"tab_id"`
			Cwd       string `json:"cwd"`
			Agent     string `json:"agent"`
			Status    string `json:"agent_status"`
		} `json:"pane"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return c, err
	}
	c.WorkspaceID, c.TabID, c.FocusedPaneID = p.Pane.Workspace, p.Pane.Tab, p.Pane.ID
	c.FocusedPaneCwd, c.FocusedPaneAgent, c.FocusedPaneStatus = p.Pane.Cwd, p.Pane.Agent, p.Pane.Status
	if c.WorkspaceID != "" {
		raw, err = client.Call(ctx, "workspace.get", map[string]any{"workspace_id": c.WorkspaceID})
		if err != nil {
			return c, err
		}
		var w struct {
			Workspace struct {
				Label    string          `json:"label"`
				Worktree json.RawMessage `json:"worktree"`
			} `json:"workspace"`
		}
		if err = json.Unmarshal(raw, &w); err != nil {
			return c, err
		}
		c.WorkspaceLabel = w.Workspace.Label
		if string(w.Workspace.Worktree) != "null" {
			c.Worktree = w.Workspace.Worktree
		}
		c.WorkspaceCwd = c.FocusedPaneCwd
		if c.WorkspaceCwd == "" && len(c.Worktree) > 0 {
			var wt struct {
				Checkout string `json:"checkout_path"`
			}
			_ = json.Unmarshal(c.Worktree, &wt)
			c.WorkspaceCwd = wt.Checkout
		}
	}
	if c.TabID != "" {
		raw, err = client.Call(ctx, "tab.get", map[string]any{"tab_id": c.TabID})
		if err != nil {
			return c, err
		}
		var tab struct {
			Tab struct {
				Label string `json:"label"`
			} `json:"tab"`
		}
		if err = json.Unmarshal(raw, &tab); err != nil {
			return c, err
		}
		c.TabLabel = tab.Tab.Label
	}
	if len(provided) > 0 && string(provided) != "null" {
		// Decode into the schema struct first, so additional caller data is ignored.
		var override InvocationContext
		if err = json.Unmarshal(provided, &override); err != nil {
			return c, err
		}
		base, _ := json.Marshal(c)
		fields := map[string]json.RawMessage{}
		_ = json.Unmarshal(base, &fields)
		incoming := map[string]json.RawMessage{}
		_ = json.Unmarshal(provided, &incoming)
		schemaFields := map[string]bool{}
		typ := reflect.TypeFor[InvocationContext]()
		for i := 0; i < typ.NumField(); i++ {
			schemaFields[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		for k, v := range incoming {
			if schemaFields[k] && string(v) != "null" {
				fields[k] = v
			}
		}
		merged, _ := json.Marshal(fields)
		err = json.Unmarshal(merged, &c)
	}
	return c, err
}
