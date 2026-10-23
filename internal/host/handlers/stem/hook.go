package stem

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Hook adapts contracts.HookPort for hook.call and hook.list. hook.call
// blocks until every subscriber has answered or timed out and replies once
// with the outcome; the shapes are the wire shapes of design/hooks.md.
type Hook struct {
	Port contracts.HookPort
}

type hookCallParams struct {
	Hook string          `json:"hook"`
	Data json.RawMessage `json:"data"`
}

// hookEntry is one subscriber's entry on the wire. Data and Items are
// omitted when absent; error is the protocol Error object the leg produced.
type hookEntry struct {
	Plugin string          `json:"plugin_id"`
	Data   json.RawMessage `json:"data,omitempty"`
	Items  json.RawMessage `json:"items,omitempty"`
	Veto   bool            `json:"veto,omitempty"`
	Reason *string         `json:"reason,omitempty"`
	Err    *protocol.Error `json:"error,omitempty"`
}

type hookVeto struct {
	Plugin string `json:"plugin_id"`
	Reason string `json:"reason"`
}

// hookCallResult is the outcome reply. Data is set (always) on a transform
// and Allowed/Vetoes (always) on a gate; the other kinds omit them.
type hookCallResult struct {
	Kind    string          `json:"kind"`
	Data    json.RawMessage `json:"data,omitempty"`
	Allowed *bool           `json:"allowed,omitempty"`
	Vetoes  *[]hookVeto     `json:"vetoes,omitempty"`
	Results []hookEntry     `json:"results"`
}

type hookSubscriberWire struct {
	Plugin   string `json:"plugin_id"`
	Priority int    `json:"priority"`
	Live     bool   `json:"live"`
}

type hookInfoWire struct {
	ID          string               `json:"id"`
	Owner       string               `json:"owner"`
	Kind        string               `json:"kind"`
	TimeoutMS   int64                `json:"timeout_ms"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	Subscribers []hookSubscriberWire `json:"subscribers"`
}

type hookListResult struct {
	Hooks []hookInfoWire `json:"hooks"`
}

// Serve implements contracts.Handler.
func (h Hook) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodHookCall:
		return h.call(ctx, c, req)
	case protocol.MethodHookList:
		return h.list(ctx, c, req)
	}
	return handlers.UnknownMethod(req)
}

func (h Hook) call(ctx context.Context, c contracts.Caller, req protocol.Message) protocol.Message {
	var p hookCallParams
	if bad := handlers.DecodeParams(req, &p); bad != nil {
		return *bad
	}
	if bad := handlers.RequireString(req, "params.hook", p.Hook); bad != nil {
		return *bad
	}
	data, bad := rawObject(req, "params.data", p.Data, false)
	if bad != nil {
		return *bad
	}
	o, err := h.Port.HookCall(ctx, c, p.Hook, data)
	if err != nil {
		return handlers.ErrorReply(req, err)
	}
	out := hookCallResult{Kind: o.Kind, Results: make([]hookEntry, 0, len(o.Results))}
	switch o.Kind {
	case "transform":
		out.Data = present(o.Data)
		if out.Data == nil {
			out.Data = json.RawMessage(`{}`)
		}
	case "gate":
		vetoes := make([]hookVeto, 0, len(o.Vetoes))
		for _, v := range o.Vetoes {
			vetoes = append(vetoes, hookVeto{Plugin: v.Plugin, Reason: v.Reason})
		}
		out.Allowed, out.Vetoes = &o.Allowed, &vetoes
	}
	for _, r := range o.Results {
		e := hookEntry{
			Plugin: r.Plugin, Data: present(r.Data), Items: present(r.Items),
			Veto: r.Veto, Err: hookWireError(r.Err),
		}
		if r.Veto {
			// A vetoing entry carries veto and reason together, even "".
			e.Reason = &r.Reason
		}
		out.Results = append(out.Results, e)
	}
	return handlers.Reply(req, out)
}

func (h Hook) list(ctx context.Context, c contracts.Caller, req protocol.Message) protocol.Message {
	infos, err := h.Port.HookList(ctx, c)
	if err != nil {
		return handlers.ErrorReply(req, err)
	}
	out := hookListResult{Hooks: make([]hookInfoWire, 0, len(infos))}
	for _, in := range infos {
		w := hookInfoWire{
			ID: in.ID, Owner: in.Owner, Kind: in.Kind, TimeoutMS: in.TimeoutMS,
			Title: in.Title, Description: in.Description,
			Subscribers: make([]hookSubscriberWire, 0, len(in.Subscribers)),
		}
		for _, s := range in.Subscribers {
			w.Subscribers = append(w.Subscribers, hookSubscriberWire{Plugin: s.Plugin, Priority: s.Priority, Live: s.Live})
		}
		out.Hooks = append(out.Hooks, w)
	}
	return handlers.Reply(req, out)
}

// present returns raw, or nil when it is empty or a JSON null, so an absent
// payload is omitted and "data":null is never emitted.
func present(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// hookWireError renders a leg's failure as the protocol Error it carried,
// data member included.
func hookWireError(err contracts.CodedError) *protocol.Error {
	if err == nil {
		return nil
	}
	e := &protocol.Error{Code: protocol.Code(err.Code()), Message: err.Error()}
	if !e.Code.Valid() {
		e.Code = protocol.CodeInternal
	}
	var withData contracts.DataError
	if errors.As(err, &withData) && withData.Data() != nil {
		if raw, merr := json.Marshal(withData.Data()); merr == nil {
			e.Data = raw
		}
	}
	return e
}
