package host

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hookFirer fires host hooks as the host caller over the runner. It
// implements stem.HookFirer for handler goroutines; the runner itself is
// loop-confined, so Fire posts onto the loop and waits off it.
type hookFirer struct{ r *Root }

var _ stem.HookFirer = hookFirer{}

// Fire posts runner.Call onto the loop and waits for the outcome or ctx. A
// result that is already there wins over an ended ctx. When ctx ends first
// the legs drain on their own deadlines and the late outcome is dropped into
// the buffered channel. It must not be called on the loop.
func (f hookFirer) Fire(ctx context.Context, hook string, data map[string]any) (hooks.Outcome, error) {
	done := make(chan hooks.Outcome, 1)
	failed := make(chan error, 1)
	if !f.r.post(func() {
		err := f.r.runner.Call(hooks.Caller{Kind: hooks.CallerHost}, hook, data, func(o hooks.Outcome) { done <- o })
		if err != nil {
			failed <- err
		}
	}) {
		return hooks.Outcome{}, &hooks.HookError{Code: protocol.CodeUnavailable, Message: "the host loop no longer runs"}
	}
	select {
	case o := <-done:
		return o, nil
	case err := <-failed:
		return hooks.Outcome{}, err
	case <-ctx.Done():
		select {
		case o := <-done:
			return o, nil
		case err := <-failed:
			return hooks.Outcome{}, err
		default:
			return hooks.Outcome{}, &hooks.HookError{Code: protocol.CodeCancelled, Message: "request ended before the hook answered", Cause: ctx.Err()}
		}
	}
}
