package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// shortDir returns a private 0700 runtime directory under /tmp: t.TempDir
// paths can overflow the sun_path limit and are not private.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// srvConn is the server side of one accepted connection.
type srvConn struct {
	conn net.Conn
	w    *protocol.Writer
}

func (c *srvConn) send(m protocol.Message) { _ = c.w.Send(m) }

// raw writes a line verbatim, for malformed-reply tests.
func (c *srvConn) raw(line string) { _, _ = c.conn.Write([]byte(line + "\n")) }

func (c *srvConn) close() { _ = c.conn.Close() }

// reply answers req with result.
func (c *srvConn) reply(req protocol.Message, result any) {
	b, _ := json.Marshal(result)
	c.send(protocol.Message{ID: req.ID, Result: b})
}

// fail answers req with an error reply.
func (c *srvConn) fail(req protocol.Message, code protocol.Code, msg string) {
	c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: code, Message: msg}})
}

// fakeServer is a socket server standing in for an instance.
type fakeServer struct {
	t      *testing.T
	ln     net.Listener
	Socket string
	mu     sync.Mutex
	reqs   []protocol.Message
}

// handler serves one request. It runs on the connection's goroutine.
type handler func(c *srvConn, req protocol.Message)

// pingOK answers session.ping like a healthy 2.0 instance and delegates the
// rest to next (nil answers unknown_method).
func pingOK(id string, next handler) handler {
	return func(c *srvConn, req protocol.Message) {
		if req.Method == protocol.MethodSessionPing {
			c.reply(req, protocol.SessionPingResult{ID: id, PID: 4242, ProtocolVersion: protocol.Version, HostVersion: "9.9.9", UptimeMS: 1500})
			return
		}
		if next == nil {
			c.fail(req, protocol.CodeUnknownMethod, "no handler for "+req.Method)
			return
		}
		next(c, req)
	}
}

// serve starts a fake instance on <dir>/<id>.sock and registers it.
func serve(t *testing.T, dir, id string, h handler) *fakeServer {
	t.Helper()
	sock := instance.SocketPath(dir, id)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, ln: ln, Socket: sock}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveConn(conn, h)
		}
	}()
	register(t, dir, id, sock, protocol.Version)
	return s
}

func (s *fakeServer) serveConn(conn net.Conn, h handler) {
	defer conn.Close()
	c := &srvConn{conn: conn, w: protocol.NewWriter(conn)}
	_ = protocol.ReadLoop(conn, func(m protocol.Message) {
		s.mu.Lock()
		s.reqs = append(s.reqs, m)
		s.mu.Unlock()
		if m.ID != nil {
			h(c, m)
		}
	}, nil)
}

// requests returns what the server received, including pings.
func (s *fakeServer) requests() []protocol.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Message(nil), s.reqs...)
}

func register(t *testing.T, dir, id, sock, version string) {
	t.Helper()
	if err := instance.Register(dir, instance.Entry{ID: id, PID: os.Getpid(), Socket: sock, ProtocolVersion: version}); err != nil {
		t.Fatal(err)
	}
}

// harness is a test Env with captured output.
type harness struct {
	out, errb bytes.Buffer
	env       Env
	dir       string
}

// newHarness builds an Env over dir: no environment variables, not a TTY.
func newHarness(t *testing.T, dir string, getenv map[string]string) *harness {
	t.Helper()
	h := &harness{dir: dir}
	h.env = Env{
		Stdout:     &h.out,
		Stderr:     &h.errb,
		Stdin:      bytes.NewReader(nil),
		Getenv:     func(k string) string { return getenv[k] },
		RuntimeDir: func() (string, error) { return dir, nil },
	}
	return h
}

func (h *harness) run(args ...string) int {
	return Main(context.Background(), args, h.env)
}
