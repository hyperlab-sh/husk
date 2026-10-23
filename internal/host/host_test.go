package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/goleak"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// ---- harness ----

// runtimeDir returns a private runtime directory whose socket path fits the
// platform limit, under t.TempDir when that is short enough.
func runtimeDir(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "r")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if instance.CheckSocketPath(instance.SocketPath(dir, id)) != nil {
		dir, err := os.MkdirTemp("/tmp", "hh")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	return dir
}

type harness struct {
	t     *testing.T
	root  *Root
	dir   string
	id    string
	in    chan any
	stop  chan struct{}
	ended chan struct{}
	once  sync.Once
}

// boot builds a host over a real runtime directory with a loop goroutine
// draining cfg.Post into Update.
func boot(t *testing.T, mutate func(*contracts.Config), opts ...Option) (*harness, error) {
	t.Helper()
	return bootIn(t, t.TempDir(), mutate, opts...)
}

// bootIn is boot over a given state directory, so a second host can start on
// what the first one persisted.
func bootIn(t *testing.T, state string, mutate func(*contracts.Config), opts ...Option) (*harness, error) {
	t.Helper()
	t.Setenv("HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(state, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(state, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(state, "run"))
	h := &harness{t: t, id: "t" + fmt.Sprint(os.Getpid()), in: make(chan any, 4096), stop: make(chan struct{}), ended: make(chan struct{})}
	h.dir = runtimeDir(t, h.id)
	cfg := contracts.Config{InstanceID: h.id, RuntimeDir: h.dir, Post: h.post}
	if mutate != nil {
		mutate(&cfg)
	}
	host, err := New(cfg, opts...)
	if err != nil {
		close(h.ended)
		return h, err
	}
	h.root = host.(*Root)
	go func() {
		defer close(h.ended)
		for {
			select {
			case m := <-h.in:
				h.root.Update(m)
			case <-h.stop:
				return
			}
		}
	}()
	t.Cleanup(h.shutdown)
	return h, nil
}

func (h *harness) post(m any) bool {
	select {
	case <-h.stop:
		return false
	default:
	}
	select {
	case h.in <- m:
		return true
	case <-h.stop:
		return false
	}
}

func (h *harness) shutdown() {
	h.once.Do(func() {
		if h.root != nil {
			_ = h.root.Close()
		}
		close(h.stop)
		<-h.ended
	})
}

// loop runs fn on the host loop and waits for it.
func (h *harness) loop(fn func()) {
	h.t.Helper()
	if err := h.root.do(h.t.Context(), fn); err != nil {
		h.t.Fatal(err)
	}
}

// remove runs plugin.remove through the Pods port as the user's CLI would and
// returns once the reply is ready.
func (h *harness) remove(id string) (contracts.PluginRemoval, error) {
	h.t.Helper()
	return h.root.pods.Remove(h.t.Context(), contracts.Caller{Kind: contracts.CallerCLI}, id)
}

func (h *harness) mustRemove(id string) contracts.PluginRemoval {
	h.t.Helper()
	res, err := h.remove(id)
	if err != nil {
		h.t.Fatalf("plugin.remove %s: %v", id, err)
	}
	return res
}

func (h *harness) leftovers() []string {
	var out []string
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), h.id) {
			out = append(out, e.Name())
		}
	}
	return out
}

// runHost starts Run in a goroutine and returns once the Server listens (the
// run hook fires after Server.Start) with a cancel and Run's result channel.
func runHost(t *testing.T, ready <-chan struct{}, h *harness) (cancel func(), result <-chan error) {
	t.Helper()
	c, cancelFn := context.WithCancel(t.Context())
	t.Cleanup(cancelFn)
	res := make(chan error, 1)
	go func() { res <- h.root.Run(c) }()
	select {
	case <-ready:
	case err := <-res:
		t.Fatalf("Run ended before listening: %v", err)
	}
	return cancelFn, res
}

// ---- fake plugin processes ----

type fakes struct {
	mu      sync.Mutex
	procs   map[string]*fakeProc
	started chan string
	onSend  func(p *fakeProc, m protocol.Message) // set before the host runs
}

func newFakes() *fakes { return &fakes{procs: map[string]*fakeProc{}, started: make(chan string, 16)} }

