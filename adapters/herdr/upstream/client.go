// Package upstream implements the adapter's read-only connection to the server.
// Calls use independent connections; an event Stream owns its connection until
// cancellation, EOF, a remote error, or Close. No method manages a server.
package upstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Error is a remote error or a named adapter failure. Data is adapter-local;
// upstream error bodies contain only code and message.
type Error struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Data    map[string]string `json:"data,omitempty"`
}

// Error formats the stable code and human-readable message.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Unavailable returns the failure actions expose while the server is absent.
func Unavailable() *Error {
	return &Error{Code: "unavailable", Message: "herdr server unavailable", Data: map[string]string{"reason": "herdr_absent"}}
}

// Timer is an injected clock's cancellable callback.
type Timer interface{ Stop() bool }

// Clock schedules deadlines and retry delays without requiring wall time.
type Clock interface {
	AfterFunc(time.Duration, func()) Timer
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Dialer opens a connection and must honor context cancellation.
type Dialer func(context.Context, string, string) (net.Conn, error)

// Options supplies the already resolved socket and injectable I/O and time.
type Options struct {
	Socket string
	Dial   Dialer
	Clock  Clock
}

// Client is immutable and safe for concurrent calls.
type Client struct{ opts Options }

// New constructs a client. Nil Dial and Clock use the operating system.
func New(opts Options) *Client {
	if opts.Dial == nil {
		opts.Dial = (&net.Dialer{}).DialContext
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	return &Client{opts: opts}
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
}

func reader(c net.Conn) *bufio.Scanner {
	s := bufio.NewScanner(c)
	s.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return s
}

func receive(s *bufio.Scanner) (response, error) {
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return response{}, err
		}
		return response{}, io.EOF
	}
	var r response
	if err := json.Unmarshal(s.Bytes(), &r); err != nil {
		return r, err
	}
	if r.Error != nil {
		return r, r.Error
	}
	return r, nil
}

// Dial opens a raw upstream connection for byte-exact proxy forwarding. The
// context bounds dialing only; the caller owns closing the returned connection.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := c.opts.Dial(ctx, "unix", c.opts.Socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, Unavailable()
	}
	return conn, nil
}

func (c *Client) open(ctx context.Context) (net.Conn, func(), error) {
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	closeConn := func() { stop(); _ = conn.Close() }
	return conn, closeConn, nil
}

func send(conn net.Conn, method string, params any) error {
	if params == nil {
		params = map[string]any{}
	}
	return json.NewEncoder(conn).Encode(struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{"1", method, params})
}

func reply(r response) error {
	if r.ID != "1" || r.Result == nil || r.Event != "" {
		return fmt.Errorf("invalid upstream reply")
	}
	return nil
}

// Call sends one request and returns the raw result, retaining vendor fields.
// Transport failures use unavailable/herdr_absent; remote errors are unchanged.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	conn, closeConn, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	if err = send(conn, method, params); err != nil {
		return nil, callError(ctx, method, err)
	}
	r, err := receive(reader(conn))
	if err != nil {
		return nil, callError(ctx, method, err)
	}
	if err = reply(r); err != nil {
		return nil, protocolError(method, err)
	}
	return r.Result, nil
}

func protocolError(method string, err error) *Error {
	return &Error{Code: "herdr_protocol", Message: fmt.Sprintf("%s: %v", method, err)}
}

func attachError(method string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	var named *Error
	if errors.As(err, &named) && (named.Code == "unavailable" || named.Code == "herdr_too_old" || named.Code == "herdr_protocol") {
		return err
	}
	return protocolError(method, err)
}

func callError(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var remote *Error
	if errors.As(err, &remote) {
		return remote
	}
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &shape) || errors.Is(err, bufio.ErrTooLong) {
		return protocolError(method, err)
	}
	return Unavailable()
}

func (c *Client) bounded(ctx context.Context, d time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	timer := c.opts.Clock.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })
	return ctx, func() { timer.Stop(); cancel(context.Canceled) }
}

// Ping bounds both dialing and reading to two seconds on the injected clock.
func (c *Client) Ping(ctx context.Context) (string, error) {
	ctx, done := c.bounded(ctx, 2*time.Second)
	defer done()
	raw, err := c.Call(ctx, "ping", nil)
	if err != nil {
		return "", attachError("ping", err)
	}
	var pong struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	}
	if err = json.Unmarshal(raw, &pong); err != nil {
		return "", protocolError("ping", err)
	}
	if pong.Type != "pong" {
		return "", protocolError("ping", fmt.Errorf("invalid ping result"))
	}
	if _, err = parseVersion(pong.Version); err != nil {
		return "", protocolError("ping", err)
	}
	return pong.Version, nil
}

// Session is a validated attach. Plugins is re-read on every successful attach.
type Session struct {
	Version string
	Plugins json.RawMessage
}

