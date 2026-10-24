package upstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

type manualTimer struct {
	mu     sync.Mutex
	active bool
	d      time.Duration
	f      func()
}

func (m *manualTimer) Stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := m.active
	m.active = false
	return was
}
func (m *manualTimer) fire() {
	m.mu.Lock()
	active := m.active
	m.active = false
	m.mu.Unlock()
	if active {
		m.f()
	}
}

type manualClock struct{ added chan *manualTimer }

func newClock() *manualClock { return &manualClock{added: make(chan *manualTimer, 64)} }
func (c *manualClock) AfterFunc(d time.Duration, f func()) upstream.Timer {
	timer := &manualTimer{active: true, d: d, f: f}
	c.added <- timer
	return timer
}
func (c *manualClock) take(t *testing.T, d time.Duration) *manualTimer {
	t.Helper()
	for {
		select {
		case timer := <-c.added:
			timer.mu.Lock()
			active := timer.active
			timer.mu.Unlock()
			if !active {
				continue
			}
			if timer.d != d {
				t.Fatalf("delay = %v, want %v", timer.d, d)
			}
			return timer
		case <-t.Context().Done():
			t.Fatal("test cancelled waiting for timer")
			return nil
		}
	}
}

func socket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ha-")
	if err != nil {
		t.Fatal(err)
	}
	// Remove only the known socket and empty directory; never walk a user's tree.
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "s")); _ = os.Remove(dir) })
	return filepath.Join(dir, "s")
}
func server(t *testing.T, path string, opts fakeherdr.Options) *fakeherdr.Server {
	t.Helper()
	s, err := fakeherdr.Start(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func client(path string, clock *manualClock) *upstream.Client {
	return upstream.New(upstream.Options{Socket: path, Clock: clock})
}
func named(t *testing.T, err error, code string) *upstream.Error {
	t.Helper()
	var e *upstream.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
	return e
}

func TestResolveSocket(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts upstream.SocketOptions
		want string
		bad  bool
	}{
		{"default", upstream.SocketOptions{Home: "/home/test"}, "/home/test/.config/herdr/herdr.sock", false},
		{"config file", upstream.SocketOptions{ConfigPath: "/tmp/config/custom.toml"}, "/tmp/config/herdr.sock", false},
		{"inherited", upstream.SocketOptions{SocketPath: "/tmp/inherited.sock"}, "/tmp/inherited.sock", false},
		{"session wins", upstream.SocketOptions{Session: "review", SocketPath: "/tmp/ignored", ConfigPath: "/tmp/config/custom.toml"}, "/tmp/config/sessions/review/herdr.sock", false},
		{"default session", upstream.SocketOptions{Session: "default", Home: "/home/test"}, "/home/test/.config/herdr/herdr.sock", false},
		{"dot", upstream.SocketOptions{Session: "..", Home: "/home/test"}, "", true},
		{"slash", upstream.SocketOptions{Session: "a/b", Home: "/home/test"}, "", true},
		{"unicode", upstream.SocketOptions{Session: "é", Home: "/home/test"}, "", true},
		{"no home", upstream.SocketOptions{}, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := upstream.ResolveSocket(tt.opts)
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("got %q, %v; want %q, bad=%v", got, err, tt.want, tt.bad)
			}
		})
	}
}

