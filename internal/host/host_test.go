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
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
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
	state := t.TempDir()
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
	return nil
}
func (p *fakeProc) MarkShutdown()         {}
func (p *fakeProc) CloseStdin()           { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) Terminate() error      { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProc) Kill() error           { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
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
	mid := protocol.NewStringID(id)
	raw, err := json.Marshal(protocol.Message{ID: &mid, Method: method})
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
	// And every inbound method reaches an adapter: an unreachable caller kind
	// is refused by Admit (denied), never by the Mux as unknown_method.
	for method, home := range contracts.MethodHomes() {
		if home.Port == contracts.Outbound || home.Port == contracts.Unrouted {
			continue
		}
		id := protocol.NewStringID("x")
		reply := h.root.mux.Serve(t.Context(), contracts.Caller{Kind: contracts.CallerKind(99)}, protocol.Message{ID: &id, Method: method}, nil)
		if reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
			t.Errorf("%s: reply = %+v, want denied by its adapter", method, reply.Error)
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

func TestStartupFailureClosesWhatStarted(t *testing.T) {
	stages := []string{"manifests", "store", "state", "services", "stem", "pods", "server"}
	for i, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			var steps []string
			hook, _ := readyHook(stage)
			h, err := boot(t, nil, withHook(hook), withClose(func(s string) { steps = append(steps, s) }))
			if err == nil || !strings.Contains(err.Error(), "injected "+stage) {
				t.Fatalf("New err = %v", err)
			}
			if h.root != nil {
				t.Fatal("a failed New must not hand out a host")
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
		if !slices.Equal(steps, []string{"calls", "server", "pods", "store"}) {
			t.Fatalf("steps = %v", steps)
		}
		if left := h.leftovers(); len(left) != 0 {
			t.Fatalf("files left: %v", left)
		}
	})
	t.Run("launch fails after the server listens", func(t *testing.T) {
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
	h.root.cfg.Post(pods.TeardownMsg{PluginID: "plain"})
	h.loop(func() {
		if calls != 1 {
			t.Errorf("calls after unrelated teardown = %d, want 1", calls)
		}
	})
	h.root.cfg.Post(pods.TeardownMsg{PluginID: "pub"})
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
			t.Fatalf("precondition: occupants = %+v", res.Occupants)
		}
	})
	h.root.cfg.Post(pods.TeardownMsg{PluginID: "alpha"})
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