func (f *fakes) factory(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
	p := &fakeProc{id: spec.ID, opts: o, done: make(chan struct{}), f: f}
	f.mu.Lock()
	f.procs[spec.ID] = p
	f.mu.Unlock()
	return p, nil
}

func (f *fakes) proc(id string) *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[id]
}

type fakeProc struct {
	id      string
	opts    wrapper.Options
	f       *fakes
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	opening []protocol.Message
	sent    []protocol.Message
	info    wrapper.Info
}

func (p *fakeProc) PluginID() string { return p.id }
func (p *fakeProc) Token() string    { return "tok-" + p.id }
func (p *fakeProc) Start(open []protocol.Message) error {
	p.mu.Lock()
	p.opening = open
	p.info = wrapper.Info{PluginID: p.id, Status: wrapper.Live}
	p.mu.Unlock()
	p.f.started <- p.id
	return nil
}
func (p *fakeProc) Info() wrapper.Info { p.mu.Lock(); defer p.mu.Unlock(); return p.info }
func (p *fakeProc) Send(m protocol.Message) error {
	p.mu.Lock()
	p.sent = append(p.sent, m)
	p.mu.Unlock()
	if p.f.onSend != nil {
		p.f.onSend(p, m)
	}
	return nil
}
func (p *fakeProc) MarkShutdown()         {}
func (p *fakeProc) FailVersionMismatch()  {}
func (p *fakeProc) CloseStdin()           { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) Terminate() error      { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProc) Kill() error           { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProc) Done() <-chan struct{} { return p.done }

// openingParams decodes the params of the opening message with method into v.
func (p *fakeProc) openingParams(t *testing.T, method string, v any) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.opening {
		if m.Method == method {
			if err := json.Unmarshal(m.Params, v); err != nil {
				t.Fatalf("%s params: %v", method, err)
			}
			return
		}
	}
	t.Fatalf("opening has no %s", method)
}

// sentResized decodes every surface.resized the Pod was sent after its
// opening, in order.
func (p *fakeProc) sentResized(t *testing.T) [][]protocol.SurfaceSize {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]protocol.SurfaceSize
	for _, m := range p.sent {
		if m.Method != protocol.MethodSurfaceResized {
			continue
		}
		var params protocol.SurfaceResizedParams
		if err := json.Unmarshal(m.Params, &params); err != nil {
			t.Fatal(err)
		}
		out = append(out, params.Surfaces)
	}
	return out
}

func (p *fakeProc) openingMethods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, m := range p.opening {
		out = append(out, m.Method)
	}
	return out
}

