package stem

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
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
	post   Post
}

var _ contracts.HookPort = (*Hooks)(nil)

// NewHooks returns the hook.call port. Both arguments are required; post runs
// fn on the host loop and returns false when the loop no longer runs, in
// which case fn is not run.
func NewHooks(runner *hooks.Runner, post Post) *Hooks {
	if runner == nil || post == nil {
		panic("stem: NewHooks needs a Hook runner and a post")
	}
	return &Hooks{runner: runner, post: post}
}

// HookCall serves hook.call: one result per live subscriber once all are
// accounted for, in completion order (empty when none).
func (p *Hooks) HookCall(ctx context.Context, c contracts.Caller, hook string, data json.RawMessage) ([]contracts.HookResult, error) {
	d, ok := callerOf(c)
	if !ok {
		return nil, coded(protocol.CodeDenied, "unknown caller")
	}
	// The mapping rules stay callerOf's (Stem is a Host caller); the kind
	// itself is translated explicitly so nothing assumes the two enums'
	// layouts match.
	var caller hooks.Caller
	switch d.Kind {
	case dispatcher.CallerCLI:
		caller.Kind = hooks.CallerCLI
	case dispatcher.CallerHost:
		caller.Kind = hooks.CallerHost
	case dispatcher.CallerPlugin:
		caller.Kind = hooks.CallerPlugin
	default:
		return nil, coded(protocol.CodeDenied, "unknown caller")
	}
	caller.PluginID = d.PluginID
	payload, err := parseObject(data, "hook data")
	if err != nil {
		return nil, err
	}
	done := make(chan []hooks.HookResult, 1)
	failed := make(chan error, 1)
	if !p.post(func() {
		if err := p.runner.Call(caller, hook, payload, func(rs []hooks.HookResult) {
			// Non-blocking: when the caller left before the aggregate
			// arrived, no one receives it and it is dropped here.
			select {
			case done <- rs:
			default:
			}
		}); err != nil {
			failed <- err
		}
	}) {
		return nil, coded(protocol.CodeUnavailable, "host is shutting down")
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
		return nil, ctxError(ctx)
	}
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
