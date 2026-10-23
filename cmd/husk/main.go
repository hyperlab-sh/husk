package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/cli"
	"github.com/hyperlab-sh/husk/pkg/cli/createcmd"
	"github.com/hyperlab-sh/husk/pkg/cli/storecmd"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// version is the build version, set with -ldflags "-X main.version=...";
// empty falls back to the module version the toolchain recorded.
var version string

// buildVersion returns the version the entry and session.ping report, or ""
// when the build carries none (a "(devel)" build has none).
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	return pickVersion(version, bi, ok)
}

// pickVersion prefers the ldflags version, then the module version recorded
// in bi; a "(devel)" module version is no version.
func pickVersion(ldflags string, bi *debug.BuildInfo, ok bool) string {
	if ldflags != "" {
		return ldflags
	}
	if ok && bi != nil && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return ""
}

// flagSet reports whether the named flag was given on the command line.
func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// startInstanceID is the id of the instance start mode becomes: the --instance
// name when given (a given empty name is invalid, never the pid default), else
// the decimal pid.
func startInstanceID(name string, given bool, pid int) (string, error) {
	if !given {
		return strconv.Itoa(pid), nil
	}
	if err := instance.ValidName(name); err != nil {
		return "", err
	}
	return name, nil
}

// startProfile validates --profile before anything touches the filesystem. A
// given empty name is invalid, like an empty --instance. It returns nil when
// the flag was not given. The alphabet is Store's profile-directory alphabet,
// the only one the specs fix.
func startProfile(name string, given bool) (*string, error) {
	if !given {
		return nil, nil
	}
	if !store.ValidProfile(name) {
		return nil, fmt.Errorf("invalid profile name %q: use a-z, 0-9, _ and -", name)
	}
	return &name, nil
}

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

// bundleFlag normalizes a supplied path at parsing, preserving empty as disable.
type bundleFlag string

func (f *bundleFlag) String() string { return string(*f) }
func (f *bundleFlag) Set(value string) error {
	if value != "" {
		abs, err := filepath.Abs(value)
		if err != nil {
			return err
		}
		value = abs
	}
	*f = bundleFlag(value)
	return nil
}

// pluginInstanceDocs folds all launch flags into one instance document, leaving
// configured values alone unless their matching flag was explicitly supplied.
func pluginInstanceDocs(fs *flag.FlagSet) map[string]map[string]any {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["plugin"] && !given["plugin-dir"] && !given["bundle"] {
		return nil
	}
	plugins := map[string]any{}
	if given["plugin"] {
		plugins["paths"] = []string(*fs.Lookup("plugin").Value.(*stringFlag))
	}
	if given["plugin-dir"] {
		plugins["dirs"] = []string(*fs.Lookup("plugin-dir").Value.(*stringFlag))
	}
	husk := map[string]any{}
	if len(plugins) > 0 {
		husk["plugins"] = plugins
	}
	if given["bundle"] {
		husk["bundle"] = fs.Lookup("bundle").Value.String()
	}
	return map[string]map[string]any{"config": {"husk": husk}}
}

// pluginAdditions keeps additive flags separate until Settings resolves the
// effective configured layers, including the named-instance file.
func pluginAdditions(fs *flag.FlagSet) (map[string][]string, error) {
	out := map[string][]string{}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for _, pair := range []struct{ replace, add, key string }{
		{"plugin", "add-plugin", "paths"},
		{"plugin-dir", "add-plugin-dir", "dirs"},
	} {
		if given[pair.replace] && given[pair.add] {
			return nil, fmt.Errorf("--%s and --%s cannot be used together", pair.replace, pair.add)
		}
		if given[pair.add] {
			out[pair.key] = append([]string(nil), (*fs.Lookup(pair.add).Value.(*stringFlag))...)
		}
	}
	return out, nil
}

