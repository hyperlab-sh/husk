package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/cli"
	"github.com/hyperlab-sh/husk/internal/host"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// stringFlag is a repeatable string flag.
type stringFlag []string

func (m *stringFlag) String() string { return strings.Join(*m, ",") }

func (m *stringFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// repeatable registers a repeatable string flag and returns its pointer.
func repeatable(name, usage string) *stringFlag {
	f := &stringFlag{}
	flag.Var(f, name, usage)
	return f
}

func main() {
	// argv[1] decides the mode: a first argument that does not start with
	// "-" is a subcommand. The CLI installs no signal handler; a signal ends
	// it by the signal.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(cli.Main(context.Background(), os.Args[1:], cli.OSEnv()))
	}
	dumpFrame := flag.Bool("dump-frame", false, "Render one live frame after plugin sync and exit")
	demoFrame := flag.Bool("demo-frame", false, "Render one frame from a built-in demo plugin that declares a border+label+close frame and exit")
	railMode := flag.String("rail", "", "Activate rail mode on startup: modal | full-canvas | stage")
	holdAlt := flag.Bool("hold-alt", false, "Simulate holding Alt/Option key on startup")
	pluginPaths := repeatable("plugin", "Plugin: executable, plugin directory, or husk-plugin.json path (repeatable)")
	pluginDirs := repeatable("plugin-dir", "Load every immediate subdirectory containing a husk-plugin.json (repeatable)")
	demo := flag.String("demo", "", "In-process frame demo: pubslots (requires --dump-frame)")
	flag.Parse()
	if *demo != "" {
		if *demo != "pubslots" || !*dumpFrame {
			fmt.Fprintln(os.Stderr, "--demo supports pubslots with --dump-frame")
			os.Exit(2)
		}
		model := newWorkbenchModel()
		model.recomputeLayout()
		model.seedPubslotsDemo()
		fmt.Println(model.View().Content)
		return
	}

	model := newWorkbenchModel()
	if *holdAlt {
		model.altLatched = true
		model.modifiers.Alt = true
	}

	if *demoFrame {
		model.demoFrame = true
		model.setupDemoFrame(*railMode)
		fmt.Println(model.View().Content)
		return
	}

	// Writing to stderr while the TUI owns the terminal corrupts the screen
	// (and the alternate screen hides it), so in TUI mode host log lines,
	// including launch failures, are held and printed once the program
	// exits. Failures also show as toasts.
	var tuiLog bytes.Buffer
	if !*dumpFrame {
		model.logOut = &tuiLog
	} else {
		model.logOut = os.Stderr
	}

	runtimeDir, err := instance.RuntimeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: runtime directory: %v\n", err)
		os.Exit(1)
	}
	prog := newProgramRef()
	if *dumpFrame {
		// runDumpFrame builds the headless program; a host Post waits for it.
		h, err := newHost(prog, model, strconv.Itoa(os.Getpid()), runtimeDir, []string(*pluginPaths), []string(*pluginDirs))
		if err != nil {
			fmt.Fprintf(os.Stderr, "husk: %v\n", err)
			os.Exit(1)
		}
		model.attach(h, prog.closeGate)
		os.Exit(dumpFrameMain(model, h, prog, *railMode, os.Stdout, os.Stderr))
	}

	// The program exists before the host, so a Post from any host worker finds
	// it. The model attaches the host before the program runs.
	p := tea.NewProgram(model)
	prog.set(p)
	h, err := newHost(prog, model, strconv.Itoa(os.Getpid()), runtimeDir, []string(*pluginPaths), []string(*pluginDirs))
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: %v\n", err)
		os.Exit(1)
	}
	model.attach(h, prog.closeGate)
	_, err = p.Run()
	prog.closeGate()     // whatever ended the program, nothing is read from here on
	model.shutdownHost() // stop plugins before printing, so their exit lines are included
	_, _ = os.Stderr.Write(tuiLog.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk error: %v\n", err)
		os.Exit(1)
	}
	if model.hostErr != nil {
		fmt.Fprintf(os.Stderr, "husk: host stopped: %v\n", model.hostErr)
		os.Exit(1)
	}
}

// dumpFrameMain runs --dump-frame on an attached host and returns the process
// exit code. The frame is printed even on failure so the diagnosis shows next
// to it, but a failed run (no plugin went live, a timeout, a host that stopped)
// exits 1 so scripts and CI see it.
func dumpFrameMain(model *WorkbenchModel, h host.Looped, prog *programRef, railMode string, stdout, stderr io.Writer) int {
	frame, err := runDumpFrame(model, dumpFrameOptions{
		Launched: h.Planned(),
		RailMode: railMode,
		Program:  prog,
	})
	prog.closeGate()
	model.shutdownHost()
	fmt.Fprintln(stdout, frame)
	if err != nil {
		fmt.Fprintf(stderr, "husk: --dump-frame: %v\n", err)
		return 1
	}
	return 0
}

// programRef lets the host's Post reach the tea.Program. The host is built
// around Post, so a worker may post before the program is set: post waits for
// set or for the gate. Once the gate closes (the program stopped reading, or
// shutdown began) post returns false without blocking, which Config.Post
// defines as "the loop is gone" (D62).
type programRef struct {
	mu     sync.RWMutex
	p      *tea.Program
	ready  chan struct{}
	gate   chan struct{}
	once   sync.Once
	closed atomic.Bool
	// waiting counts posts that reached the wait for set or the gate; tests
	// use it to know a post is parked without sleeping.
	waiting atomic.Int32
}

func newProgramRef() *programRef {
	return &programRef{ready: make(chan struct{}), gate: make(chan struct{})}
}

func (r *programRef) set(p *tea.Program) {
	r.mu.Lock()
	r.p = p
	r.mu.Unlock()
	select {
	case <-r.ready:
	default:
		close(r.ready)
	}
}

// closeGate is idempotent and safe from any goroutine.
func (r *programRef) closeGate() {
	r.closed.Store(true)
	r.once.Do(func() { close(r.gate) })
}

func (r *programRef) post(msg any) bool {
	if r.closed.Load() {
		return false
	}
	r.waiting.Add(1)
	defer r.waiting.Add(-1)
	select {
	case <-r.ready:
	case <-r.gate:
		return false
	}
	r.mu.RLock()
	p := r.p
	r.mu.RUnlock()
	p.Send(msg)
	return !r.closed.Load()
}

// newHost builds the v2 host over a private runtime directory. Nothing
// listens until the model's Init runs the host.
func newHost(prog *programRef, model *WorkbenchModel, id, dir string, plugins, dirs []string, opts ...host.Option) (host.Looped, error) {
	h, err := host.New(contracts.Config{
		InstanceID: id,
		RuntimeDir: dir,
		Entry: instance.Entry{
			ID:              id,
			PID:             os.Getpid(),
			Socket:          instance.SocketPath(dir, id),
			ProtocolVersion: protocol.Version,
		},
		Plugins:    plugins,
		PluginDirs: dirs,
		Post:       prog.post,
		Content:    model.content,
	}, append([]host.Option{host.WithLogger(func(level, msg string) { model.logf("%s: %s", level, msg) })}, opts...)...)
	if err != nil {
		return nil, err
	}
	looped, ok := h.(host.Looped)
	if !ok {
		return nil, fmt.Errorf("host does not run on a loop")
	}
	return looped, nil
}