func TestVersions(t *testing.T) {
	for _, tt := range []struct {
		server, min, want string
		bad               bool
	}{
		{"0.9.5", "0.9.0", "", false}, {"0.9.5", "0.9.5+build", "", false},
		{"0.8.2", "0.8.0", "needs 0.9.0, server is 0.8.2", false}, {"0.9.5", "0.10.0", "needs 0.10.0, server is 0.9.5", false},
		{"0.9.0-rc.1", "0.9.0", "needs 0.9.0, server is 0.9.0-rc.1", false}, {"0.9.5-beta.2", "0.9.5-beta.10", "needs 0.9.5-beta.10, server is 0.9.5-beta.2", false},
		{"0.9.5-beta.10", "0.9.5-beta.2", "", false}, {"0.9.5-1", "0.9.5-alpha", "needs 0.9.5-alpha, server is 0.9.5-1", false},
		{"0.9.5-alpha", "0.9.5-1", "", false}, {"1.0.0", "0.99.0", "", false},
		{"0.9.5", "garbage", "", true}, {"0.09.5", "0.9.0", "", true},
		{"0.9.5-01", "0.9.0", "", true}, {"0.9.5", "0.9", "", true},
	} {
		t.Run(tt.server+"_"+tt.min, func(t *testing.T) {
			err := upstream.CheckVersion(tt.server, tt.min)
			if tt.want != "" {
				e := named(t, err, "herdr_too_old")
				if e.Message != tt.want {
					t.Fatalf("message = %q, want %q", e.Message, tt.want)
				}
			} else if (err != nil) != tt.bad {
				t.Fatalf("error = %v, bad=%v", err, tt.bad)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	var b upstream.Backoff
	for _, d := range []time.Duration{5, 10, 20, 30, 30, 30} {
		if got := b.Next(); got != d*time.Second {
			t.Fatalf("delay %v, want %v", got, d*time.Second)
		}
	}
}

func TestAttachAndVersionRefusal(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "too_old"}[old], func(t *testing.T) {
			path := socket(t)
			version := "0.9.5"
			if old {
				version = "0.8.2"
			}
			s := server(t, path, fakeherdr.Options{Version: version, Plugins: []map[string]any{{"plugin_id": "sample", "name": "Sample", "version": "1.0.0", "manifest_path": "plugin.toml", "plugin_root": "plugin", "enabled": true}}})
			session, err := client(path, newClock()).Attach(t.Context(), "0.9.0")
			if old {
				named(t, err, "herdr_too_old")
				if len(s.Requests()) != 1 {
					t.Fatal("version refusal sent requests after ping")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if session.Version != version || !strings.Contains(string(session.Plugins), `"enabled":true`) {
				t.Fatalf("session = %+v", session)
			}
			methods := []string{}
			for _, r := range s.Requests() {
				methods = append(methods, r.Method)
			}
			if !reflect.DeepEqual(methods, []string{"ping", "plugin.list"}) {
				t.Fatal(methods)
			}
		})
	}
}

func TestSlowPingDeadline(t *testing.T) {
	path := socket(t)
	clock := newClock()
	seen := make(chan struct{})
	gate := make(chan struct{})
	server(t, path, fakeherdr.Options{PingGate: gate, OnRequest: func(r fakeherdr.Request) {
		if r.Method == "ping" {
			close(seen)
		}
	}})
	done := make(chan error, 1)
	go func() { _, err := client(path, clock).Ping(t.Context()); done <- err }()
	<-seen
	clock.take(t, 2*time.Second).fire()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestInjectedDialerAndUnavailable(t *testing.T) {
	calls := 0
	c := upstream.New(upstream.Options{Socket: "chosen", Clock: newClock(), Dial: func(_ context.Context, network, path string) (net.Conn, error) {
		calls++
		if network != "unix" || path != "chosen" {
			t.Errorf("dial %s %s", network, path)
		}
		return nil, errors.New("absent")
	}})
	_, err := c.Call(t.Context(), "pane.current", nil)
	e := named(t, err, "unavailable")
	if calls != 1 || e.Message != "herdr server unavailable" || e.Data["reason"] != "herdr_absent" {
		t.Fatalf("calls=%d, error=%+v", calls, e)
	}
}

func TestAbsentThenPresentAndReconnect(t *testing.T) {
	path := socket(t)
	clock := newClock()
	c := client(path, clock)
	statuses := make(chan string, 8)
	type outcome struct {
		session *upstream.Session
		err     error
	}
	wait := func() <-chan outcome {
		done := make(chan outcome, 1)
		go func() {
			s, e := c.WaitAttach(t.Context(), "0.9.0", func(v string) { statuses <- v })
			done <- outcome{s, e}
		}()
		return done
	}
	done := wait()
	if v := <-statuses; v != "absent" {
		t.Fatal(v)
	}
	timer := clock.take(t, 5*time.Second)
	first := server(t, path, fakeherdr.Options{})
	timer.fire()
	got := <-done
	if got.err != nil || got.session.Version != "0.9.5" {
		t.Fatalf("%+v", got)
	}
	if v := <-statuses; v != "attached" {
		t.Fatal(v)
	}
	stream, err := c.Subscribe(t.Context(), []string{"pane.focused"})
	if err != nil {
		t.Fatal(err)
	}
	first.DropSubscriptions()
	_, err = stream.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("drop error = %v", err)
	}
	first.Close()
	done = wait()
	if v := <-statuses; v != "absent" {
		t.Fatal(v)
	}
	timer = clock.take(t, 5*time.Second)
	second := server(t, path, fakeherdr.Options{Version: "0.10.0"})
	timer.fire()
	got = <-done
	if got.err != nil || got.session.Version != "0.10.0" {
		t.Fatalf("reattach = %+v", got)
	}
	if v := <-statuses; v != "attached" {
		t.Fatal(v)
	}
	records := second.Requests()
	if len(records) != 2 || records[1].Method != "plugin.list" {
		t.Fatal(records)
	}
	stream, err = c.Subscribe(t.Context(), []string{"pane.focused"})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
}

func TestWaitCancellationAndTerminalVersion(t *testing.T) {
	t.Run("cancel retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		clock := newClock()
		c := client(socket(t), clock)
		done := make(chan error, 1)
		absent := make(chan struct{})
		go func() {
			_, err := c.WaitAttach(ctx, "0.9.0", func(string) { close(absent) })
			done <- err
		}()
		<-absent // Ping has completed and stopped its timer before retry inspection.
		timer := clock.take(t, 5*time.Second)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		timer.mu.Lock()
		active := timer.active
		timer.mu.Unlock()
		if active {
			t.Fatal("retry timer not stopped")
		}
	})
	t.Run("old is terminal", func(t *testing.T) {
		path := socket(t)
		s := server(t, path, fakeherdr.Options{Version: "0.8.2"})
		var states []string
		_, err := client(path, newClock()).WaitAttach(t.Context(), "0.9.0", func(v string) { states = append(states, v) })
		named(t, err, "herdr_too_old")
		if !reflect.DeepEqual(states, []string{"too_old"}) || len(s.Requests()) != 1 {
			t.Fatalf("states=%v, requests=%v", states, s.Requests())
		}
	})
}

func TestEventsAndStreamErrors(t *testing.T) {
	for _, mode := range []string{"eof", "error reply", "defensive id-less error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			path := socket(t)
			s := server(t, path, fakeherdr.Options{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream, err := client(path, newClock()).Subscribe(ctx, []string{"pane.focused", "pane.focused"})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			req := s.Requests()[0]
			if len(req.Params["subscriptions"].([]any)) != 1 {
				t.Fatal("names not deduplicated")
			}
			if err = s.Emit("pane_focused", map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"}); err != nil {
				t.Fatal(err)
			}
			event, err := stream.Next()
			if err != nil || event.Event != "pane_focused" || !strings.Contains(string(event.Data), `"pane_id":"p"`) {
				t.Fatalf("event=%+v, err=%v", event, err)
			}
			switch mode {
			case "eof":
				s.DropSubscriptions()
			case "cancel":
				cancel()
			default:
				if err = s.FailSubscriptions("unavailable", "stream failed", mode == "error reply"); err != nil {
					t.Fatal(err)
				}
			}
			_, err = stream.Next()
			switch mode {
			case "eof":
				if !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			default:
				e := named(t, err, "unavailable")
				if e.Message != "stream failed" {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestPassthroughAndContextRecords(t *testing.T) {
	path := socket(t)
	s := server(t, path, fakeherdr.Options{Passthrough: []string{"custom.method"}})
	c := client(path, newClock())
	for _, tt := range []struct {
		method string
		params any
		kind   string
	}{
		{"pane.current", nil, "pane_current"}, {"workspace.get", map[string]string{"workspace_id": "workspace-1"}, "workspace_info"},
		{"tab.get", map[string]string{"tab_id": "tab-1"}, "tab_info"}, {"popup.close", nil, "ok"},
		{"notification.show", map[string]string{"title": "hello"}, "notification_show"},
		{"plugin.pane.open", map[string]string{"plugin_id": "external", "entrypoint": "main"}, "plugin_pane_opened"},
		{"plugin.pane.open", map[string]string{"plugin_id": "external", "entrypoint": "main", "placement": "popup"}, "ok"},
	} {
		raw, err := c.Call(t.Context(), tt.method, tt.params)
		if err != nil {
			t.Fatal(err)
		}
		var r map[string]any
		_ = json.Unmarshal(raw, &r)
		if r["type"] != tt.kind {
			t.Fatalf("%s = %s", tt.method, raw)
		}
	}
	conn, err := c.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	line := "{ \"id\" : \"vendor\", \"method\":\"custom.method\", \"params\":{\"extra\":7} }\n"
	if _, err = io.WriteString(conn, line); err != nil {
		t.Fatal(err)
	}
	var reply map[string]any
	if err = json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply["id"] != "vendor" {
		t.Fatal(reply)
	}
	records := s.Requests()
	if string(records[len(records)-1].Raw) != line {
		t.Fatal("passthrough bytes changed")
	}
	records[0].Params["mutated"] = true
	records[0].Raw[0] = '!'
	fresh := s.Requests()
	if fresh[0].Params["mutated"] != nil || fresh[0].Raw[0] != '{' {
		t.Fatal("recorder aliases its caller")
	}
}
