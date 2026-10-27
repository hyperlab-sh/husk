// Package upstream implements independent requests to an existing server.
// No method starts, monitors or manages a server.
package upstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Error is a remote error or a named adapter failure.
type Error struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Data    map[string]string `json:"data,omitempty"`
	cause   error
	remote  bool
}

// IsRemote reports whether the error came from a decoded server reply.
func IsRemote(err error) bool { var e *Error; return errors.As(err, &e) && e.remote }

// Unwrap retains transport causes.
func (e *Error) Unwrap() error { return e.cause }

// Error formats the stable code and message.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Unavailable tells the user how to restore the upstream service.
func Unavailable() *Error {
	return &Error{Code: "unavailable", Message: "herdr server unavailable; start herdr and try again", Data: map[string]string{"reason": "herdr_absent"}}
}

// Timer is an injected clock's cancellable callback.
type Timer interface{ Stop() bool }

// Clock schedules request deadlines.
type Clock interface {
	AfterFunc(time.Duration, func()) Timer
}
type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Dialer opens a connection and must honor cancellation.
type Dialer func(context.Context, string, string) (net.Conn, error)

// Options supplies the resolved socket and injectable I/O and time.
type Options struct {
	Socket string
	Dial   Dialer
	Clock  Clock
}

// Client is immutable and safe for concurrent calls.
type Client struct{ opts Options }

// New constructs a client; nil seams use the operating system.
func New(o Options) *Client {
	if o.Dial == nil {
		o.Dial = (&net.Dialer{}).DialContext
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	return &Client{opts: o}
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
	Event  string          `json:"event"`
}

// Dial opens a raw connection for byte-exact forwarding; the caller owns closing it.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := c.opts.Dial(ctx, "unix", c.opts.Socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		failure := Unavailable()
		failure.cause = err
		return nil, failure
	}
	return conn, nil
}

// Call sends one request and retains vendor fields in its raw result.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if params == nil {
		params = map[string]any{}
	}
	if err = json.NewEncoder(conn).Encode(struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{"1", method, params}); err != nil {
		return nil, callError(ctx, method, err)
	}
	scan := bufio.NewScanner(conn)
	scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !scan.Scan() {
		err = scan.Err()
		if err == nil {
			err = io.EOF
		}
		return nil, callError(ctx, method, err)
	}
	var r response
	if err = json.Unmarshal(scan.Bytes(), &r); err != nil {
		return nil, protocolError(method, err)
	}
	if r.ID != "1" || r.Event != "" || (r.Error == nil) == (r.Result == nil) {
		return nil, protocolError(method, fmt.Errorf("invalid upstream reply"))
	}
	if r.Error != nil {
		r.Error.remote = true
		return nil, r.Error
	}
	return r.Result, nil
}
func protocolError(method string, err error) *Error {
	return &Error{Code: "herdr_protocol", Message: fmt.Sprintf("%s: %v", method, err)}
}
func callError(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return protocolError(method, err)
	}
	return Unavailable()
}

// Ping validates one server reply within a two-second injected deadline.
func (c *Client) Ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	timer := c.opts.Clock.AfterFunc(2*time.Second, func() { cancel(context.DeadlineExceeded) })
	defer timer.Stop()
	raw, err := c.Call(ctx, "ping", nil)
	if err != nil {
		return "", err
	}
	var pong struct {
		Type     string `json:"type"`
		Version  string `json:"version"`
		Protocol *int   `json:"protocol"`
	}
	if err = json.Unmarshal(raw, &pong); err != nil {
		return "", protocolError("ping", err)
	}
	if pong.Type != "pong" || pong.Protocol == nil {
		return "", protocolError("ping", fmt.Errorf("invalid ping result"))
	}
	if _, err = parseVersion(pong.Version); err != nil {
		return "", protocolError("ping", err)
	}
	return pong.Version, nil
}

// Check performs a one-shot ping and version check. It retains no attachment state.
func (c *Client) Check(ctx context.Context, minVersion string) (string, error) {
	version, err := c.Ping(ctx)
	if err != nil {
		return "", err
	}
	if err = CheckVersion(version, minVersion); err != nil {
		return "", err
	}
	return version, nil
}