func main() {
	// argv[1] decides the mode: a first argument that does not start with
	// "-" is a subcommand. The CLI installs no signal handler; a signal ends
	// it by the signal.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cli.Register("store", storecmd.Command())
		cli.Register("create", createcmd.Command(buildVersion()))
		cli.Register("__guide-plugin", createcmd.GuideCommand())
		os.Exit(cli.Main(context.Background(), os.Args[1:], cli.OSEnv()))
	}
	dumpFrame := flag.Bool("dump-frame", false, "Render one live frame after plugin sync and exit")
	demoFrame := flag.Bool("demo-frame", false, "Render one frame from a built-in demo plugin that declares a border+label+close frame and exit")
	railMode := flag.String("rail", "", "Activate rail mode on startup: modal | full-canvas | stage")
	holdAlt := flag.Bool("hold-alt", false, "Simulate holding Alt/Option key on startup")
	repeatable("plugin", "Plugin: executable, plugin directory, or husk-plugin.json path (repeatable)")
	repeatable("plugin-dir", "Load every immediate subdirectory containing a husk-plugin.json (repeatable)")
	repeatable("add-plugin", "Append a plugin path to configured plugins, deduplicating paths (repeatable)")
	repeatable("add-plugin-dir", "Append a directory to configured plugin directories, deduplicating paths (repeatable)")
	var bundleDir bundleFlag
	flag.Var(&bundleDir, "bundle", "Load a bundle directory as plugins and distribution config; empty disables")
	demo := flag.String("demo", "", "In-process frame demo: pubslots (requires --dump-frame)")
	instanceName := flag.String("instance", "", "Name this instance (a-z, 0-9 and -, not digits only); default is the pid. Command mode's --instance targets one instead")
	profileName := flag.String("profile", "", "Select a profile on startup (a-z, 0-9, _ and -); it wins over the saved selection and profile.default")
	flag.Parse()
	instanceDocs := pluginInstanceDocs(flag.CommandLine)
	additions, err := pluginAdditions(flag.CommandLine)
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: %v\n", err)
		os.Exit(2)
	}
	// Subcommands come first; a positional left over here is a misplaced one
	// (`husk --instance notes ping`) and must not start an instance.
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "husk: unexpected argument %q; subcommands come first (husk <noun> [flags])\n", flag.Arg(0))
		os.Exit(2)
	}
	// The name is validated before anything touches the filesystem.
	id, err := startInstanceID(*instanceName, flagSet("instance"), os.Getpid())
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: %v\n", err)
		os.Exit(2)
	}
	profileFlag, err := startProfile(*profileName, flagSet("profile"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: %v\n", err)
		os.Exit(2)
	}
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

	// Resolve without creating: a bad bundle must fail before any runtime or
	// persistence directory is written. The host checks/creates this path only
	// after its bundle stage; the Server and Store receive the same string.
	runtimeDir, _, err := instance.LookupRuntimeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: runtime directory: %v\n", err)
		os.Exit(1)
	}
	prog := newProgramRef()
	if *dumpFrame {
		// runDumpFrame builds the headless program; a host Post waits for it.
		h, err := newHost(prog, model, id, runtimeDir, instanceDocs, profileFlag, host.WithVersion(buildVersion()), host.WithPluginAdditions(additions))
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
	h, err := newHost(prog, model, id, runtimeDir, instanceDocs, profileFlag, host.WithVersion(buildVersion()), host.WithPluginAdditions(additions), host.WithPrompt(secretPromptFunc(prog)))
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
func newHost(prog *programRef, model *WorkbenchModel, id, dir string, instanceDocs map[string]map[string]any, profileFlag *string, opts ...host.Option) (host.Looped, error) {
	h, err := host.New(contracts.Config{
		InstanceID: id,
		RuntimeDir: dir,
		Entry: instance.Entry{
			ID:              id,
			PID:             os.Getpid(),
			Socket:          instance.SocketPath(dir, id),
			ProtocolVersion: protocol.Version,
		},
		InstanceDocs: instanceDocs,
		ProfileFlag:  profileFlag,
		Post:         prog.post,
		Content:      model.content,
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
