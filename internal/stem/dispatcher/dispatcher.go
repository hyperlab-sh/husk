package dispatcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
)

// Sender is the part of *calls.Table the Dispatcher uses.
type Sender interface {
	Send(plugin calls.PluginID, method string, params json.RawMessage, opts calls.Options) (calls.Handle, error)
}

// Options wires a Dispatcher. Catalog, Liveness, Focus and Calls are required.
type Options struct {
	Catalog  Catalog
	Liveness Liveness
	Focus    FocusSource
	Calls    Sender
	// Timeout is the action timeout handed to Calls; zero selects
	// calls.DefaultTimeout (10 s).
	Timeout time.Duration
	// Logger receives info-level lines (denied requests). It must not block.
	Logger func(format string, args ...any)
}

// Dispatcher routes actions. It is confined to the loop goroutine.
type Dispatcher struct {
	opts    Options
	host    map[string]func(ActionRequest) ActionReply
	pending map[calls.Handle]func(ActionReply)
}

// New returns a Dispatcher. It panics when a required view is nil.
func New(opts Options) *Dispatcher {
	if opts.Catalog == nil || opts.Liveness == nil || opts.Focus == nil || opts.Calls == nil {
		panic("dispatcher: Catalog, Liveness, Focus and Calls are required")
	}
	return &Dispatcher{
		opts:    opts,
		host:    make(map[string]func(ActionRequest) ActionReply),
		pending: make(map[calls.Handle]func(ActionReply)),
	}
}

// RegisterHost binds an in-process handler for a husk.* action id or a rail
// item id. Host handlers run synchronously on the loop. A later registration
// replaces an earlier one; a nil handler removes the binding.
func (d *Dispatcher) RegisterHost(action string, h func(ActionRequest) ActionReply) {
	if h == nil {
		delete(d.host, action)
		return
	}
	d.host[action] = h
}

// Pending reports how many requests wait on an owner.
func (d *Dispatcher) Pending() int { return len(d.pending) }

func fail(code, msg string) ActionReply {
	return ActionReply{Err: &ActionError{Code: code, Message: msg}}
}

// Invoke never blocks. Reply is called exactly once, possibly before Invoke
// returns (immediate failures and host actions).
func (d *Dispatcher) Invoke(req ActionRequest) {
	done := false
	reply := func(r ActionReply) {
		if done {
			return
		}
		done = true
		if req.Reply != nil {
			req.Reply(r)
		}
	}

	// 1. Well-formed id, object args within the size limit.
	if !validActionID(req.Action) {
		reply(fail(CodeInvalidParams, "malformed action id"))
		return
	}
	var args json.RawMessage
	if req.Args != nil {
		b, err := json.Marshal(req.Args)
		if err != nil {
			reply(fail(CodeInvalidParams, "args are not a JSON object"))
			return
		}
		if len(b) > MaxArgsBytes {
			reply(fail(CodeInvalidParams, "args exceed 1 MiB"))
			return
		}
		args = b
	}

	// 2. Catalog.
	def, ok := d.opts.Catalog.Lookup(req.Action)
	if !ok {
		reply(fail(CodeNotFound, "no action "+quote(req.Action)))
		return
	}

	// 3. Permission, before liveness so a denied caller learns nothing about
	// whether the owner runs.
	if !Permitted(req.Caller, def) {
		d.logf("dispatcher: %s %q denied invoking %q", req.Caller.Kind, req.Caller.PluginID, req.Action)
		reply(fail(CodeDenied, "not allowed to invoke "+quote(req.Action)))
		return
	}

	// 4. Host actions.
	if def.Owner == HostOwner {
		h := d.host[req.Action]
		if h == nil {
			reply(fail(CodeNotFound, "no handler for "+quote(req.Action)))
			return
		}
		if req.Caller.Kind != CallerInput {
			req.Focus = d.opts.Focus.Focus()
		}
		reply(h(req))
		return
	}

	// 5. Liveness.
	if !d.opts.Liveness.Live(def.Owner) {
		reply(fail(CodeUnavailable, "plugin "+quote(def.Owner)+" is not running"))
		return
	}

	// 6. Focus: only Input keeps its own snapshot.
	focus := req.Focus
	if req.Caller.Kind != CallerInput {
		focus = d.opts.Focus.Focus()
	}

	// 7. Send through Calls.
	params, err := json.Marshal(wireParams{
		Action: req.Action,
		Args:   args,
		Caller: wireCaller{Kind: req.Caller.Kind.String(), PluginID: req.Caller.PluginID},
		Focus:  focus,
	})
	if err != nil {
		reply(fail(CodeInternal, "cannot encode request"))
		return
	}
	h, err := d.opts.Calls.Send(calls.PluginID(def.Owner), "action.invoke", params,
		calls.Options{Timeout: d.opts.Timeout, Requester: Requester})
	if err != nil {
		reply(sendFailure(err))
		return
	}
	d.pending[h] = reply
}

