package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type connection struct {
	server                                *SocketServer
	raw                                   *net.UnixConn
	id                                    uint64
	ctx                                   context.Context
	cancel                                context.CancelFunc
	writer                                *protocol.Writer
	once                                  sync.Once
	slots                                 chan struct{}
	notifications                         chan notification
	notifySlots                           chan struct{}
	notifyDone                            chan struct{}
	budgetMu                              sync.Mutex
	window                                time.Time
	count                                 int
	diagnosticCount, diagnosticSuppressed uint64
	lastDiagnosticCode                    protocol.Code
	diagnosticTimer                       protocol.Timer
	lastDiagnosticPost                    time.Time
	hasDiagnosticPost                     bool
	flightMu                              sync.Mutex
	flight                                int
	draining                              bool
	flightIdle                            chan struct{}
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
	c.writer = protocol.NewQueuedWriter(struct{ io.Writer }{raw}, protocol.QueueOptions{Capacity: QueueCapacity, WriteTimeout: WriteTimeout, Clock: s.cfg.Clock, Closer: raw, OnFail: func(error) { c.noteDiagnostic(protocol.CodeUnavailable); c.close(); c.publishDiagnostic() }})
	return c
}
func (c *connection) close() {
	c.once.Do(func() {
		c.cancel()
		c.budgetMu.Lock()
		if c.diagnosticTimer != nil {
			c.diagnosticTimer.Stop()
			c.diagnosticTimer = nil
		}
		c.budgetMu.Unlock()
		c.writer.Abort()
		_ = c.raw.Close()
	})
}

// beginFlight counts a request handler as in flight; false once draining.
func (c *connection) beginFlight() bool {
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	if c.draining {
		return false
	}
	c.flight++
	return true
}

func (c *connection) endFlight() {
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	c.flight--
	if c.flight == 0 && c.flightIdle != nil {
		close(c.flightIdle)
		c.flightIdle = nil
	}
}

// drain is the graceful end of a connection: it stops taking requests, waits
// for the request handlers already running to enqueue their replies, lets the
// writer flush its queue, then closes the connection. A handler or a peer that
// does not finish within WriteTimeout (on the injected clock) is cut off.
func (c *connection) drain() {
	var idle chan struct{}
	c.flightMu.Lock()
	c.draining = true
	if c.flight > 0 {
		idle = make(chan struct{})
		c.flightIdle = idle
	}
	c.flightMu.Unlock()
	t := c.server.cfg.Clock.AfterFunc(WriteTimeout, c.close)
	defer t.Stop()
	if idle != nil {
		select {
		case <-idle:
		case <-c.ctx.Done():
		}
	}
	_ = c.writer.Close()
	<-c.writer.Done()
	c.close()
}

func (c *connection) run() {
	defer c.publishDiagnostic()
	defer c.close()
	go c.runNotifications()
	_ = protocol.ReadLoopFirst(c.raw, c.rejectLegacyHello, c.receive, func(e *protocol.LineError) {
		if m, ok := e.Response(); ok {
			c.respond(m)
		} else {
			c.noteDiagnostic(e.Code)
		}
	})
}

// rejectLegacyHello sees only the first line of the connection. A major-1
// hello gets the one major-1 line, the connection is closed once it has
// flushed, and the plugin the hello names is failed through Config.Legacy. The
// line reaches no handler and is not an invalid_message diagnostic.
func (c *connection) rejectLegacyHello(line []byte) bool {
	pluginID, ok := protocol.IsLegacyHello(line)
	if !ok {
		return false
	}
	name := pluginID
	if name == "" {
		name = "unknown plugin"
	}
	c.server.post(c.id, "warn", "plugin "+name+" spoke protocol major 1 on the socket")
	if err := c.writer.SendLine(protocol.LegacyRejectionLine()); err == nil {
		c.drain()
	}
	if c.server.cfg.Legacy != nil {
		c.server.cfg.Legacy.FailLegacy(pluginID)
	}
	return true
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
func (c *connection) respond(m protocol.Message) {
	if m.Error != nil {
		c.noteDiagnostic(m.Error.Code)
	}
	if m.Method == protocol.MethodProtocolError {
		var p protocol.ProtocolErrorParams
		_ = json.Unmarshal(m.Params, &p)
		c.budgetMu.Lock()
		now := c.server.cfg.Clock.Now()
		if c.window.IsZero() || now.Sub(c.window) >= time.Second {
			c.window = now
			c.count = 0
		}
		if c.count >= protocol.MaxProtocolErrorsPerSecond {
			c.noteDiagnosticLocked(p.Code, true)
			c.budgetMu.Unlock()
			return
		}
		c.count++
		c.noteDiagnosticLocked(p.Code, false)
		c.budgetMu.Unlock()
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
		c.noteDiagnostic(protocol.CodeInvalidMessage)
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
			}{e.ID, e.PID, protocol.Version, e.HostVersion, uptime})
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
			c.noteDiagnostic(protocol.CodeUnknownMethod)
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
		if !c.beginFlight() {
			<-c.slots
			c.reject(req, protocol.CodeCancelled, "instance is shutting down", json.RawMessage(`{"reason":"shutdown"}`))
			return
		}
		go func() {
			defer func() { <-c.slots }()
			defer c.endFlight()
			c.invoke(h, caller, req)
		}()
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
		if reply.Error != nil && reply.Error.Code == protocol.CodeUnknownMethod {
			// protocol.md: a notification for an unknown method gets no
			// response and is logged at debug level (a newer minor may send it).
			c.server.post(c.id, "debug", "ignored notification for unknown method "+req.Method)
			return
		}
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
