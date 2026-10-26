//go:build linux || darwin

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type handlerFunc func(context.Context, Caller, protocol.Message, Notifier) protocol.Message

func (f handlerFunc) Serve(ctx context.Context, c Caller, m protocol.Message, n Notifier) protocol.Message {
	return f(ctx, c, m, n)
}

type tokenMap struct {
	mu sync.RWMutex
	m  map[string]string
}

func (t *tokenMap) Resolve(s string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	id, ok := t.m[s]
	return id, ok
}

func inertServer(t *testing.T) *SocketServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hs-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	s := New(Config{InstanceID: "test", RuntimeDir: dir, Clock: testclock.New(), Entry: instance.Entry{HostVersion: "test"}})
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func start(t *testing.T, s *SocketServer) {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
}
func dial(t *testing.T, s *SocketServer) (*net.UnixConn, *bufio.Reader) {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: s.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, bufio.NewReader(c)
}
func write(t *testing.T, c *net.UnixConn, line string) {
	t.Helper()
	if _, err := fmt.Fprintln(c, line); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, r *bufio.Reader) protocol.Message {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCallerAttributionPerRequestAndTokenRevocation(t *testing.T) {
	s := inertServer(t)
	tokens := &tokenMap{m: map[string]string{"valid": "Plugin.ID"}}
	s.cfg.Tokens = tokens
	s.Handle("sample", handlerFunc(func(_ context.Context, c Caller, m protocol.Message, _ Notifier) protocol.Message {
		if m.HasToken() {
			panic("credential leaked to handler")
		}
		// ConnID is never serialized, so the echo carries it separately.
		data, _ := json.Marshal(struct {
			Caller
			ConnID uint64 `json:"conn_id"`
		}{c, c.ConnID})
		return protocol.Message{Result: data}
	}))
	start(t, s)
	c, r := dial(t, s)
	for _, tc := range []struct {
		token  string
		want   CallerKind
		denied bool
	}{{"", CallerCLI, false}, {`,"token":"valid"`, CallerPlugin, false}, {`,"token":""`, "", true}, {`,"token":"unknown"`, "", true}} {
		write(t, c, `{"id":"a","method":"sample.echo"`+tc.token+`}`)
		got := read(t, r)
		if got.ID.String() != `"a"` {
			t.Fatal("id changed")
		}
		if tc.denied {
			if got.Error == nil || got.Error.Code != protocol.CodeDenied {
				t.Fatal(got)
			}
			continue
		}
		var caller struct {
			Caller
			ConnID uint64 `json:"conn_id"`
		}
		if err := json.Unmarshal(got.Result, &caller); err != nil {
			t.Fatal(err)
		}
		if caller.Kind != tc.want || caller.ConnID == 0 || (tc.want == CallerPlugin && caller.PluginID != "Plugin.ID") {
			t.Fatal(caller)
		}
	}
	tokens.mu.Lock()
	delete(tokens.m, "valid")
	tokens.mu.Unlock()
	write(t, c, `{"id":2,"method":"sample.echo","token":"valid"}`)
	if m := read(t, r); m.Error == nil || m.Error.Code != protocol.CodeDenied {
		t.Fatal("revoked token accepted")
	}
}

func TestPipeliningOutOfOrderAndNotifierLifetime(t *testing.T) {
	s := inertServer(t)
	blocked := make(chan struct{}, 1)
	release := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, m protocol.Message, n Notifier) protocol.Message {
		if m.ID.String() == "1" {
			blocked <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		if m.ID.String() == "3" {
			go func() { <-ctx.Done(); cancelled <- struct{}{} }()
			if err := n.Notify(protocol.Message{Method: protocol.MethodStateChanged, Params: json.RawMessage(`{}`)}); err != nil {
				return protocol.Message{Error: &protocol.Error{Code: protocol.CodeFailed, Message: "notify failed"}}
			}
		}
		return protocol.Message{Result: json.RawMessage(`{}`)}
	}))
	start(t, s)
	c, r := dial(t, s)
	write(t, c, `{"id":1,"method":"sample.wait"}`)
	testclock.Receive(t, blocked)
	write(t, c, `{"id":2,"method":"sample.fast"}`)
	if m := read(t, r); m.ID.String() != "2" {
		t.Fatal("reader blocked on first handler")
	}
	close(release)
	if m := read(t, r); m.ID.String() != "1" {
		t.Fatal(m)
	}
	write(t, c, `{"id":3,"method":"sample.watch"}`)
	a, b := read(t, r), read(t, r)
	if a.Method != protocol.MethodStateChanged || b.ID.String() != "3" {
		t.Fatal(a, b)
	}
	c.Close()
	testclock.Receive(t, cancelled)
}

