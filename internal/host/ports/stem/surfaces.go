package stem

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// SurfacePort adapts a surfaces.Engine as contracts.SurfacePort. The engine
// is loop-confined and the Server calls this port from reader goroutines, so
// every engine call crosses the Loop seam with Do and carries the engine's
// reply back out with it.
type SurfacePort struct {
	engine *surfaces.Engine
	loop   Loop
}

var _ contracts.SurfacePort = (*SurfacePort)(nil)

// NewSurfacePort wraps eng for the loop. Both arguments are required.
func NewSurfacePort(eng *surfaces.Engine, loop Loop) *SurfacePort {
	if eng == nil || loop == nil {
		panic("stem: NewSurfacePort needs an Engine and a Loop")
	}
	return &SurfacePort{engine: eng, loop: loop}
}

// run runs fn, an engine call, on the loop and carries its error back. A Do
// that cannot run fn — the request context ended or the loop shut down —
// answers unavailable; the engine call never ran in that case.
func (p *SurfacePort) run(ctx context.Context, fn func() error) error {
	var err error
	if lerr := p.loop.Do(ctx, func() { err = fn() }); lerr != nil {
		return contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil)
	}
	return codedEngine(err)
}

// SurfaceUpdate serves surface.update (plugin callers only).
func (p *SurfacePort) SurfaceUpdate(ctx context.Context, c contracts.Caller, params protocol.SurfaceUpdateParams) error {
	if err := allowCaller(protocol.MethodSurfaceUpdate, c); err != nil {
		return err
	}
	return p.run(ctx, func() error { return p.engine.SurfaceUpdate(c.PluginID, params) })
}

// SurfaceClose serves surface.close (plugin callers only).
func (p *SurfacePort) SurfaceClose(ctx context.Context, c contracts.Caller, params protocol.SurfaceCloseParams) error {
	if err := allowCaller(protocol.MethodSurfaceClose, c); err != nil {
		return err
	}
	return p.run(ctx, func() error { return p.engine.SurfaceClose(c.PluginID, params) })
}

// SlotContribute serves slot.contribute (plugin callers only). Refusals are
// replies: the coded error becomes the request's error.
func (p *SurfacePort) SlotContribute(ctx context.Context, c contracts.Caller, params protocol.SlotContributeParams) error {
	if err := allowCaller(protocol.MethodSlotContribute, c); err != nil {
		return err
	}
	return p.run(ctx, func() error { return p.engine.SlotContribute(c.PluginID, params) })
}

// ToastShow serves toast.show from a plugin or the CLI. A CLI caller's empty
// PluginID attributes the toast to the host.
func (p *SurfacePort) ToastShow(ctx context.Context, c contracts.Caller, params protocol.ToastShowParams) error {
	if err := allowCaller(protocol.MethodToastShow, c); err != nil {
		return err
	}
	return p.run(ctx, func() error { return p.engine.ToastShow(c.PluginID, params) })
}

// allowCaller reports whether c's kind may call method, per the homes the
// consumer contracts declare, and whether a plugin-kind caller is
// authenticated: a plugin caller with an empty PluginID has no identity to
// authorize, like the Calls port's callerOf. CallerStem is in-process and
// never arrives through a port.
func allowCaller(method string, c contracts.Caller) error {
	home, ok := contracts.MethodHomes()[method]
	if !ok {
		return contracts.NewError(protocol.CodeUnknownMethod, fmt.Sprintf("%s has no home", method), nil)
	}
	for _, kind := range home.Kinds {
		if kind == c.Kind {
			if c.Kind == contracts.CallerPlugin && c.PluginID == "" {
				return contracts.NewError(protocol.CodeDenied, fmt.Sprintf("%s requires an authenticated plugin caller", method), nil)
			}
			return nil
		}
	}
	return contracts.NewError(protocol.CodeDenied, fmt.Sprintf("%s does not accept %s callers", method, kindName(c.Kind)), nil)
}

func kindName(kind contracts.CallerKind) string {
	switch kind {
	case contracts.CallerCLI:
		return "CLI"
	case contracts.CallerPlugin:
		return "plugin"
	case contracts.CallerStem:
		return "Stem"
	default:
		return "unknown"
	}
}

// codedEngine converts an engine refusal to a CodedError, passing its wire
// code through unchanged (the package's coded builds codes directly).
func codedEngine(err error) error {
	if err == nil {
		return nil
	}
	var serr *surfaces.Error
	if errors.As(err, &serr) {
		return contracts.NewError(serr.Code, serr.Message, serr.Data)
	}
	return contracts.NewError(protocol.CodeUnavailable, err.Error(), nil)
}
