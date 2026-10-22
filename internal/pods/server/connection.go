package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type connection struct {
	server            *SocketServer
	raw               *net.UnixConn
	id                uint64
	ctx               context.Context
	cancel            context.CancelFunc
	writer            *protocol.Writer
	once              sync.Once
	slots             chan struct{}
	notifications     chan notification
	notifySlots       chan struct{}
	notifyDone        chan struct{}
	budgetMu          sync.Mutex
	window            time.Time
	count, suppressed int
}

type notification struct {
	handler Handler
	caller  Caller
	message protocol.Message
}

func newConnection(s *SocketServer, raw *net.UnixConn, id uint64) *connection {
	ctx, cancel := context.WithCancel(context.Background())
	c := &connection{server: s, raw: raw, id: id, ctx: ctx, cancel: cancel, slots: make(chan struct{}, MaxInFlight), notifications: make(chan notification, MaxNotificationQueue), notifySlots: make(chan struct{}, MaxNotificationQueue), notifyDone: make(chan struct{})}
	// Use the queue watchdog rather than an absolute native write deadline:
	// an injected clock's epoch need not be the operating system's epoch.
	c.writer = protocol.NewQueuedWriter(struct{ io.Writer }{raw}, protocol.QueueOptions{Capacity: QueueCapacity, WriteTimeout: WriteTimeout, Clock: s.cfg.Clock, Closer: raw, OnFail: func(err error) { c.close(); s.post(id, "warn", "IPC connection closed: "+err.Error()) }})
	return c
}
func (c *connection) close() { c.once.Do(func() { c.cancel(); c.writer.Abort(); _ = c.raw.Close() }) }
func (c *connection) run() {
	defer c.flush()
	defer c.close()
	go c.runNotifications()
	_ = protocol.ReadLoop(c.raw, c.receive, func(e *protocol.LineError) {
		if m, ok := e.Response(); ok {
			c.respond(m)
		} else {
			c.server.post(c.id, "warn", fmt.Sprintf("husk: protocol error from connection %d: %s: %s", c.id, e.Code, e.Err))
		}
	})
}

