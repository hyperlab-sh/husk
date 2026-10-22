package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// probeModel runs a function on the program loop, the one goroutine allowed to
// read the model.
type probeModel struct {
	*WorkbenchModel
}

type probeMsg struct {
	fn   func(*WorkbenchModel)
	done chan struct{}
}

func (p probeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if pm, ok := msg.(probeMsg); ok {
		pm.fn(p.WorkbenchModel)
		close(pm.done)
		return p, nil
	}
	_, cmd := p.WorkbenchModel.Update(msg)
	return p, cmd
}

// fakeProcess stands in for a plugin process: Start succeeds at once and the
// test speaks for the plugin over the real socket with the process's token.
type fakeProcess struct {
	id      string
	started chan<- string
	done    chan struct{}
	once    sync.Once
}

func (p *fakeProcess) PluginID() string { return p.id }
func (p *fakeProcess) Token() string    { return "tok-" + p.id }
func (p *fakeProcess) Start([]protocol.Message) error {
	p.started <- p.id
	return nil
}
func (p *fakeProcess) Info() wrapper.Info {
	return wrapper.Info{PluginID: p.id, Status: wrapper.Live}
}
func (p *fakeProcess) Send(protocol.Message) error { return nil }
func (p *fakeProcess) MarkShutdown()               {}
func (p *fakeProcess) CloseStdin()                 { p.once.Do(func() { close(p.done) }) }
func (p *fakeProcess) Terminate() error            { p.CloseStdin(); return nil }
func (p *fakeProcess) Kill() error                 { p.CloseStdin(); return nil }
func (p *fakeProcess) Done() <-chan struct{}       { return p.done }

// gateOrderHost records whether the post gate was already closed when Close ran.
type gateOrderHost struct {
	host.Looped
	prog        *programRef
	gateAtClose chan bool
}

func (h gateOrderHost) Close() error {
	h.gateAtClose <- h.prog.closed.Load()
	return h.Looped.Close()
}

// shortRuntimeDir is a private directory whose socket path fits the platform
// limit.
func shortRuntimeDir(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "r")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if instance.CheckSocketPath(instance.SocketPath(dir, id)) == nil {
		return dir
	}
	dir, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// The workbench over the real v2 host: a plugin goes live, its surface.update
// reaches the model, a resize writes husk.geometry, and q closes the post gate,
// then the host, then ends the program.
func TestWorkbenchOverTheRealHost(t *testing.T) {
	state0 := t.TempDir()
	t.Setenv("HOME", state0)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state0, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(state0, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(state0, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(state0, "run"))

	const id = "itest"
	dir := shortRuntimeDir(t, id)
	pluginDir := filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"manifest_version":2,"protocol":2,"id":"alpha","name":"alpha","version":"1","command":["./plugin"],"surfaces":[{"id":"sidebar-list"}]}`
	if err := os.WriteFile(filepath.Join(pluginDir, manifest.FileName), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	started := make(chan string, 4)
	factory := pods.Factory(func(spec manifest.Spec, _ wrapper.Options) (wrapper.Process, error) {
		return &fakeProcess{id: spec.ID, started: started, done: make(chan struct{})}, nil
	})

	model := goldenModel()
	prog := newProgramRef()
	p := tea.NewProgram(probeModel{model},
		tea.WithContext(t.Context()),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(goldenW, goldenH),
	)
	prog.set(p)
	h, err := newHost(prog, model, id, dir, []string{pluginDir}, nil, host.WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	gateAtClose := make(chan bool, 4)
	model.attach(gateOrderHost{Looped: h, prog: prog, gateAtClose: gateAtClose}, prog.closeGate)

	ended := make(chan error, 1)
	go func() { _, err := p.Run(); ended <- err }()
	t.Cleanup(func() { prog.closeGate(); p.Kill() })

	probe := func(fn func(*WorkbenchModel)) {
		t.Helper()
		pm := probeMsg{fn: fn, done: make(chan struct{})}
		p.Send(pm)
		select {
		case <-pm.done:
		case err := <-ended:
			t.Fatalf("program ended: %v", err)
		case <-t.Context().Done():
			t.Fatal("probe never ran")
		}
	}
	// await re-probes until cond holds; each probe is a round trip through the
	// loop, so the host's pending posts run in between.
	await := func(what string, cond func(*WorkbenchModel) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			ok := false
			probe(func(m *WorkbenchModel) { ok = cond(m) })
			if ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			runtime.Gosched()
		}
	}

	select {
	case got := <-started:
		if got != "alpha" {
			t.Fatalf("started %q", got)
		}
	case err := <-ended:
		t.Fatalf("program ended before the plugin started: %v", err)
	case <-t.Context().Done():
		t.Fatal("plugin never started")
	}
	await("alpha live", func(m *WorkbenchModel) bool { return m.plugins["alpha"] })

	// The plugin speaks over the real socket with its launch token.
	conn, err := net.Dial("unix", instance.SocketPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	params, _ := json.Marshal(protocol.SurfaceUpdateParams{Surface: protocol.SurfaceSidebarList, Title: "Alpha", Lines: []string{"hello from alpha"}})
	req, _ := json.Marshal(map[string]any{"id": "1", "token": "tok-alpha", "method": protocol.MethodSurfaceUpdate, "params": json.RawMessage(params)})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err := json.Unmarshal(line, &reply); err != nil || reply.Error != nil {
		t.Fatalf("surface.update reply %s (%v)", line, err)
	}
	await("alpha content", func(m *WorkbenchModel) bool {
		su := m.slotUpdates[protocol.SurfaceSidebarList]
		return su != nil && su.PluginID == "alpha" && len(su.Lines) == 1 && su.Lines[0] == "hello from alpha"
	})

	// A resize reaches State as husk.geometry.
	p.Send(tea.WindowSizeMsg{Width: 120, Height: 36})
	await("geometry for 120x36", func(m *WorkbenchModel) bool {
		e, ok := m.fe.Snapshot().Slices[state.GeometryNamespace]
		if !ok {
			return false
		}
		var g struct {
			Terminal struct{ Width, Height int } `json:"terminal"`
		}
		return json.Unmarshal(e.Value, &g) == nil && g.Terminal.Width == 120 && g.Terminal.Height == 36
	})

	// q: the gate closes before the host does, the program ends, nothing hangs.
	p.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	select {
	case gateClosed := <-gateAtClose:
		if !gateClosed {
			t.Error("host.Close ran before the post gate closed")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("q never closed the host")
	}
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("program: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("program did not end after q")
	}
	if prog.post(struct{}{}) {
		t.Error("post succeeded after shutdown")
	}
	if !model.closed {
		t.Error("model does not record the host as closed")
	}
}
