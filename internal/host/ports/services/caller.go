package services

import (
	"context"
	"errors"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hostCallerKind spells the services Caller.Kind for each host caller kind.
// CLI maps to services' "cli"; the host's Stem engine is trusted like the
// host itself and maps to "host", the unfiltered view D34a gives it; a plugin
// keeps its exact id (case and dots included) as services' plugin identity.
func hostCallerKind(k host.CallerKind) string {
	switch k {
	case host.CallerCLI:
		return "cli"
	case host.CallerStem:
		return "host"
	}
	return "plugin"
}

// caller converts the Server's verified contracts.Caller. The Server, not the
// wire, produces these; conversion never invents identity.
func caller(cc host.Caller) services.Caller {
	return services.Caller{Kind: hostCallerKind(cc.Kind), PluginID: cc.PluginID}
}

// InputSource gathers the user-authorized Store input a dry rebuild needs;
// *services.Coordinator satisfies it (Coordinator.Inputs, user-only). The
// settings and theme wrappers use it for settings.validate and theme.check.
type InputSource interface {
	Inputs(ctx context.Context, caller services.Caller) (services.ReloadInput, error)
}

// codedError attaches data to a contracts-coded error for ErrorReply.
type codedError struct {
	code protocol.Code
	data map[string]any
	msg  string
}

func (e *codedError) Error() string { return e.msg }

// Code implements contracts.CodedError.
func (e *codedError) Code() string { return string(e.code) }

// Data implements contracts.DataError.
func (e *codedError) Data() map[string]any { return e.data }

var (
	_ host.CodedError = (*codedError)(nil)
	_ host.DataError  = (*codedError)(nil)
)

// coded converts a services error into a host-coded error. Services errors
// already carry value-free protocol codes and reasons (services.md, Errors),
// which survive verbatim; a reason lands in data.reason; context expiry maps
// to cancelled/timeout; anything else is internal with its message. Messages
// never contain secret values: services constructs them value-free.
func coded(err error) error {
	if err == nil {
		return nil
	}
	var serr *c.Error
	if errors.As(err, &serr) && serr != nil {
		code := protocol.Code(serr.Code)
		if !code.Valid() {
			return errors.New(serr.Message)
		}
		if serr.Message == "" {
			serr.Message = serr.Code + ": " + serr.Reason
		}
		e := &codedError{code: code, msg: serr.Message}
		if serr.Reason != "" {
			e.data = map[string]any{"reason": serr.Reason}
		}
		return e
	}
	if errors.Is(err, context.Canceled) {
		return &codedError{code: protocol.CodeCancelled, msg: "cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &codedError{code: protocol.CodeTimeout, data: map[string]any{"reason": "input_deadline"}, msg: "input deadline exceeded"}
	}
	return err
}