// writePlugin writes a v2 manifest for plugin id claiming sidebar-list and
// publishing one slot when slot is true.
func writePlugin(t *testing.T, id string, slot bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"manifest_version": 2, "protocol": 2, "id": id, "name": id, "version": "1",
		"command":  []string{"./plugin"},
		"surfaces": []any{map[string]any{"id": "sidebar-list"}},
	}
	if slot {
		doc["published_slots"] = []any{map[string]any{
			"id": id + ".toolbar", "kind": "region",
			"rect": map[string]any{"surface": "sidebar-list", "x": 0, "y": 0, "width": 2, "height": 1},
		}}
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ---- wire helpers ----

type wireConn struct {
	c net.Conn
	r *bufio.Reader
}

func dial(t *testing.T, path string) wireConn {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return wireConn{c, bufio.NewReader(c)}
}

func (w wireConn) call(t *testing.T, id, method string) protocol.Message {
	t.Helper()
	return w.callParams(t, id, method, nil)
}

// callParams is call with a params object.
func (w wireConn) callParams(t *testing.T, id, method string, params any) protocol.Message {
	t.Helper()
	mid := protocol.NewStringID(id)
	msg := protocol.Message{ID: &mid, Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		msg.Params = p
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.c.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := w.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return m
}

// readyHook returns a stage hook that closes ready once Server.Start ran (the
// run stage), and fails the named stage.
func readyHook(failAt string) (func(string) error, <-chan struct{}) {
	ready := make(chan struct{})
	var once sync.Once
	return func(stage string) error {
		if stage == "run" {
			once.Do(func() { close(ready) })
		}
		if stage == failAt {
			return errors.New("injected " + stage + " failure")
		}
		return nil
	}, ready
}

func withHook(fn func(string) error) Option { return func(o *options) { o.afterStage = fn } }
func withClose(fn func(string)) Option      { return func(o *options) { o.onClose = fn } }
func withServer(fn func(server.Config) server.Server) Option {
	return func(o *options) { o.newServer = fn }
}

// ---- tests ----

func TestBootPingAndClose(t *testing.T) {
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook), WithVersion("9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	cancel, result := runHost(t, ready, h)
	defer cancel()

	sock := instance.SocketPath(h.dir, h.id)
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not listening: %v", err)
	}
	res := dial(t, sock).call(t, "1", protocol.MethodSessionPing)
	if res.Error != nil {
		t.Fatalf("ping error: %+v", res.Error)
	}
	var ping protocol.SessionPingResult
	if err := json.Unmarshal(res.Result, &ping); err != nil {
		t.Fatal(err)
	}
	if ping.ID != h.id || ping.HostVersion != "9.9.9" || ping.ProtocolVersion != protocol.Version || ping.PID != os.Getpid() {
		t.Fatalf("ping = %+v", ping)
	}
	// Registered with internal/instance.
	if left := h.leftovers(); len(left) < 2 {
		t.Fatalf("expected socket and entry files, got %v", left)
	}

	if err := h.root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Fatalf("files left after Close: %v", left)
	}
	if _, err := net.Dial("unix", sock); err == nil {
		t.Fatal("socket still accepts after Close")
	}
}

func TestEveryInboundMethodIsRegistered(t *testing.T) {
	var mu sync.Mutex
	var prefixes []string
	var inner server.Server
	h, err := boot(t, nil, withServer(func(c server.Config) server.Server {
		inner = server.New(c)
		return spyServer{Server: inner, handle: func(p string) { mu.Lock(); prefixes = append(prefixes, p); mu.Unlock() }}
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for method, home := range contracts.MethodHomes() {
		if home.Port == contracts.Outbound || home.Port == contracts.Unrouted {
			continue
		}
		prefix, _, _ := strings.Cut(method, ".")
		want[prefix] = true
	}
	// The Server answers session.ping from its own entry; every other
	// inbound prefix must be on the Server.
	got := map[string]bool{"session": true}
	for _, p := range prefixes {
		got[p] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("prefix %q is in MethodHomes but not registered on the Server", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("prefix %q registered but no inbound method has it", p)
		}
	}
	if len(prefixes) != len(got)-1 {
		t.Errorf("duplicate registrations: %v", prefixes)
	}
	// Admission: a caller kind no method allows is refused (denied) before any
	// adapter runs, for every inbound method.
	for method, home := range contracts.MethodHomes() {
		if home.Port == contracts.Outbound || home.Port == contracts.Unrouted {
			continue
		}
		id := protocol.NewStringID("x")
		reply := h.root.mux.Serve(t.Context(), contracts.Caller{Kind: "bogus"}, protocol.Message{ID: &id, Method: method}, nil)
		if reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
			t.Errorf("%s: reply = %+v, want denied for an unknown caller kind", method, reply.Error)
		}
	}
	// Reach: with an allowed caller kind admission passes, so the adapter's own
	// method switch runs. A method the adapter lacks a case for answers
	// unknown_method; any other outcome (success, invalid_params, not_found...)
	// proves the method reaches its adapter.
	for method, home := range contracts.MethodHomes() {
		if home.Port == contracts.Outbound || home.Port == contracts.Unrouted {
			continue
		}
		if len(home.Kinds) == 0 {
			t.Errorf("%s has no allowed caller kind", method)
			continue
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		id := protocol.NewStringID("x")
		caller := contracts.Caller{Kind: home.Kinds[0], PluginID: "probe", ConnID: 1}
		reply := h.root.mux.Serve(ctx, caller, protocol.Message{ID: &id, Method: method}, nil)
		cancel()
		if reply.Error != nil && reply.Error.Code == protocol.CodeUnknownMethod {
			t.Errorf("%s: the adapter answered unknown_method (%s): no case for it", method, reply.Error.Message)
		}
	}
}

type spyServer struct {
	server.Server
	handle func(string)
}

func (s spyServer) Handle(prefix string, h server.Handler) {
	s.handle(prefix)
	s.Server.Handle(prefix, h)
}

func TestCloseIsIdempotentAndOrdered(t *testing.T) {
	var mu sync.Mutex
	var steps []string
	hook, ready := readyHook("")
	f := newFakes()
	plug := writePlugin(t, "alpha", false)
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{plug} }, withHook(hook), WithFactory(f.factory),
		withClose(func(s string) { mu.Lock(); steps = append(steps, s); mu.Unlock() }))
	if err != nil {
		t.Fatal(err)
	}
	_, result := runHost(t, ready, h)
	<-f.started
	h.loop(func() {}) // Start returned on the loop: the launch is complete

	errs := make(chan error, 3)
	for range 3 {
		go func() { errs <- h.root.Close() }()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if err := h.root.Close(); err != nil {
		t.Fatal(err)
	}
	<-result
	want := []string{"calls", "server", "pods", "store"}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(steps, want) {
		t.Fatalf("shutdown steps = %v, want %v (each exactly once, in order)", steps, want)
	}
	if !f.proc("alpha").stopped() {
		t.Fatal("plugin process was not stopped")
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Fatalf("files left: %v", left)
	}
}

func (p *fakeProc) stopped() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// noLeaks fails the test if goroutines started after this call outlive it.
// Call it before boot: cleanups run last-in first-out, so the harness shuts
// the host down before the check.
func noLeaks(t *testing.T) {
	t.Helper()
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, opt) })
}

