// Package stem adapts Stem engines to their host ports. The adapters own no
// logic: they enforce the caller kinds of contracts.MethodHomes, derive
// plugin identity from the authenticated caller, and map engine refusals to
// contracts.CodedError.
package stem

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// SurfacePort adapts a surfaces.Engine as contracts.SurfacePort.
type SurfacePort struct {
	engine *surfaces.Engine
}

// NewSurfacePort wraps eng. The engine stays loop-only; the port is called
// from the Server's handlers on the loop.
func NewSurfacePort(eng *surfaces.Engine) *SurfacePort {
	return &SurfacePort{engine: eng}
}

// SurfaceUpdate serves surface.update (plugin callers only).
func (p *SurfacePort) SurfaceUpdate(ctx context.Context, c contracts.Caller, params protocol.SurfaceUpdateParams) error {
	if err := allowCaller(protocol.MethodSurfaceUpdate, c); err != nil {
		return err
	}
	return codedEngine(p.engine.SurfaceUpdate(c.PluginID, params))
}

// SurfaceClose serves surface.close (plugin callers only).
func (p *SurfacePort) SurfaceClose(ctx context.Context, c contracts.Caller, params protocol.SurfaceCloseParams) error {
	if err := allowCaller(protocol.MethodSurfaceClose, c); err != nil {
		return err
	}
	return codedEngine(p.engine.SurfaceClose(c.PluginID, params))
}

// SlotContribute serves slot.contribute (plugin callers only). Refusals are
// replies: the coded error becomes the request's error.
func (p *SurfacePort) SlotContribute(ctx context.Context, c contracts.Caller, params protocol.SlotContributeParams) error {
	if err := allowCaller(protocol.MethodSlotContribute, c); err != nil {
		return err
	}
	return codedEngine(p.engine.SlotContribute(c.PluginID, params))
}

// ToastShow serves toast.show from a plugin or the CLI. A CLI caller's empty
// PluginID attributes the toast to the host.
func (p *SurfacePort) ToastShow(ctx context.Context, c contracts.Caller, params protocol.ToastShowParams) error {
	if err := allowCaller(protocol.MethodToastShow, c); err != nil {
		return err
	}
	return codedEngine(p.engine.ToastShow(c.PluginID, params))
}

// allowCaller reports whether c's kind may call method, per the homes the
// consumer contracts declare. CallerStem is in-process and never arrives
// through a port.
func allowCaller(method string, c contracts.Caller) error {
	home, ok := contracts.MethodHomes()[method]
	if !ok {
		return contracts.NewError(protocol.CodeUnknownMethod, fmt.Sprintf("%s has no home", method), nil)
	}
	for _, kind := range home.Kinds {
		if kind == c.Kind {
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