func TestSaturatedConnectionRepliesBusyAndCancelsAllHandlersAtEOF(t *testing.T) {
	s := inertServer(t)
	started := make(chan struct{}, MaxInFlight)
	cancelled := make(chan struct{}, MaxInFlight)
	s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, _ protocol.Message, _ Notifier) protocol.Message {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return protocol.Message{Result: json.RawMessage(`{}`)}
	}))
	start(t, s)
	c, r := dial(t, s)
	for i := 0; i < MaxInFlight; i++ {
		write(t, c, fmt.Sprintf(`{"id":%d,"method":"sample.block"}`, i))
	}
	for i := 0; i < MaxInFlight; i++ {
		testclock.Receive(t, started)
	}
	write(t, c, `{"id":65,"method":"sample.block"}`)
	m := read(t, r)
	if m.Error == nil || m.Error.Code != protocol.CodeBusy || string(m.Error.Data) != `{"reason":"too_many_in_flight"}` {
		t.Fatal(m)
	}
	c.Close()
	for i := 0; i < MaxInFlight; i++ {
		testclock.Receive(t, cancelled)
	}
}

func TestBadLinesRecoverUnknownMethodsAndNeverAnswerReplies(t *testing.T) {
	s := inertServer(t)
	start(t, s)
	c, r := dial(t, s)
	for _, tc := range []struct {
		raw  string
		code protocol.Code
		id   bool
	}{
		{"not-json", protocol.CodeParseError, false},
		{string([]byte{0xff}), protocol.CodeParseError, false},
		{`{"id":7,"method":"session.ping","params":[]}`, protocol.CodeInvalidParams, true},
		{strings.Repeat("x", protocol.MaxLineBytes+1), protocol.CodeLineTooLong, false},
		{`{"id":7,"method":"unknown.method"}`, protocol.CodeUnknownMethod, true},
	} {
		write(t, c, tc.raw)
		m := read(t, r)
		if tc.id {
			if m.Error == nil || m.Error.Code != tc.code {
				t.Fatal(m)
			}
		} else {
			var p protocol.ProtocolErrorParams
			if err := json.Unmarshal(m.Params, &p); err != nil || p.Code != tc.code {
				t.Fatal(p, err)
			}
		}
	}
	write(t, c, `{"id":90,"result":{}}`)
	write(t, c, `{"method":"protocol.error","params":{}}`)
	write(t, c, `{"method":"unknown.notice"}`)
	write(t, c, `{"id":99,"method":"session.ping"}`)
	m := read(t, r)
	if m.ID.String() != "99" {
		t.Fatal("reply, protocol.error or unknown notification was answered")
	}
	var ping map[string]any
	if err := json.Unmarshal(m.Result, &ping); err != nil || ping["id"] != "test" || ping["protocol_version"] != protocol.Version || ping["host_version"] != "test" {
		t.Fatal(ping, err)
	}
}

func TestPeerCredentialsCheckedBeforeReading(t *testing.T) {
	for _, foreign := range []bool{true, false} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			s := inertServer(t)
			checked := make(chan struct{}, 1)
			s.peer = func(*net.UnixConn) (uint32, error) {
				checked <- struct{}{}
				if foreign {
					return uint32(os.Getuid() + 1), nil
				}
				return 0, errors.New("unreadable")
			}
			start(t, s)
			c, r := dial(t, s)
			testclock.Receive(t, checked)
			// Send no bytes: rejection must precede any attempt to decode a request.
			if _, err := r.ReadByte(); err == nil {
				t.Fatal("untrusted peer received data")
			}
			c.Close()
		})
	}
}

func TestClaimModesStaleSocketAndIdempotentUnregister(t *testing.T) {
	s := inertServer(t)
	start(t, s)
	for _, path := range []string{s.SocketPath(), instance.EntryPath(s.cfg.RuntimeDir, "test")} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("private mode", err)
		}
	}
	other := New(s.cfg)
	if err := other.Start(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.SocketPath()); err != nil {
		t.Fatal("failed claimant removed live owner")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.SocketPath(), instance.EntryPath(s.cfg.RuntimeDir, "test")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("owned file survived close", path, err)
		}
	}
	st, err := os.Stat(s.cfg.RuntimeDir)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("shared root removed", err)
	}
	stale := inertServer(t)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	start(t, stale)
}