// storeStopped reports whether the Store's worker has exited.
func storeStopped(r *Root) bool {
	if r.store == nil {
		return true
	}
	select {
	case <-r.store.Done():
		return true
	default:
		return false
	}
}

func TestStartupFailureClosesWhatStarted(t *testing.T) {
	stages := []string{"manifests", "store", "state", "services", "stem", "pods", "server"}
	for i, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			noLeaks(t)
			var steps []string
			var failed *Root
			hook, _ := readyHook(stage)
			h, err := boot(t, nil, withHook(hook), withClose(func(s string) { steps = append(steps, s) }),
				func(o *options) { o.afterFail = func(r *Root) { failed = r } })
			if err == nil || !strings.Contains(err.Error(), "injected "+stage) {
				t.Fatalf("New err = %v", err)
			}
			if h.root != nil {
				t.Fatal("a failed New must not hand out a host")
			}
			if failed == nil || !storeStopped(failed) {
				t.Fatalf("the Store worker still runs after New failed at %s", stage)
			}
			storeBuilt := i >= 1
			if got := slices.Contains(steps, "store"); got != storeBuilt {
				t.Fatalf("store closed = %v after failing %s (steps %v)", got, stage, steps)
			}
			serverBuilt := stage == "server"
			if got := slices.Contains(steps, "server"); got != serverBuilt {
				t.Fatalf("server closed = %v after failing %s (steps %v)", got, stage, steps)
			}
			if left := h.leftovers(); len(left) != 0 {
				t.Fatalf("files left: %v", left)
			}
		})
	}
}

func TestBadExplicitPluginFailsNew(t *testing.T) {
	var steps []string
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{filepath.Join(t.TempDir(), "missing")} },
		withClose(func(s string) { steps = append(steps, s) }))
	if err == nil || !strings.Contains(err.Error(), "--plugin") {
		t.Fatalf("err = %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("nothing was built, yet %v closed", steps)
	}
	if left := h.leftovers(); len(left) != 0 {
		t.Fatal(left)
	}
}

type failStart struct {
	server.Server
	err error
}

func (f failStart) Start() error { return f.err }

