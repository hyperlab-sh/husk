package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The spec's exit codes (instances.md, "Exit codes").
const (
	// ExitOK: success.
	ExitOK = 0
	// ExitRemote: the instance answered with an error reply, or a local
	// store command failed.
	ExitRemote = 1
	// ExitUsage: unknown command, bad flag or argument, nothing sent.
	ExitUsage = 2
	// ExitNoInstance: nothing running, not registered, or the target is stale.
	ExitNoInstance = 3
	// ExitAmbiguous: several live instances and nothing chose one.
	ExitAmbiguous = 4
	// ExitTransport: connect failed, no reply in time, the connection
	// dropped, or discovery found only unresponsive instances.
	ExitTransport = 5
	// ExitProtocol: protocol major differs, unreadable reply, protocol.error
	// notification, unknown reply id, or one of the four reader error replies.
	ExitProtocol = 6
	// ExitEnvironment: the runtime directory is unusable.
	ExitEnvironment = 7
)

// The error codes the CLI itself produces; every Protocol error code is also
// a valid code, forwarded from the instance's reply.
const (
	CodeUsage       = "usage"
	CodeNoInstance  = "no_instance"
	CodeAmbiguous   = "ambiguous"
	CodeTransport   = "transport"
	CodeProtocol    = "protocol"
	CodeEnvironment = "environment"
	CodeStore       = "store"
)

// WarningUnresponsive is the only warning code of v1.
const WarningUnresponsive = "unresponsive"

// Candidate is one live instance named by an ambiguity error.
type Candidate struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// Error is every failure a command reports: the exit code to return, the
// code of the JSON error object and the human sentence (without the "husk: "
// prefix).
type Error struct {
	// Exit is the process exit code.
	Exit int
	// Code is the "code" of the error object: a Protocol code for an error
	// reply, otherwise one of the Code constants.
	Code string
	// Message is the human sentence.
	Message string
	// Data is the error object's data member: the reply's data, forwarded
	// unchanged, or the candidate list of an ambiguity error.
	Data json.RawMessage
	// Candidates is the ambiguity list, sorted by id; table mode prints it
	// under the message.
	Candidates []Candidate
	// Err is the wrapped cause.
	Err error
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.Err }

// Usage is a usage error: nothing was sent.
func Usage(format string, a ...any) *Error {
	return &Error{Exit: ExitUsage, Code: CodeUsage, Message: fmt.Sprintf(format, a...)}
}

// NoInstance is "nothing to talk to".
func NoInstance(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitNoInstance, Code: CodeNoInstance, Message: fmt.Sprintf(format, a...), Err: cause}
}

// Transport is a connect, timeout or disconnect failure.
func Transport(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitTransport, Code: CodeTransport, Message: fmt.Sprintf(format, a...), Err: cause}
}

// Protocol is a version or reply-reading failure.
func Protocol(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitProtocol, Code: CodeProtocol, Message: fmt.Sprintf(format, a...), Err: cause}
}

// Environment is an unusable runtime directory.
func Environment(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitEnvironment, Code: CodeEnvironment, Message: fmt.Sprintf(format, a...), Err: cause}
}

// Store is a failed local store command.
func Store(cause error, format string, a ...any) *Error {
	return &Error{Exit: ExitRemote, Code: CodeStore, Message: fmt.Sprintf(format, a...), Err: cause}
}

// Ambiguous is discovery finding several live instances; the candidates go
// into data (JSON) and under the message (table), sorted by id.
func Ambiguous(message string, candidates []Candidate) *Error {
	list := append([]Candidate(nil), candidates...)
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, _ := json.Marshal(map[string]any{"candidates": list})
	return &Error{Exit: ExitAmbiguous, Code: CodeAmbiguous, Message: message, Data: data, Candidates: list}
}

// FromReply maps an instance's error reply: exit 1 with its code and data
// forwarded unchanged, except the four codes that say the CLI itself sent
// something the instance could not read (parse_error, invalid_message,
// line_too_long, version_mismatch), which are exit 6.
func FromReply(pe *protocol.Error) *Error {
	exit := ExitRemote
	switch pe.Code {
	case protocol.CodeVersionMismatch, protocol.CodeParseError,
		protocol.CodeInvalidMessage, protocol.CodeLineTooLong:
		exit = ExitProtocol
	}
	return &Error{Exit: exit, Code: string(pe.Code), Message: pe.Message, Data: pe.Data, Err: pe}
}

// AsError returns err as an *Error. Anything that is not one is reported as a
// transport failure: the closed code set has no generic code, and the only
// non-*Error failures are streams and connections ending underneath a command.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Transport(err, "%v", err)
}
