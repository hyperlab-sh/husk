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
	return upstream.New(upstream.Options{Socket: path, Clock: clock, Dial: guardedDial(path)})
}
func guardedDial(allowed string) upstream.Dialer {
	return func(ctx context.Context, network, path string) (net.Conn, error) {
		if network != "unix" || path != allowed || !strings.HasPrefix(filepath.Dir(path), "/tmp/ha-") {
			return nil, os.ErrPermission
		}
		return (&net.Dialer{}).DialContext(ctx, network, path)
	}
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
		name   string
		opts   upstream.SocketOptions
		values map[string]string
		want   string
		bad    bool
	}{
		{"default", upstream.SocketOptions{Home: "/home/test"}, nil, "/home/test/.config/herdr/herdr.sock", false},
		{"config file ignored", upstream.SocketOptions{Home: "/home/test"}, map[string]string{"HERDR_CONFIG_PATH": "/tmp/config/custom.toml"}, "/home/test/.config/herdr/herdr.sock", false},
		{"xdg", upstream.SocketOptions{Home: "/home/test"}, map[string]string{"XDG_CONFIG_HOME": "/config"}, "/config/herdr/herdr.sock", false},
		{"xdg without home", upstream.SocketOptions{}, map[string]string{"XDG_CONFIG_HOME": "/config"}, "/config/herdr/herdr.sock", false},
		{"windows appdata", upstream.SocketOptions{Platform: "windows", Home: "/home/test"}, map[string]string{"APPDATA": "/roaming"}, "/roaming/herdr/herdr.sock", false},
		{"windows profile", upstream.SocketOptions{Platform: "windows", Home: "/home/test"}, map[string]string{"USERPROFILE": "/profile"}, "/profile/AppData/Roaming/herdr/herdr.sock", false},
		{"windows home environment", upstream.SocketOptions{Platform: "windows"}, map[string]string{"HOME": "/home/env"}, "/home/env/.config/herdr/herdr.sock", false},
		{"inherited", upstream.SocketOptions{SocketPath: "/tmp/inherited.sock"}, nil, "/tmp/inherited.sock", false},
		{"session wins", upstream.SocketOptions{Session: "review", SocketPath: "/tmp/ignored", Home: "/home/test"}, map[string]string{"XDG_CONFIG_HOME": "/config", "HERDR_CONFIG_PATH": "/tmp/config/custom.toml"}, "/config/herdr/sessions/review/herdr.sock", false},
		{"default session", upstream.SocketOptions{Session: "default", Home: "/home/test"}, nil, "/home/test/.config/herdr/herdr.sock", false},
		{"dot", upstream.SocketOptions{Session: "..", Home: "/home/test"}, nil, "", true},
		{"slash", upstream.SocketOptions{Session: "a/b", Home: "/home/test"}, nil, "", true},
		{"unicode", upstream.SocketOptions{Session: "é", Home: "/home/test"}, nil, "", true},
		{"no home", upstream.SocketOptions{}, nil, "", true},
		{"config file without home", upstream.SocketOptions{}, map[string]string{"HERDR_CONFIG_PATH": "/tmp/config/custom.toml"}, "", true},
		{"relative xdg", upstream.SocketOptions{Home: "/home/test"}, map[string]string{"XDG_CONFIG_HOME": "relative"}, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.Getenv = func(key string) string { return tt.values[key] }
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

func TestAttachAndVersionRefusal(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "too_old"}[old], func(t *testing.T) {
			path := socket(t)
			version := "0.9.5"
			if old {
				version = "0.8.2"
			}
			s := server(t, path, fakeherdr.Options{Version: version, Plugins: []map[string]any{{"plugin_id": "sample", "name": "Sample", "version": "1.0.0", "manifest_path": "plugin.toml", "plugin_root": "plugin", "enabled": true}}})
			got, err := client(path, newClock()).Check(t.Context(), "0.9.0")
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
			if got != version {
				t.Fatalf("version = %q", got)
			}
			methods := []string{}
			for _, r := range s.Requests() {
				methods = append(methods, r.Method)
			}
			if !reflect.DeepEqual(methods, []string{"ping"}) {
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
	if calls != 1 || e.Message != "herdr server unavailable; start herdr and try again" || e.Data["reason"] != "herdr_absent" {
		t.Fatalf("calls=%d, error=%+v", calls, e)
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