func TestRegistrationFailureAndUnsafeRootsAreFatal(t *testing.T) {
	s := inertServer(t)
	if err := os.Mkdir(instance.EntryPath(s.cfg.RuntimeDir, "test"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("registration failure ignored")
	}
	if _, err := os.Lstat(s.SocketPath()); !os.IsNotExist(err) {
		t.Fatal("failed registration left socket", err)
	}
	bad := inertServer(t)
	if err := os.Chmod(bad.cfg.RuntimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := bad.Start(); err == nil {
		t.Fatal("unsafe root accepted")
	}
	long := inertServer(t)
	long.cfg.RuntimeDir = filepath.Join(long.cfg.RuntimeDir, strings.Repeat("x", instance.MaxSocketPathBytes))
	if err := long.Start(); err == nil {
		t.Fatal("oversize socket path accepted")
	}
}

func TestHandlerValidationPanicsAndCloseCancellation(t *testing.T) {
	s := inertServer(t)
	s.Handle("sample", handlerFunc(func(context.Context, Caller, protocol.Message, Notifier) protocol.Message {
		panic("private data must not appear in diagnostics")
	}))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("duplicate accepted")
			}
		}()
		s.Handle("sample", handlerFunc(nil))
	}()
	start(t, s)
	c, r := dial(t, s)
	write(t, c, `{"id":1,"method":"sample.crash"}`)
	if m := read(t, r); m.Error == nil || m.Error.Code != protocol.CodeInternal || strings.Contains(m.Error.Message, "private") {
		t.Fatal(m)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownMethodNotificationsAreIgnoredAndCoalesced(t *testing.T) {
	for _, method := range []string{"sample.nope", "unregistered.nope"} {
		t.Run(method, func(t *testing.T) {
			s := inertServer(t)
			clock := s.cfg.Clock.(*testclock.Clock)
			entered := make(chan Notice, 8)
			var posts atomic.Int32
			s.cfg.Post = func(v any) bool {
				posts.Add(1)
				entered <- v.(Notice)
				<-t.Context().Done()
				return false
			}
			s.Handle("sample", handlerFunc(func(_ context.Context, _ Caller, req protocol.Message, _ Notifier) protocol.Message {
				return protocol.Message{Error: &protocol.Error{Code: protocol.CodeUnknownMethod, Message: "no inbound handler for " + req.Method}}
			}))
			start(t, s)
			c, r := dial(t, s)

			// Far more than the notification queue holds, with Post about to block.
			for i := 0; i < 5*MaxNotificationQueue; i++ {
				write(t, c, fmt.Sprintf(`{"method":%q}`, method))
			}
			clock.Await(t, DiagnosticInterval)
			clock.Advance(DiagnosticInterval)
			n := testclock.Receive(t, entered)
			if n.Level != "debug" || n.Count == 0 || !strings.Contains(n.Message, method) {
				t.Fatal(n)
			}
			for i := 0; i < 5*MaxNotificationQueue; i++ {
				write(t, c, fmt.Sprintf(`{"method":%q}`, method))
			}
			// session.ping is answered inline on the reader after every earlier
			// line was read, so a protocol.error from a notification (enqueued
			// by the notification worker) would have to precede it or be
			// reported by the writer queue; assert both.
			write(t, c, `{"id":5,"method":"session.ping"}`)
			m := read(t, r)
			if m.ID == nil || m.ID.String() != "5" || m.Error != nil {
				t.Fatal("unexpected wire message:", m)
			}
			if method == "sample.nope" {
				// Wait for the worker to drain every queued notification.
				s.mu.Lock()
				var conn *connection
				for k := range s.conns {
					conn = k
				}
				s.mu.Unlock()
				for len(conn.notifySlots) > 0 {
					runtime.Gosched()
				}
				write(t, c, `{"id":6,"method":"session.ping"}`)
				if m := read(t, r); m.ID.String() != "6" {
					t.Fatal("unexpected wire message:", m)
				}
			}
			if got := posts.Load(); got != 1 {
				t.Fatal("posts:", got)
			}
		})
	}
}

func TestUnknownMethodRequestInRegisteredPrefixStillReplies(t *testing.T) {
	s := inertServer(t)
	s.Handle("sample", handlerFunc(func(_ context.Context, _ Caller, req protocol.Message, _ Notifier) protocol.Message {
		return protocol.Message{Error: &protocol.Error{Code: protocol.CodeUnknownMethod, Message: "no inbound handler for " + req.Method}}
	}))
	start(t, s)
	c, r := dial(t, s)
	write(t, c, `{"id":5,"method":"sample.nope"}`)
	m := read(t, r)
	if m.ID == nil || m.ID.String() != "5" || m.Error == nil || m.Error.Code != protocol.CodeUnknownMethod {
		t.Fatal(m)
	}
}
