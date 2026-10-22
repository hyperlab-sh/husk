package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// ConnectTimeout bounds the dial: a local connect that takes longer is a
// failure (spec, "Limits").
const ConnectTimeout = 2 * time.Second

// ClientOptions configures Dial.
type ClientOptions struct {
	// Token is the plugin token (HUSK_PLUGIN_TOKEN) sent with every
	// request; empty is the user's own CLI and sends no token member.
	Token string
}

// Client is one socket connection to an instance: it sends requests and
// reads replies and, after Stream, notifications. It speaks pkg/protocol
// types only. A Client serves one caller at a time.
type Client struct {
	conn   net.Conn
	w      *protocol.Writer
	token  string
	nextID int64

	events chan event
	done   chan struct{}
	once   sync.Once
}

// event is one thing the reader goroutine saw: a message, a malformed
// line, or (closed channel) the end of the connection.
type event struct {
	msg  protocol.Message
	line *protocol.LineError
}

// Dial connects to the instance socket. A missing or refused socket is
// ExitNoInstance (the instance went away after resolution); any other
// failure is ExitTransport.
func Dial(ctx context.Context, socket string, opts ClientOptions) (*Client, error) {
	d := net.Dialer{Timeout: ConnectTimeout}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, &Error{Exit: ExitNoInstance, Code: "no_instance", Message: "nobody is listening on " + socket, Err: err}
		}
		return nil, transportErr(err, "connect %s: %v", socket, err)
	}
	c := &Client{
		conn:   conn,
		w:      protocol.NewWriter(conn),
		token:  opts.Token,
		events: make(chan event),
		done:   make(chan struct{}),
	}
	go c.read()
	return c, nil
}

// read feeds the connection's lines to events until it ends.
func (c *Client) read() {
	defer close(c.events)
	send := func(e event) bool {
		select {
		case c.events <- e:
			return true
		case <-c.done:
			return false
		}
	}
	_ = protocol.ReadLoop(c.conn,
		func(m protocol.Message) { send(event{msg: m}) },
		func(le *protocol.LineError) { send(event{line: le}) })
}

// Close closes the connection. It is idempotent.
func (c *Client) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		err = c.conn.Close()
	})
	return err
}

// Call sends one request and returns the raw result of its reply. An error
// reply is an *Error with the exit code of its protocol code; a malformed
// reply line is ExitProtocol; a connection that ends first is
// ExitTransport; ctx ending is ExitTransport wrapping ctx.Err.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, err := c.send(ctx, method, params)
	if err != nil {
		return nil, err
	}
	for {
		m, err := c.next(ctx, "connection closed by host")
		if err != nil {
			return nil, err
		}
		if m.Method == protocol.MethodProtocolError {
			return nil, protocolErr(nil, "host rejected the request: %s", protocolErrorText(m))
		}
		if k, _ := m.Kind(); k != protocol.KindReply || m.ID.String() != id.String() {
			continue // a notification outside a stream, or a stray reply
		}
		if m.Error != nil {
			return nil, remoteErr(m.Error)
		}
		return m.Result, nil
	}
}

// Stream is a request whose connection stays open: after the reply, the
// instance pushes notifications until the connection ends.
type Stream struct {
	c *Client
	// Result is the raw result of the opening reply (for state.watch the
	// current entries).
	Result json.RawMessage
}

// Stream sends a request and, on a success reply, returns a Stream over
// the same connection. It is the API behind the later watch commands.
func (c *Client) Stream(ctx context.Context, method string, params any) (*Stream, error) {
	res, err := c.Call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return &Stream{c: c, Result: res}, nil
}

// Next returns the next notification. When the connection ends it returns an
// ExitTransport error "instance went away"; ctx ending returns ctx's error
// wrapped as ExitTransport, which the caller maps (an interrupt is a
// watch's normal end).
func (s *Stream) Next(ctx context.Context) (protocol.Message, error) {
	for {
		m, err := s.c.next(ctx, "instance went away")
		if err != nil {
			return protocol.Message{}, err
		}
		if k, _ := m.Kind(); k != protocol.KindNotification {
			continue
		}
		if m.Method == protocol.MethodProtocolError {
			return protocol.Message{}, protocolErr(nil, "host rejected a message: %s", protocolErrorText(m))
		}
		return m, nil
	}
}

// send writes one request with the next id and returns that id.
func (c *Client) send(ctx context.Context, method string, params any) (protocol.ID, error) {
	c.nextID++
	id, err := protocol.NewIntID(c.nextID)
	if err != nil {
		return protocol.ID{}, protocolErr(err, "request id: %v", err)
	}
	m := protocol.Message{ID: &id, Method: method, Token: c.token}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return protocol.ID{}, usageErr("params: %v", err)
		}
		m.Params = raw
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(dl)
	}
	if err := c.w.Send(m); err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			return protocol.ID{}, usageErr("%s", pe.Message)
		}
		return protocol.ID{}, transportErr(err, "send %s: %v", method, err)
	}
	return id, nil
}

// next waits for the next message, a malformed line, the end of the
// connection (closedMsg) or ctx.
func (c *Client) next(ctx context.Context, closedMsg string) (protocol.Message, error) {
	select {
	case e, ok := <-c.events:
		switch {
		case !ok:
			return protocol.Message{}, transportErr(io.EOF, "%s", closedMsg)
		case e.line != nil:
			return protocol.Message{}, protocolErr(e.line, "unreadable reply from the instance: %v", e.line)
		}
		return e.msg, nil
	case <-ctx.Done():
		return protocol.Message{}, transportErr(ctx.Err(), "no reply: %v", ctx.Err())
	}
}

func protocolErrorText(m protocol.Message) string {
	var p protocol.ProtocolErrorParams
	if json.Unmarshal(m.Params, &p) != nil || p.Message == "" {
		return "protocol.error"
	}
	return string(p.Code) + ": " + p.Message
}