func TestRunFailureAfterServerStartIsClosed(t *testing.T) {
	t.Run("server start fails", func(t *testing.T) {
		noLeaks(t)
		boom := errors.New("listen refused")
		var steps []string
		h, err := boot(t, nil, withClose(func(s string) { steps = append(steps, s) }),
			withServer(func(c server.Config) server.Server { return failStart{Server: server.New(c), err: boom} }))
		if err != nil {
			t.Fatal(err)
		}
		runErr := h.root.Run(t.Context())
		if !errors.Is(runErr, boom) {
			t.Fatalf("Run = %v", runErr)
		}
		if !storeStopped(h.root) {
			t.Fatal("the Store worker still runs after Run failed")
		}
		h.shutdown()
		if !slices.Equal(steps, []string{"calls", "server", "pods", "store"}) {
			t.Fatalf("steps = %v", steps)
		}
		if left := h.leftovers(); len(left) != 0 {
			t.Fatalf("files left: %v", left)
		}
	})
	t.Run("launch fails after the server listens", func(t *testing.T) {
		noLeaks(t)
		hook, _ := readyHook("run")
		h, err := boot(t, nil, withHook(hook))
		if err != nil {
			t.Fatal(err)
		}
		sock := instance.SocketPath(h.dir, h.id)
		runErr := h.root.Run(t.Context())
		if runErr == nil || !strings.Contains(runErr.Error(), "injected run") {
			t.Fatalf("Run = %v", runErr)
		}
		if !storeStopped(h.root) {
			t.Fatal("the Store worker still runs after Run failed")
		}
		h.shutdown()
		if left := h.leftovers(); len(left) != 0 {
			t.Fatalf("files left: %v", left)
		}
		if _, err := net.Dial("unix", sock); err == nil {
			t.Fatal("listener leaked")
		}
	})
}

func (h *harness) podStatus(id string) string {
	var status string
	h.loop(func() {
		e, ok := h.root.st.Snapshot().Slices[state.PodsNamespace]
		if !ok {
			return
		}
		var doc struct {
			Plugins map[string]struct {
				Status string `json:"status"`
			} `json:"plugins"`
		}
		_ = json.Unmarshal(e.Value, &doc)
		status = doc.Plugins[id].Status
	})
	return status
}

func TestLaunchGoesLiveWithOpening(t *testing.T) {
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{writePlugin(t, "alpha", false)} }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	if id := <-f.started; id != "alpha" {
		t.Fatal(id)
	}
	if got := f.proc("alpha").openingMethods(); !slices.Equal(got, []string{protocol.MethodThemeChanged, protocol.MethodSurfaceResized, protocol.MethodInputKeyState}) {
		t.Fatalf("opening = %v", got)
	}
	if got := h.podStatus("alpha"); got != "live" {
		t.Fatalf("husk.pods status = %q", got)
	}
	// Host-owned opening content: a claim holds a surface only once the Pod
	// is live (D70), so the opening lists none and the claimed surface
	// arrives as a surface.resized push right after; the key state is the
	// all-up default.
	var resized protocol.SurfaceResizedParams
	f.proc("alpha").openingParams(t, protocol.MethodSurfaceResized, &resized)
	if len(resized.Surfaces) != 0 {
		t.Fatalf("opening surface.resized = %+v, want none before the Pod is live", resized.Surfaces)
	}
	h.loop(func() {})
	if got := f.proc("alpha").sentResized(t); len(got) != 1 || len(got[0]) != 1 || got[0][0].Surface != "sidebar-list" {
		t.Fatalf("pushed surface.resized = %+v, want one listing sidebar-list", got)
	}
	var keys protocol.KeyStateParams
	f.proc("alpha").openingParams(t, protocol.MethodInputKeyState, &keys)
	if keys.Phase != protocol.KeyPhaseSync || keys.Modifiers != (protocol.ModifierState{}) {
		t.Fatalf("input.key_state = %+v, want sync with all modifiers up", keys)
	}
	// The plugin's manifest claim reached husk.surfaces through the
	// occupants declaration slice registered before the first reload.
	h.loop(func() {
		res, ok := decodeSurfaces(h.root.st.Snapshot())
		if !ok || res.Occupants["sidebar-list"].Plugin != "alpha" {
			t.Errorf("occupants = %+v", res.Occupants)
		}
	})
}

