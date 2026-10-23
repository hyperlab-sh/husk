package stem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/hooks"
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

// HookCall serves hook.call from the hook's owner: the outcome once all
// legs are accounted for. The caller is passed to the runner, which decides
// ownership; the adapter has already admitted plugin callers only. params.data
// over 1 MiB (compact JSON, measured on the raw bytes) is invalid_params and
// nothing is posted.
func (p *Hooks) HookCall(ctx context.Context, c contracts.Caller, hook string, data json.RawMessage) (contracts.HookOutcome, error) {
	if !validCaller(c) {
		return contracts.HookOutcome{}, coded(protocol.CodeDenied, "unknown caller")
	}
	if n := compactLen(data); n > hooks.MaxPayload {
		return contracts.HookOutcome{}, coded(protocol.CodeInvalidParams,
			fmt.Sprintf("hook data is %d bytes, over the %d byte limit", n, hooks.MaxPayload))
	}
	payload, err := parseObject(data, "hook data")
	if err != nil {
		return contracts.HookOutcome{}, err
	}
	done := make(chan hooks.Outcome, 1)
	failed := make(chan error, 1)
	if !p.post(func() {
		if err := p.runner.Call(c, hook, payload, func(o hooks.Outcome) {
			// Non-blocking: when the caller left before the aggregate
			// arrived, no one receives it and it is dropped here.
			select {
			case done <- o:
			default:
			}
		}); err != nil {
			failed <- err
		}
	}) {
		return contracts.HookOutcome{}, coded(protocol.CodeUnavailable, "host is shutting down")
	}
	var o hooks.Outcome
	select {
	case o = <-done:
	case err := <-failed:
		return contracts.HookOutcome{}, hookErrorOf(err)
	case <-ctx.Done():
		// A result that is already there wins over an ended ctx.
		select {
		case o = <-done:
		case err := <-failed:
			return contracts.HookOutcome{}, hookErrorOf(err)
		default:
			return contracts.HookOutcome{}, ctxError(ctx)
		}
	}
	return hookOutcomeOf(o), nil
}

// HookList serves hook.list: the runner's declared hooks with their
// subscribers and liveness, read on the loop.
func (p *Hooks) HookList(ctx context.Context, _ contracts.Caller) ([]contracts.HookInfo, error) {
	done := make(chan []hooks.Listed, 1)
	if !p.post(func() { done <- p.runner.List() }) {
		return nil, coded(protocol.CodeUnavailable, "host is shutting down")
	}
	listed, ok := await(ctx, done)
	if !ok {
		return nil, ctxError(ctx)
	}
	out := make([]contracts.HookInfo, len(listed))
	for i, l := range listed {
		info := contracts.HookInfo{
			ID: l.ID, Owner: l.Owner, Kind: string(l.Kind), TimeoutMS: l.Timeout.Milliseconds(),
			Title: l.Title, Description: l.Description,
			Subscribers: make([]contracts.HookSubscriber, len(l.Subscribers)),
		}
		for j, s := range l.Subscribers {
			info.Subscribers[j] = contracts.HookSubscriber{Plugin: s.Plugin, Priority: s.Priority, Live: s.Live}
		}
		out[i] = info
	}
	return out, nil
}

// compactLen is the byte length of raw's compact JSON encoding; raw that is
// not valid JSON measures as 0 and is rejected later by parseObject.
func compactLen(raw json.RawMessage) int {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return 0
	}
	return buf.Len()
}

// hookOutcomeOf converts the runner's outcome, carrying kind, the transform
// value, the gate verdict and every entry's fields.
func hookOutcomeOf(o hooks.Outcome) contracts.HookOutcome {
	out := contracts.HookOutcome{
		Kind:    string(o.Kind),
		Data:    marshalPresent(o.Data, o.Data != nil),
		Allowed: o.Allowed,
		Results: make([]contracts.HookResult, len(o.Results)),
	}
	for _, v := range o.Vetoes {
		out.Vetoes = append(out.Vetoes, contracts.HookVeto{Plugin: v.Plugin, Reason: v.Reason})
	}
	for i, r := range o.Results {
		out.Results[i] = hookResultOf(r)
	}
	return out
}

// hookResultOf converts one entry. A nil Data map is a pass-through (no
// data); an empty one is a replacement, "{}".
func hookResultOf(r hooks.HookResult) contracts.HookResult {
	out := contracts.HookResult{
		Plugin: r.Plugin,
		Data:   marshalPresent(r.Data, r.Data != nil),
		Items:  marshalPresent(r.Items, r.Items != nil),
		Veto:   r.Veto,
		Reason: r.Reason,
	}
	if r.Err != nil {
		out.Err = contracts.NewError(r.Err.Code, r.Err.Message, r.Err.Data)
	}
	return out
}

// marshalPresent encodes v when present is true, else returns nil.
func marshalPresent(v any, present bool) json.RawMessage {
	if !present {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// hookErrorOf carries the runner's coded refusals across the port boundary;
// an uncoded error becomes internal.
func hookErrorOf(err error) error {
	var he *hooks.HookError
	if errors.As(err, &he) {
		return contracts.NewError(he.Code, he.Message, he.Data)
	}
	return contracts.NewError(protocol.CodeInternal, err.Error(), nil)
}
