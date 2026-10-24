package shim

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/settings"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type testTimer struct {
	mu     sync.Mutex
	active bool
	d      time.Duration
	f      func()
}

func (t *testTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := t.active
	t.active = false
	return v
}
func (t *testTimer) fire() {
	t.mu.Lock()
	v := t.active
	t.active = false
	t.mu.Unlock()
	if v {
		t.f()
	}
}

type testClock struct{ timers chan *testTimer }

func (c *testClock) AfterFunc(d time.Duration, f func()) upstream.Timer {
	tm := &testTimer{active: true, d: d, f: f}
	c.timers <- tm
	return tm
}
func (c *testClock) fire(t *testing.T, d time.Duration) {
	t.Helper()
	c.next(t, d).fire()
}
func (c *testClock) next(t *testing.T, d time.Duration) *testTimer {
	t.Helper()
	for {
		tm := take(t, c.timers)
		tm.mu.Lock()
		active := tm.active
		tm.mu.Unlock()
		if active && tm.d == d {
			return tm
		}
	}
}
func take[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("test barrier timed out")
		var zero T
		return zero
	}
}

type hostRecord struct {
	method string
	params json.RawMessage
}
type testHost struct {
	events chan hostRecord
	invoke func(context.Context, any) (json.RawMessage, error)
}

func (h *testHost) Call(ctx context.Context, m string, p any) (json.RawMessage, error) {
	if m == protocol.MethodActionInvoke && h.invoke != nil {
		return h.invoke(ctx, p)
	}
	raw, _ := json.Marshal(p)
	select {
	case h.events <- hostRecord{m, raw}:
	case <-ctx.Done():
	}
	return json.RawMessage(`{}`), nil
}
func (h *testHost) Notify(ctx context.Context, m string, p any) error {
	raw, _ := json.Marshal(p)
	select {
	case h.events <- hostRecord{m, raw}:
	case <-ctx.Done():
	}
	return nil
}
func (h *testHost) until(t *testing.T, method, server string) json.RawMessage {
	t.Helper()
	for {
		v := take(t, h.events)
		if v.method != method {
			continue
		}
		if server != "" {
			var s struct {
				Value state `json:"value"`
			}
			_ = json.Unmarshal(v.params, &s)
			if s.Value.Server != server {
				continue
			}
		}
		return v.params
	}
}

type testProcess struct {
	pid       int
	done      chan int
	signals   chan syscall.Signal
	termStops bool
}

func (p *testProcess) PID() int            { return p.pid }
func (p *testProcess) Wait() (int, string) { return <-p.done, "last stderr" }
func (p *testProcess) Signal(s syscall.Signal) error {
	p.signals <- s
	if s == syscall.SIGKILL || p.termStops {
		select {
		case p.done <- 137:
		default:
		}
	}
	return nil
}

type spawnRecord struct {
	command Command
	process *testProcess
}
type rig struct {
	o      Options
	host   *testHost
	clock  *testClock
	server *fakeherdr.Server
	in     *io.PipeWriter
	reply  *json.Decoder
	exit   chan error
	spawns chan spawnRecord
	logs   *lockedBuffer
}
type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
func newRig(t *testing.T, policy string, enabled bool, extra string, configure ...func(*fakeherdr.Options)) *rig {
	t.Helper()
	dir := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "ha-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	options := fakeherdr.Options{Plugins: []map[string]any{{"plugin_id": "demo", "enabled": enabled}}, Pane: map[string]any{"pane_id": "p", "workspace_id": "w", "tab_id": "t", "cwd": "/project", "agent": "worker", "agent_status": "working"}, Workspace: map[string]any{"workspace_id": "w", "label": "Project"}, Tab: map[string]any{"tab_id": "t", "label": "Main"}, Passthrough: []string{"vendor.unknown"}}
	for _, f := range configure {
		f(&options)
	}
	srv, err := fakeherdr.Start(filepath.Join(socketDir, "u.sock"), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	text := "id='demo'\nname='Demo'\nversion='1'\nmin_herdr_version='0.9.0'\n[[actions]]\nid='go'\ntitle='Go'\ncommand=['./command','arg']\n[[panes]]\nid='panel'\ntitle='Panel'\ncommand=['./panel']\n" + extra
	if err = os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	h := &testHost{events: make(chan hostRecord, 1024)}
	clk := &testClock{timers: make(chan *testTimer, 4096)}
	r := &rig{host: h, clock: clk, server: srv, exit: make(chan error, 1), spawns: make(chan spawnRecord, 64), logs: &lockedBuffer{}}
	next := 0
	r.o = Options{Dir: dir, Binary: "/usr/local/herdr", Socket: filepath.Join(socketDir, "u.sock"), ConfigDir: filepath.Join(dir, "config"), StateDir: filepath.Join(dir, "state"), Env: []string{"KEEP=value", "HUSK_PLUGIN_TOKEN=secret", "HUSK_PLUGIN_ID=demo", "HUSK_PROTOCOL_VERSION=2.7", "HUSK_EXTRA=drop", "HUSK_SOCKET_PATH=/host.sock", "HUSK_INSTANCE=unit", "HUSK_BIN_PATH=/usr/local/husk", "HERDR_PLUGIN_EVENT=stale"}, Settings: settings.Config{Hooks: policy}, Host: h, Clock: clk, Now: func() time.Time { return time.Unix(100, 0) }, Platform: "linux", LogPath: "/logs/demo.log"}
	r.o.Spawn = func(c Command) (Process, error) {
		next++
		p := &testProcess{pid: next, done: make(chan int, 1), signals: make(chan syscall.Signal, 4), termStops: true}
		r.spawns <- spawnRecord{c, p}
		return p, nil
	}
	return r
}
func (r *rig) start(t *testing.T) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r.in = inW
	r.reply = json.NewDecoder(outR)
	go func() { r.exit <- Run(context.Background(), r.o, inR, outW, r.logs); _ = outW.Close() }()
	t.Cleanup(func() {
		_ = r.in.Close()
		select {
		case <-r.exit:
		case <-time.After(10 * time.Second):
			t.Error("shim failed to stop")
		}
		_ = outR.Close()
	})
}
func (r *rig) invoke(t *testing.T, action string) protocol.Message {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": "call", "method": "action.invoke", "params": map[string]any{"action": action, "caller": map[string]any{"kind": "cli"}, "focus": map[string]any{"surface": "main-stage", "owner": "other"}}})
	_, err := r.in.Write(append(raw, '\n'))
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err = r.reply.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func proxyCall(t *testing.T, path, line string) map[string]any {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = c.Write([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.NewDecoder(c).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