func TestPublishedSlotCatalogChangeCallsCatalogChanged(t *testing.T) {
	hook, ready := readyHook("")
	f := newFakes()
	var calls int
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = []string{writePlugin(t, "plain", false), writePlugin(t, "pub", true)}
	}, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		orig := h.root.catalog.onSlotsChanged
		h.root.catalog.onSlotsChanged = func() { calls++; orig() }
	})
	runHost(t, ready, h)
	<-f.started
	<-f.started
	h.loop(func() {
		if calls != 1 {
			t.Errorf("CatalogChanged calls after launch = %d, want 1", calls)
		}
		if p, ok := h.root.catalog.Owner("pub.toolbar"); !ok || p != "pub" {
			t.Errorf("owner = %q %v", p, ok)
		}
	})
	// Tearing the publisher down changes the catalog again; tearing down a
	// plugin that published nothing does not.
	h.mustRemove("plain")
	h.loop(func() {
		if calls != 1 {
			t.Errorf("calls after unrelated teardown = %d, want 1", calls)
		}
	})
	h.mustRemove("pub")
	h.loop(func() {
		if calls != 2 {
			t.Errorf("calls after publisher teardown = %d, want 2", calls)
		}
		if _, ok := h.root.catalog.Owner("pub.toolbar"); ok {
			t.Error("slot still owned after teardown")
		}
	})
}

func TestTeardownDropsOccupantsAndDeclarations(t *testing.T) {
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{writePlugin(t, "alpha", false)} }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	h.loop(func() {
		if res, _ := decodeSurfaces(h.root.st.Snapshot()); res.Occupants["sidebar-list"].Plugin != "alpha" {
			t.Errorf("precondition: occupants = %+v", res.Occupants)
		}
	})
	h.mustRemove("alpha")
	h.loop(func() {
		res, _ := decodeSurfaces(h.root.st.Snapshot())
		if occ, ok := res.Occupants["sidebar-list"]; ok {
			t.Errorf("removed owner's occupant survived: %+v", occ)
		}
		if h.root.decl.occ["alpha"] != nil || h.root.decl.con["alpha"] != nil {
			t.Error("declaration slices survived teardown")
		}
		if _, ok := h.root.store.Declared(store.DeclaredPlugins)["alpha"]; ok {
			t.Error("catalog document survived teardown")
		}
	})
}

func TestOpeningKeyStatePassesModifiersThrough(t *testing.T) {
	hook, ready := readyHook("")
	f := newFakes()
	want := protocol.ModifierState{Alt: true, Shift: true}
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{writePlugin(t, "alpha", false)} },
		withHook(hook), WithFactory(f.factory), WithModifiers(func() protocol.ModifierState { return want }))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	var keys protocol.KeyStateParams
	f.proc("alpha").openingParams(t, protocol.MethodInputKeyState, &keys)
	if keys.Phase != protocol.KeyPhaseSync || keys.Modifiers != want {
		t.Fatalf("input.key_state = %+v, want sync with %+v", keys, want)
	}
}

func TestSpawnRefusedOnceClosing(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.root.Close(); err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{}, 1)
	h.root.spawn(func() tea.Msg { ran <- struct{}{}; return nil })
	h.root.wg.Wait()
	select {
	case <-ran:
		t.Fatal("a worker was spawned after shutdown began")
	default:
	}
}

// A TeardownMsg handled after the shutdown mark is ignored: it starts no
// reload worker, so it cannot Add to the worker group. Shutdown is parked in
// the "pods" hook, before wg.Wait starts, with a held worker keeping wg above
// zero; one TeardownMsg for a live Pod is then delivered through Update.
func TestTeardownAfterShutdownMarkStartsNoWorker(t *testing.T) {
	hook, ready := readyHook("")
	f := newFakes()
	var mu sync.Mutex
	var accepted []bool
	atPods := make(chan struct{})
	proceed := make(chan struct{})
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{writePlugin(t, "alpha", false)} },
		withHook(hook), WithFactory(f.factory),
		withClose(func(step string) {
			if step == "pods" { // plugins stopped; parked here until the Teardown is delivered
				close(atPods)
				<-proceed
			}
		}),
		func(o *options) {
			o.onSpawn = func(ok bool) { mu.Lock(); accepted = append(accepted, ok); mu.Unlock() }
		})
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	hold := make(chan struct{})
	held := make(chan struct{})
	h.root.spawn(func() tea.Msg { close(held); <-hold; return nil })
	<-held
	closed := make(chan error, 1)
	go func() { closed <- h.root.Close() }()
	<-atPods // shutdown has begun (closing set) and the held worker keeps wg above zero
	mu.Lock()
	before := len(accepted)
	mu.Unlock()
	h.loop(func() {
		if _, ok := h.root.pods.Pod("alpha"); !ok {
			t.Error("precondition: alpha has no Pod")
		}
		h.root.Update(pods.TeardownMsg{PluginID: "alpha"})
	})
	mu.Lock()
	got := append([]bool(nil), accepted[before:]...)
	mu.Unlock()
	close(proceed)
	if len(got) != 0 {
		t.Errorf("spawn attempts after the shutdown mark = %v, want none", got)
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v while a worker was still held", err)
	default:
	}
	close(hold)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// postGate is a Config.Post that blocks marked messages until the gate closes,