type wireCaller struct {
	Kind     string `json:"kind"`
	PluginID string `json:"plugin_id,omitempty"`
}

type wireParams struct {
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args,omitempty"`
	Caller wireCaller      `json:"caller"`
	Focus  FocusContext    `json:"focus"`
}

func sendFailure(err error) ActionReply {
	switch {
	case errors.Is(err, calls.ErrBusy), errors.Is(err, calls.ErrPluginBusy):
		return fail(CodeBusy, "too many pending calls")
	case errors.Is(err, calls.ErrUnavailable), errors.Is(err, calls.ErrClosed):
		return fail(CodeUnavailable, "plugin is not reachable")
	}
	return fail(CodeInternal, "cannot send request")
}

// Complete resolves pending requests with Calls results (from Resolve, Expire,
// DropPlugin, Sweep or Close). Results of other requesters and unknown handles
// are ignored. It returns how many replies it delivered. Loop only.
func (d *Dispatcher) Complete(results ...calls.Result) int {
	n := 0
	for _, res := range results {
		reply, ok := d.pending[res.Handle]
		if !ok || res.Requester != Requester {
			continue
		}
		delete(d.pending, res.Handle)
		reply(convert(res))
		n++
	}
	return n
}

func convert(res calls.Result) ActionReply {
	if res.Err != nil {
		e := &ActionError{Code: string(res.Err.Code), Message: res.Err.Message}
		if len(res.Err.Data) > 0 {
			var data map[string]any
			if json.Unmarshal(res.Err.Data, &data) == nil {
				e.Data = data
			}
		}
		return ActionReply{Err: e}
	}
	if len(res.Value) == 0 || bytes.Equal(bytes.TrimSpace(res.Value), []byte("null")) {
		return ActionReply{Result: map[string]any{}}
	}
	dec := json.NewDecoder(bytes.NewReader(res.Value))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return fail(CodeInternal, "owner returned a result that is not an object")
	}
	return ActionReply{Result: out}
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.opts.Logger != nil {
		d.opts.Logger(format, args...)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Listed is one catalog entry as a caller sees it, with owner liveness.
type Listed struct {
	ActionDef
	// Live is true for host actions and for actions whose owner is live.
	Live bool
}

// List returns the catalog entries caller may invoke (the same Permitted rule
// Invoke applies), sorted by id. Input, CLI and Host callers see every entry;
// a plugin sees its own actions and the public ones. Loop only.
func (d *Dispatcher) List(caller Caller) []Listed {
	defs := d.opts.Catalog.List()
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	out := make([]Listed, 0, len(defs))
	for _, def := range defs {
		if !Permitted(caller, def) {
			continue
		}
		out = append(out, Listed{ActionDef: def, Live: def.Owner == HostOwner || d.opts.Liveness.Live(def.Owner)})
	}
	return out
}