// Notify implements Notifier for this connection, including after a watch
// handler returns. It refuses requests/replies and fails after cancellation.
func (c *connection) Notify(m protocol.Message) error {
	kind, err := m.Kind()
	if err != nil {
		return err
	}
	if kind != protocol.KindNotification || m.HasToken() {
		return errors.New("server: notifier requires an uncredentialed notification")
	}
	return c.send(m)
}
func (c *connection) send(m protocol.Message) error {
	select {
	case <-c.ctx.Done():
		return net.ErrClosed
	default:
	}
	err := c.writer.Send(m)
	if err != nil && c.writer.Err() != nil {
		c.close()
	}
	return err
}
func (c *connection) flush() {
	c.budgetMu.Lock()
	defer c.budgetMu.Unlock()
	if c.suppressed > 0 {
		c.server.post(c.id, "warn", fmt.Sprintf("connection %d: %d rejections suppressed", c.id, c.suppressed))
		c.suppressed = 0
	}
}
func (c *connection) respond(m protocol.Message) {
	if m.Method == protocol.MethodProtocolError {
		c.budgetMu.Lock()
		now := c.server.cfg.Clock.Now()
		if c.window.IsZero() || now.Sub(c.window) >= time.Second {
			if c.suppressed > 0 {
				c.server.post(c.id, "warn", fmt.Sprintf("connection %d: %d rejections suppressed", c.id, c.suppressed))
			}
			c.window = now
			c.count = 0
			c.suppressed = 0
		}
		if c.count >= protocol.MaxProtocolErrorsPerSecond {
			c.suppressed++
			c.budgetMu.Unlock()
			return
		}
		c.count++
		c.budgetMu.Unlock()
	}
	if m.Error != nil {
		c.server.post(c.id, "warn", fmt.Sprintf("husk: protocol error from connection %d: %s: %s", c.id, m.Error.Code, m.Error.Message))
	}
	if m.Method == protocol.MethodProtocolError {
		var p protocol.ProtocolErrorParams
		_ = json.Unmarshal(m.Params, &p)
		c.server.post(c.id, "warn", fmt.Sprintf("husk: protocol error from connection %d: %s: %s", c.id, p.Code, p.Message))
	}
	_ = c.send(m)
}
func (c *connection) reject(req protocol.Message, code protocol.Code, message string, data json.RawMessage) {
	if req.ID != nil {
		c.respond(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: code, Message: message, Data: data}})
		return
	}
	if req.Method == protocol.MethodProtocolError {
		return
	}
	p, _ := json.Marshal(protocol.ProtocolErrorParams{Code: code, Message: message, Method: req.Method, Data: data})
	c.respond(protocol.Message{Method: protocol.MethodProtocolError, Params: p})
}
func (c *connection) receive(req protocol.Message) {
	select {
	case <-c.ctx.Done():
		return
	default:
	}
	kind, _ := req.Kind()
	if kind == protocol.KindReply || req.Method == protocol.MethodProtocolError {
		c.server.post(c.id, "debug", "socket reply or protocol.error ignored")
		return
	}
	caller := Caller{Kind: CallerCLI, ConnID: c.id}
	if req.HasToken() {
		var ok bool
		if c.server.cfg.Tokens != nil && req.Token != "" {
			caller.PluginID, ok = c.server.cfg.Tokens.Resolve(req.Token)
		}
		if !ok || caller.PluginID == "" {
			c.reject(req, protocol.CodeDenied, "unknown or revoked plugin token", nil)
			return
		}
		caller.Kind = CallerPlugin
	}
	// Never forward the credential itself into owner code or diagnostics.
	req = protocol.Message{ID: req.ID, Method: req.Method, Params: req.Params}
	if req.Method == protocol.MethodSessionPing {
		if req.ID != nil {
			e := c.server.cfg.Entry
			uptime := c.server.cfg.Clock.Now().Sub(e.StartedAt).Milliseconds()
			if uptime < 0 {
				uptime = 0
			}
			data, _ := json.Marshal(struct {
				ID              string `json:"id"`
				PID             int    `json:"pid"`
				ProtocolVersion string `json:"protocol_version"`
				HostVersion     string `json:"host_version"`
				Uptime          int64  `json:"uptime_ms"`
			}{e.ID, e.PID, protocol.Version, e.HuskVersion, uptime})
			c.respond(protocol.Message{ID: req.ID, Result: data})
		}
		return
	}
	prefix, _, _ := strings.Cut(req.Method, ".")
	h := c.server.handlers[prefix]
	if h == nil {
		if req.ID != nil {
			c.reject(req, protocol.CodeUnknownMethod, "unknown method", nil)
		} else {
			c.server.post(c.id, "debug", "unknown notification ignored: "+req.Method)
		}
		return
	}
	if req.ID == nil {
		select {
		case c.notifySlots <- struct{}{}:
			c.notifications <- notification{h, caller, req}
		default:
			c.reject(req, protocol.CodeBusy, "too many pending notifications", json.RawMessage(`{"reason":"too_many_in_flight"}`))
		}
		return
	}
	select {
	case c.slots <- struct{}{}:
		go func() { defer func() { <-c.slots }(); c.invoke(h, caller, req) }()
	default:
		c.reject(req, protocol.CodeBusy, "too many in-flight requests", json.RawMessage(`{"reason":"too_many_in_flight"}`))
	}
}
func (c *connection) runNotifications() {
	defer close(c.notifyDone)
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		select {
		case <-c.ctx.Done():
			return
		case n := <-c.notifications:
			c.invoke(n.handler, n.caller, n.message)
			<-c.notifySlots
		}
	}
}

func (c *connection) invoke(h Handler, caller Caller, req protocol.Message) {
	reply := func() (out protocol.Message) {
		defer func() {
			if recover() != nil {
				out = protocol.Message{Error: &protocol.Error{Code: protocol.CodeInternal, Message: "handler panicked"}}
			}
		}()
		return h.Serve(c.ctx, caller, req, c)
	}()
	if req.ID == nil {
		if reply.Error != nil {
			c.reject(req, reply.Error.Code, reply.Error.Message, reply.Error.Data)
		}
		return
	}
	// The transport always echoes its own request id, never a handler's id.
	out := protocol.Message{ID: req.ID, Result: reply.Result, Error: reply.Error}
	if out.Validate() != nil {
		out = protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeInternal, Message: "handler returned an invalid reply"}}
	}
	if err := c.send(out); err != nil && c.writer.Err() == nil {
		_ = c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeInternal, Message: "handler reply could not be encoded"}})
	}
}