// like a loop owner's post blocked on a full channel, and refuses everything
// once the gate is closed.
type postGate struct {
	gate chan struct{}
	next func(any) bool
}

type blockedPost struct{}

func (g *postGate) post(m any) bool {
	if _, ok := m.(blockedPost); ok {
		<-g.gate
		return false
	}
	select {
	case <-g.gate:
		return false
	default:
	}
	return g.next(m)
}

// bootWithWorker boots a host with a running plugin and a worker in flight
// whose last act is a Post that blocks until the gate closes.
func bootWithWorker(t *testing.T, g *postGate, workerDone *atomic.Bool, steps *[]string, opts ...Option) *harness {
	t.Helper()
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = []string{writePlugin(t, "alpha", false)}
		g.next = c.Post
		c.Post = g.post
	}, append(opts, withHook(hook), WithFactory(f.factory), withClose(func(s string) { *steps = append(*steps, s) }))...)
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	inWorker := make(chan struct{})
	h.root.spawn(func() tea.Msg {
		close(inWorker)
		h.root.cfg.Post(blockedPost{})
		workerDone.Store(true)
		return nil
	})
	<-inWorker
	return h
}

// Close from Update with a plugin running and a worker in flight: once the
// loop owner closes the post gate (the documented precondition) the worker's
// Post refuses, the worker ends and Close completes on the loop.
func TestCloseFromUpdateWithLiveWorkerAfterGateClosed(t *testing.T) {
	g := &postGate{gate: make(chan struct{})}
	var workerDone atomic.Bool
	var steps []string
	h := bootWithWorker(t, g, &workerDone, &steps)
	closed := make(chan error, 1)
	g.next(fnMsg{fn: func() {
		close(g.gate)
		closed <- h.root.Close()
	}})
	if err := <-closed; err != nil {
		t.Fatalf("Close from Update: %v", err)
	}
	if !workerDone.Load() {
		t.Error("Close returned before the in-flight worker finished")
	}
	if want := []string{"calls", "server", "pods", "store"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
}

// A Post that blocks past shutdown must not hang Close: the wait for workers
// is bounded, Close counts the stuck workers, and the Store is left open.
func TestCloseFromUpdateBoundsTheWaitOnABlockedPost(t *testing.T) {
	g := &postGate{gate: make(chan struct{})}
	var workerDone atomic.Bool
	var steps []string
	h := bootWithWorker(t, g, &workerDone, &steps, func(o *options) { o.workerWait = 20 * time.Millisecond })
	closed := make(chan error, 1)
	g.next(fnMsg{fn: func() { closed <- h.root.Close() }})
	err := <-closed
	if err == nil || !strings.Contains(err.Error(), "workers still running") {
		t.Fatalf("Close = %v, want a workers-still-running error", err)
	}
	if workerDone.Load() {
		t.Error("worker finished although its Post was still blocked")
	}
	if !strings.Contains(err.Error(), "1 workers still running") {
		t.Errorf("Close = %v, want the stuck-worker count", err)
	}
	if want := []string{"calls", "server", "pods"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v (no store close under a live worker)", steps, want)
	}
	close(g.gate) // release the worker so the test leaks nothing
}

func TestTeardownOfUnknownPodLeavesDeclarations(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		h.root.store.Declare(store.DeclaredPlugins, "ghost", store.Doc{"id": "ghost"})
		h.root.Update(pods.TeardownMsg{PluginID: "ghost"})
		if _, ok := h.root.store.Declared(store.DeclaredPlugins)["ghost"]; !ok {
			t.Error("teardown of an id with no Pod undeclared it")
		}
	})
}

