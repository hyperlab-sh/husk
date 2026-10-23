package host

import (
	"context"
	"sync/atomic"

	"github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hookFirer fires host hooks as the host caller over the runner. It
// implements stem.HookFirer for handler goroutines; the runner itself is
// loop-confined, so Fire posts onto the loop and waits off it.
type hookFirer struct{ r *Root }

var _ stem.HookFirer = hookFirer{}

// Fire posts runner.Call onto the loop and waits for the outcome, ctx or the
// host's own shutdown. A ctx that is already done returns cancelled before
// anything is posted, and a posted closure that runs after the waiter left
// skips runner.Call, so no subscriber is asked on behalf of a request that
// was already answered. A result that is already there wins over an ended
// ctx. When ctx ends after the legs went out they drain on their own
// deadlines and the late outcome falls into the buffered channel. It must
// not be called on the loop.
func (f hookFirer) Fire(ctx context.Context, hook string, data map[string]any) (hooks.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return hooks.Outcome{}, cancelledWait(err)
	}
	const (
		pending = iota
		running
		abandoned
	)
	var claim atomic.Int32
	done := make(chan hooks.Outcome, 1)
	failed := make(chan error, 1)
	if !f.r.post(func() {
		if !claim.CompareAndSwap(pending, running) {
			return
		}
		err := f.r.runner.Call(hooks.Caller{Kind: hooks.CallerHost}, hook, data, func(o hooks.Outcome) { done <- o })
		if err != nil {
			failed <- err
		}
	}) {
		return hooks.Outcome{}, loopGone()
	}
	ready := func() (hooks.Outcome, error, bool) {
		select {
		case o := <-done:
			return o, nil, true
		case err := <-failed:
			return hooks.Outcome{}, err, true
		default:
			return hooks.Outcome{}, nil, false
		}
	}
	select {
	case o := <-done:
		return o, nil
	case err := <-failed:
		return hooks.Outcome{}, err
	case <-ctx.Done():
		if claim.CompareAndSwap(pending, abandoned) {
			return hooks.Outcome{}, cancelledWait(ctx.Err())
		}
		if o, err, ok := ready(); ok {
			return o, err
		}
		return hooks.Outcome{}, cancelledWait(ctx.Err())
	case <-f.r.ctx.Done():
		if claim.CompareAndSwap(pending, abandoned) {
			return hooks.Outcome{}, loopGone()
		}
		if o, err, ok := ready(); ok {
			return o, err
		}
		return hooks.Outcome{}, loopGone()
	}
}

func cancelledWait(cause error) error {
	return &hooks.HookError{Code: protocol.CodeCancelled, Message: "request ended before the hook answered", Cause: cause}
}

func loopGone() error {
	return &hooks.HookError{Code: protocol.CodeUnavailable, Message: "the host loop no longer runs"}
}
