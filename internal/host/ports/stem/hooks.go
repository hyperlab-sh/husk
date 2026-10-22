package stem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Hooks implements contracts.HookPort over the Stem Hook runner. The runner
// is loop-confined, so the fan-out posts onto the loop through post and the
// port waits for the aggregate off the loop, or until ctx ends. An ended ctx
// abandons the wait only: the legs stay pending in Calls until their own
// deadlines and their replies are still consumed.
type Hooks struct {
	runner *hooks.Runner
	post   func(fn func()) bool
}

var _ contracts.HookPort = (*Hooks)(nil)

// NewHooks returns the hook.call port. Both arguments are required; post runs
// fn on the host loop and returns false when the loop no longer runs, in
// which case fn is not run.
func NewHooks(runner *hooks.Runner, post func(fn func()) bool) *Hooks {
	if runner == nil || post == nil {
		panic("stem: NewHooks needs a Hook runner and a post")
	}
	return &Hooks{runner: runner, post: post}
}

// HookCall serves hook.call: one result per live subscriber once all are
// accounted for, in completion order (empty when none).
func (p *Hooks) HookCall(ctx context.Context, c contracts.Caller, hook string, data json.RawMessage) ([]contracts.HookResult, error) {
	caller, ok := hookCallerOf(c)
	if !ok {
		return nil, contracts.NewError(protocol.CodeDenied, "unknown caller", nil)
	}
	payload, err := parseHookData(data)
	if err != nil {
		return nil, err
	}
	done := make(chan []hooks.HookResult, 1)
	failed := make(chan error, 1)
	if !p.post(func() {
		if err := p.runner.Call(caller, hook, payload, func(rs []hooks.HookResult) {
			// Non-blocking: the buffered slot is free unless the caller left
			// and this continuation was already dropped.
			select {
			case done <- rs:
			default:
			}
		}); err != nil {
			failed <- err
		}
	}) {
		return nil, contracts.NewError(protocol.CodeUnavailable, "host is shutting down", nil)
	}
	select {
	case rs := <-done:
		out := make([]contracts.HookResult, len(rs))
		for i, r := range rs {
			out[i] = hookResultOf(r)
		}
		return out, nil
	case err := <-failed:
		return nil, hookErrorOf(err)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, contracts.NewError(protocol.CodeTimeout, "caller deadline passed", nil)
		}
		return nil, contracts.NewError(protocol.CodeCancelled, "caller went away", nil)
	}
}

// hookCallerOf maps the port's Caller to the runner's. CallerStem is a Host
// caller, as in the Calls port: only Input keeps its own focus snapshot.
func hookCallerOf(c contracts.Caller) (hooks.Caller, bool) {
	switch c.Kind {
	case contracts.CallerCLI:
		return hooks.Caller{Kind: hooks.CallerCLI}, true
	case contracts.CallerStem:
		return hooks.Caller{Kind: hooks.CallerHost}, true
	case contracts.CallerPlugin:
		if c.PluginID != "" {
			return hooks.Caller{Kind: hooks.CallerPlugin, PluginID: c.PluginID}, true
		}
	}
	return hooks.Caller{}, false
}

// parseHookData decodes the hook data member as a JSON object. Absent, empty
// and null are nil.
func parseHookData(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, contracts.NewError(protocol.CodeInvalidParams, "hook data must be a JSON object", nil)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, contracts.NewError(protocol.CodeInvalidParams, "hook data must be a JSON object", nil)
	}
	return m, nil
}

// hookResultOf converts one aggregate entry, marshalling the payload back to
// wire form.
func hookResultOf(r hooks.HookResult) contracts.HookResult {
	out := contracts.HookResult{Plugin: r.Plugin}
	if r.Data != nil {
		if b, err := json.Marshal(r.Data); err == nil {
			out.Data = b
		}
	}
	if r.Err != nil {
		out.Err = contracts.NewError(protocol.Code(r.Err.Code), r.Err.Message, r.Err.Data)
	}
	return out
}

// hookErrorOf carries the runner's coded refusals across the port boundary;
// an uncoded error becomes internal.
func hookErrorOf(err error) error {
	var he *hooks.HookError
	if errors.As(err, &he) {
		return contracts.NewError(protocol.Code(he.Code), he.Message, he.Data)
	}
	return contracts.NewError(protocol.CodeInternal, err.Error(), nil)
}