func TestCloseFromUpdateRunsOnTheLoop(t *testing.T) {
	var steps []string
	h, err := boot(t, nil, withClose(func(s string) { steps = append(steps, s) }),
		func(o *options) { o.loopWait = 20 * time.Millisecond })
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	h.root.cfg.Post(fnMsg{fn: func() { closed <- h.root.Close() }})
	if err := <-closed; err != nil {
		t.Fatalf("Close from Update: %v", err)
	}
	if want := []string{"calls", "server", "pods", "store"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
}

func TestCloseDoesNotRunLoopWorkInlineOnAWedgedLoop(t *testing.T) {
	h, err := boot(t, nil, func(o *options) { o.loopWait = 20 * time.Millisecond })
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	h.root.cfg.Post(fnMsg{fn: func() { close(entered); <-release }})
	<-entered
	err = h.root.Close()
	close(release)
	if err == nil || !strings.Contains(err.Error(), "wedged") {
		t.Fatalf("Close = %v, want a wedged-loop error", err)
	}
	if strings.Contains(err.Error(), "final state flush") {
		t.Fatalf("Close waited on the wedged loop a second time: %v", err)
	}
}

// manualClock is a protocol.Clock whose timers fire only when the test says.
type manualClock struct {
	now     time.Time
	pending []*manualTimer
}

type manualTimer struct {
	fn      func()
	stopped bool
}

func (t *manualTimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

func (c *manualClock) Now() time.Time { return c.now }
func (c *manualClock) AfterFunc(_ time.Duration, f func()) protocol.Timer {
	t := &manualTimer{fn: f}
	c.pending = append(c.pending, t)
	return t
}

func TestStoreClockAdaptsAnInjectedClock(t *testing.T) {
	mc := &manualClock{now: time.Unix(1000, 0)}
	sc := storeClock{mc}
	if !sc.Now().Equal(mc.now) {
		t.Fatalf("Now = %v, want the injected clock's", sc.Now())
	}
	timer := sc.NewTimer(time.Hour)
	select {
	case <-timer.C():
		t.Fatal("timer fired before the injected clock did")
	default:
	}
	mc.now = mc.now.Add(time.Hour)
	mc.pending[0].fn()
	select {
	case at := <-timer.C():
		if !at.Equal(mc.now) {
			t.Fatalf("fired at %v, want %v", at, mc.now)
		}
	default:
		t.Fatal("timer did not deliver after the injected clock fired it")
	}
	stopped := sc.NewTimer(time.Hour)
	if !stopped.Stop() || !mc.pending[1].stopped {
		t.Fatal("Stop did not reach the injected timer")
	}
}

// Planned counts the plugins the arguments resolved to, not the arguments: an
// empty --plugin-dir plans none and a good one plans its plugins.
func TestPlannedCountsResolvedPlugins(t *testing.T) {
	empty := t.TempDir()
	h, err := boot(t, func(c *contracts.Config) { c.PluginDirs = []string{empty} })
	if err != nil {
		t.Fatal(err)
	}
	if got := h.root.Planned(); got != 0 {
		t.Fatalf("empty dir planned %d plugins", got)
	}
	one := filepath.Dir(writePlugin(t, "alpha", false))
	h2, err := boot(t, func(c *contracts.Config) { c.PluginDirs = []string{one} })
	if err != nil {
		t.Fatal(err)
	}
	if got := h2.root.Planned(); got != 1 {
		t.Fatalf("one-plugin dir planned %d plugins", got)
	}
}

// A freshly built host reloads its services cleanly: the settings service
// already carries the built-in schema, so the host must not register it again
// (every key came back as a duplicate and the launch reload warned).
func TestFreshHostReloadsServicesWithoutProblems(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonPlugin)
	if err != nil {
		for _, r := range reports {
			t.Logf("%s applied=%v problems=%+v", r.Service, r.Applied, r.Problems)
		}
		t.Fatal(err)
	}
}