// Attach performs ping, the version check, and one plugin.list, in that order.
// Ping and plugin.list each have an independent two-second injected deadline.
// It never starts a process or sends a management request.
func (c *Client) Attach(ctx context.Context, minVersion string) (*Session, error) {
	version, err := c.Ping(ctx)
	if err != nil {
		return nil, err
	}
	if err = CheckVersion(version, minVersion); err != nil {
		return nil, attachError("ping version check", err)
	}
	ctx, done := c.bounded(ctx, 2*time.Second)
	defer done()
	plugins, err := c.Call(ctx, "plugin.list", nil)
	if err != nil {
		return nil, attachError("plugin.list", err)
	}
	var list struct {
		Type    string          `json:"type"`
		Plugins json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(plugins, &list) != nil || list.Type != "plugin_list" || len(list.Plugins) == 0 || list.Plugins[0] != '[' {
		return nil, protocolError("plugin.list", fmt.Errorf("invalid result"))
	}
	return &Session{Version: version, Plugins: list.Plugins}, nil
}

// Backoff is a caller-owned exponential retry sequence: 5, 10, 20, then 30s.
// Start a new sequence after a successful attach or when a stream is lost.
type Backoff struct{ next time.Duration }

// Next returns the next bounded delay.
func (b *Backoff) Next() time.Duration {
	if b.next == 0 {
		b.next = 5 * time.Second
	}
	d := b.next
	b.next *= 2
	if b.next > 30*time.Second {
		b.next = 30 * time.Second
	}
	return d
}

// WaitAttach retries absent servers with the specified backoff. notify runs
// synchronously with absent, attached, or too_old. Both attach deadlines retry
// as absent. Cancellation stops the wait; version refusal is terminal. Malformed
// replies, invalid versions/results and remote errors other than unavailable are
// terminal *Error values with code herdr_protocol and the method in Message, so
// the shim can publish failed/last_error without matching prose. A stream owner
// calls it again after EOF/error.
func (c *Client) WaitAttach(ctx context.Context, minVersion string, notify func(string)) (*Session, error) {
	var backoff Backoff
	for {
		session, err := c.Attach(ctx, minVersion)
		if err == nil {
			if notify != nil {
				notify("attached")
			}
			return session, nil
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		var named *Error
		if errors.As(err, &named) && named.Code == "herdr_too_old" {
			if notify != nil {
				notify("too_old")
			}
			return nil, err
		}
		if !errors.As(err, &named) && !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if named != nil && named.Code != "unavailable" {
			return nil, err
		}
		if notify != nil {
			notify("absent")
		}
		ready := make(chan struct{})
		timer := c.opts.Clock.AfterFunc(backoff.Next(), func() { close(ready) })
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-ready:
			timer.Stop()
		}
	}
}

// Event preserves the server's event name and payload without translating them.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// Stream reads an acknowledged event subscription. Next has one reader; Close
// is safe from another goroutine. Cancellation also closes the connection.
type Stream struct {
	scan  *bufio.Scanner
	close func()
	once  sync.Once
	ctx   context.Context
}

// Subscribe deduplicates hook names and sends one subscription entry per name.
// Names requiring pane-specific parameters in the committed schema are refused
// with invalid_subscription before dialing. The supplied context owns the entire
// stream, not only its opening handshake.
func (c *Client) Subscribe(ctx context.Context, names []string) (*Stream, error) {
	entries := make([]map[string]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		switch name {
		case "pane.agent_status_changed", "pane.output_matched", "pane.scroll_changed":
			return nil, &Error{Code: "invalid_subscription", Message: fmt.Sprintf("events.subscribe: %s requires pane-specific parameters", name)}
		}
		if !seen[name] {
			entries = append(entries, map[string]string{"type": name})
			seen[name] = true
		}
	}
	conn, closeConn, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	s := &Stream{scan: reader(conn), close: closeConn, ctx: ctx}
	if err = send(conn, "events.subscribe", map[string]any{"subscriptions": entries}); err != nil {
		s.Close()
		return nil, callError(ctx, "events.subscribe", err)
	}
	r, err := receive(s.scan)
	if err != nil {
		s.Close()
		return nil, callError(ctx, "events.subscribe", err)
	}
	var ack struct {
		Type string `json:"type"`
	}
	if reply(r) != nil || json.Unmarshal(r.Result, &ack) != nil || ack.Type != "subscription_started" {
		s.Close()
		return nil, protocolError("events.subscribe", fmt.Errorf("invalid acknowledgement"))
	}
	return s, nil
}

// Next returns a pushed event. EOF or any remote error is terminal and closes
// the stream; the shim marks the server lost and reattaches. Schema-defined
// errors carry a request id. An id-less error body is also terminal defensively;
// the upstream schema does not define that framing.
func (s *Stream) Next() (Event, error) {
	r, err := receive(s.scan)
	if err == nil && (r.Event == "" || r.Data == nil || r.Result != nil) {
		err = fmt.Errorf("invalid event envelope")
	}
	if err != nil {
		s.Close()
		if s.ctx.Err() != nil {
			return Event{}, context.Cause(s.ctx)
		}
		return Event{}, err
	}
	return Event{Event: r.Event, Data: r.Data}, nil
}

// Close releases the subscription and is idempotent.
func (s *Stream) Close() { s.once.Do(s.close) }
