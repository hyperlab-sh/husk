package contracts

import (
	"context"
	"errors"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// CallerKind says how a request was attributed; it is the shared kind type.
type CallerKind = protocol.CallerKind

// The caller kinds. A socket request is only ever cli or plugin; input and
// host are in-process Stem or frontend callers acting for the user.
const (
	CallerCLI    = protocol.CallerCLI
	CallerPlugin = protocol.CallerPlugin
	CallerInput  = protocol.CallerInput
	CallerHost   = protocol.CallerHost
)

// Caller is who sent a request, as the accepting module attributes it. It is
// the one shared identity type; PluginID is set only for CallerPlugin.
type Caller = protocol.Caller

// IsUserKind reports whether k acts as the user: the CLI and the in-process
// callers (input and host). A plugin never does, whatever it claims in params.
func IsUserKind(k CallerKind) bool {
	return k == CallerCLI || k == CallerInput || k == CallerHost
}

// Notifier pushes a notification on the caller's connection. Only watch
// methods use it.
type Notifier interface {
	Notify(n protocol.Message) error
}

// Handler serves one method family off the loop. It must not block the loop;
// it posts to it and waits on ctx, which is cancelled when the connection
// closes. It has the Server's Handler shape: a request in, a reply out.
type Handler interface {
	Serve(ctx context.Context, c Caller, req protocol.Message, n Notifier) protocol.Message
}

// CodedError is an error that carries a wire code (one of protocol.Code's
// values). Owner ports return it; adapters read Code and never inspect the
// message text.
type CodedError interface {
	error
	Code() string
}

// DataError is an optional extension of CodedError for errors that carry the
// wire error's structured data member (for example {"reason":"rate_limited"}
// or {"revision":9}).
type DataError interface {
	CodedError
	Data() map[string]any
}

// ErrShutdown is the cancellation cause of a request context ended because
// the host is shutting down, as opposed to the caller going away.
var ErrShutdown = errors.New("host is shutting down")

// ErrDeclined is returned (wrapped or not, matched with errors.Is) by a
// HostActionHandler that declines a key press: the action has nothing to do
// and the key belongs to the focused occupant. It is host-only. The host
// maps it to the Dispatcher's declined reply; it is never a CodedError and
// never crosses the wire.
var ErrDeclined = errors.New("host action declined")

// NewError returns a CodedError that also implements DataError. data may be nil.
func NewError(code protocol.Code, message string, data map[string]any) CodedError {
	return &codedError{code: string(code), message: message, data: data}
}

type codedError struct {
	code    string
	message string
	data    map[string]any
}

func (e *codedError) Error() string        { return e.message }
func (e *codedError) Code() string         { return e.code }
func (e *codedError) Data() map[string]any { return e.data }

// CodeOf returns the wire code of err: the code of the first CodedError in its
// chain, or "internal" when there is none. A nil err returns "".
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var coded CodedError
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return string(protocol.CodeInternal)
}
