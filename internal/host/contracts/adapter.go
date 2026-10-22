package contracts

import (
	"context"
	"errors"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// CallerKind says how a socket request was attributed.
type CallerKind uint8

const (
	// CallerCLI is the user's uncredentialed client (no token).
	CallerCLI CallerKind = iota + 1
	// CallerPlugin carries a valid per-launch plugin token.
	CallerPlugin
	// CallerStem is an in-process Stem module (Input, the palette) acting for
	// the user. It never arrives over the socket.
	CallerStem
)

// User reports whether the caller acts as the user: the CLI and the Stem.
// A plugin never does, whatever it claims in params.
func (k CallerKind) User() bool { return k == CallerCLI || k == CallerStem }

// Caller is who sent a request, as the accepting module attributes it. Field
// names and kinds match the Server's own Caller, so an adapter converts with
// one struct literal. PluginID is set only for CallerPlugin.
type Caller struct {
	Kind     CallerKind
	PluginID string
	ConnID   uint64
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
