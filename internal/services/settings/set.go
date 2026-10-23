package settings

import (
	"context"
	"strings"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// StoreWriter is the sole configuration write seam; it runs off the loop.
type StoreWriter interface {
	// Patch atomically merges a single-key user-layer configuration patch.
	Patch(store.DocName, store.Layer, string, store.Doc) error
}

// Waiter is a registration on the flight that observes a completed write.
type Waiter interface {
	// Wait awaits the captured flight off the loop without cancelling it.
	Wait(context.Context) ([]c.Report, error)
}

// Reloader registers writes on the loop using the coordinator's coalescing rule.
type Reloader interface {
	// AwaitSettings registers a write after persistence; loop-only.
	AwaitSettings() Waiter
}

// Bind connects the coordinator once, before Set can be called.
func (s *Service) Bind(r Reloader) { s.reloader = r }

// Set writes one validated value to the user layer off the loop. Only its
// waiter registration runs on the loop; snapshots change through reload alone.
// The returned source is the serving snapshot's source after that flight.
func (s *Service) Set(ctx context.Context, caller c.Caller, key string, value any) (string, []c.Report, error) {
	if caller.Kind == protocol.CallerPlugin {
		return "", nil, c.Failure("denied", "user_only")
	}
	if !c.ValidCaller(caller) || caller.Kind != protocol.CallerCLI && caller.Kind != protocol.CallerHost {
		return "", nil, c.Failure("denied", "invalid_caller")
	}
	entry, ok := s.current.Load().schema[key]
	if !ok {
		return "", nil, c.Failure("not_found", "unknown_setting")
	}
	// Typed nil slices from in-process callers are JSON null too.
	value = c.Clone(value)
	if value == nil {
		return "", nil, c.Failure("invalid_params", "null_value")
	}
	if _, err := validate(entry, value); err != nil {
		return "", nil, &c.Error{Code: "invalid_params", Reason: "invalid_value", Key: key, Message: key + ": " + err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if s.opts.Store == nil || s.reloader == nil {
		return "", nil, c.Failure("unavailable", "store_error")
	}
	var patch any = c.Clone(value)
	parts := strings.Split(key, ".")
	for i := len(parts) - 1; i >= 0; i-- {
		patch = store.Doc{parts[i]: patch}
	}
	if err := s.opts.Store.Patch("config", store.LayerUser, "", patch.(store.Doc)); err != nil {
		return "", nil, &c.Error{Code: "unavailable", Reason: "store_error", Message: err.Error()}
	}
	registered := make(chan Waiter, 1)
	if s.opts.Post == nil || !s.opts.Post(func() { registered <- s.reloader.AwaitSettings() }) {
		return "", nil, c.Failure("cancelled", "shutdown")
	}
	select {
	case waiter := <-registered:
		reports, err := waiter.Wait(ctx)
		if err != nil {
			return "", reports, err
		}
		return s.Snapshot().Source(key), reports, nil
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}
