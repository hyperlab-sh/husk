package services

import (
	"context"
	"errors"

	host "github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	services "github.com/hyperlab-sh/husk/pkg/services"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
)

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
// to cancelled/timeout — the plain timeout, because a raw deadline here is
// the caller's context expiring, not the input load (codedInput spells that
// one); anything else is internal with its message. Messages never contain
// secret values: services constructs them value-free, and this function
// never mutates the services error it inspects.
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
		msg := serr.Message
		if msg == "" {
			msg = serr.Code + ": " + serr.Reason
		}
		e := &codedError{code: code, msg: msg}
		if serr.Reason != "" {
			e.data = map[string]any{"reason": serr.Reason}
		}
		if serr.Key != "" {
			if e.data == nil {
				e.data = map[string]any{}
			}
			e.data["key"] = serr.Key
		}
		return e
	}
	if errors.Is(err, context.Canceled) {
		return &codedError{code: protocol.CodeCancelled, msg: "cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &codedError{code: protocol.CodeTimeout, msg: "timeout"}
	}
	return err
}

// codedInput is coded for the input-gathering calls (settings.validate,
// theme.check): a deadline there is the input load timing out, which the
// protocol spells timeout with reason input_deadline. The services input
// timer already returns that as a services error; this branch catches the
// caller's context expiring while waiting on the same load. Every other
// method uses coded, whose deadline is the plain caller timeout.
func codedInput(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &codedError{code: protocol.CodeTimeout, data: map[string]any{"reason": "input_deadline"}, msg: "input deadline exceeded"}
	}
	return coded(err)
}
