package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The spec's exit codes (instances.md, "Exit codes").
const (
	// ExitOK: success.
	ExitOK = 0
	// ExitRemote: the instance replied with an error.
	ExitRemote = 1
	// ExitUsage: bad flags or arguments.
	ExitUsage = 2
	// ExitNoInstance: nothing running, not registered, or stale.
	ExitNoInstance = 3
	// ExitAmbiguous: several live instances and nothing chose one.
	ExitAmbiguous = 4
	// ExitTransport: connect failed, no reply in time, or the connection dropped.
	ExitTransport = 5
	// ExitProtocol: unparsable reply, or a different protocol major.
	ExitProtocol = 6
)

// Error is every failure a command reports: the exit code to return, a code
// for the JSON error object, and the human sentence (without the "husk: "
// prefix). Remote errors keep the instance's own code and data.
type Error struct {
	// Exit is the process exit code.
	Exit int
	// Code is the "code" of the JSON error object: a protocol code for a
	// remote error, otherwise one of usage, no_instance, ambiguous,
	// transport, protocol.
	Code string
	// Message is the human sentence.
	Message string
	// Data is the remote error's data object, when it sent one.
	Data json.RawMessage
	// Err is the wrapped cause.
	Err error
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.Err }

func usageErr(format string, a ...any) *Error {
	return &Error{Exit: ExitUsage, Code: "usage", Message: fmt.Sprintf(format, a...)}
}

func transportErr(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitTransport, Code: "transport", Message: fmt.Sprintf(format, a...), Err: cause}
}

func protocolErr(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitProtocol, Code: "protocol", Message: fmt.Sprintf(format, a...), Err: cause}
}

// remoteErr maps an error reply to its exit code. A reply that says our
// request was malformed (the codec codes) or that the versions differ is a
// protocol failure, exit 6; every other code is the instance reporting a
// failure, exit 1, and the script reads the code from the JSON error.
func remoteErr(pe *protocol.Error) *Error {
	exit := ExitRemote
	switch pe.Code {
	case protocol.CodeVersionMismatch, protocol.CodeParseError,
		protocol.CodeInvalidMessage, protocol.CodeLineTooLong:
		exit = ExitProtocol
	}
	return &Error{Exit: exit, Code: string(pe.Code), Message: pe.Message, Data: pe.Data, Err: pe}
}

// resolveErr maps a resolution failure to its exit code. The message of an
// ambiguity error carries the candidate list.
func resolveErr(err error) *Error {
	var ie *instance.Error
	if !errors.As(err, &ie) {
		return &Error{Exit: ExitRemote, Code: "error", Message: err.Error(), Err: err}
	}
	code := map[instance.ErrorKind]string{
		instance.KindUsage:      "usage",
		instance.KindNoInstance: "no_instance",
		instance.KindAmbiguous:  "ambiguous",
		instance.KindTransport:  "transport",
		instance.KindTaken:      "taken",
	}[ie.Kind]
	return &Error{Exit: ie.Kind.ExitCode(), Code: code, Message: ie.Error(), Err: err}
}

// asError wraps any error a command returned. A deadline becomes a transport
// failure naming the timeout; an interrupt a transport failure too.
func asError(err error, timeout string) *Error {
	var ce *Error
	if errors.As(err, &ce) {
		if errors.Is(ce.Err, context.DeadlineExceeded) && timeout != "" {
			c := *ce
			c.Message = "instance did not answer within " + timeout
			return &c
		}
		return ce
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return transportErr(err, "instance did not answer within %s", timeout)
	case errors.Is(err, context.Canceled):
		return transportErr(err, "interrupted")
	}
	return &Error{Exit: ExitRemote, Code: "error", Message: err.Error(), Err: err}
}
