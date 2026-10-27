package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/cli"
	"github.com/hyperlab-sh/husk/pkg/cli/createcmd"
	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/cli/storecmd"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/workbench"
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
	// A first argument without "-" selects the CLI, which installs no signal
	// handler. Only workbench.Run starts an interactive Bubble Tea program.
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
	converters := repeatable("converter", "Executable manifest converter, absolute path or PATH name (repeatable)")
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
	opt := workbench.Options{HoldAlt: *holdAlt, RailMode: *railMode, DemoFrame: *demoFrame, Demo: *demo}
	if *demo != "" && (*demo != "pubslots" || !*dumpFrame) {
		fmt.Fprintln(os.Stderr, "--demo supports pubslots with --dump-frame")
		os.Exit(2)
	}
	if *demo != "" || *demoFrame {
		frame, err := workbench.DumpFrame(context.Background(), opt)
		os.Exit(printDumpFrame(frame, err, os.Stdout, os.Stderr))
	}

	// Buffer diagnostics while the TUI owns the terminal; shutdown completes
	// before the buffer is printed, including plugin exit diagnostics.
	var tuiLog bytes.Buffer
	opt.Log = &tuiLog
	if *dumpFrame {
		opt.Log = os.Stderr
	}
	// Resolve without creating: bundle validation precedes runtime and Store
	// writes. The host receives this same path for its Server and Store.
	runtimeDir, _, err := instance.LookupRuntimeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk: runtime directory: %v\n", err)
		os.Exit(output.ExitEnvironment)
	}
	opt.NewHost = hostFactory(id, runtimeDir, instanceDocs, profileFlag, additions, (*converters)...)
	if *dumpFrame {
		os.Exit(dumpFrameMain(context.Background(), opt, os.Stdout, os.Stderr))
	}
	err = workbench.Run(context.Background(), opt)
	_, _ = os.Stderr.Write(tuiLog.Bytes())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(startExit(err))
	}
}

// startExit is the exit code of a failed start: 7 when the runtime directory
// is unusable, else 1.
func startExit(err error) int {
	var rd *host.RuntimeDirError
	if errors.As(err, &rd) {
		return output.ExitEnvironment
	}
	return 1
}

// dumpFrameMain keeps the binary's output and exit policy outside the UI.
func dumpFrameMain(ctx context.Context, opt workbench.Options, stdout, stderr io.Writer) int {
	frame, err := workbench.DumpFrame(ctx, opt)
	if frame == "" && err != nil {
		// Host construction failed before a frame existed, as in start mode.
		fmt.Fprintf(stderr, "husk: %v\n", err)
		return startExit(err)
	}
	return printDumpFrame(frame, err, stdout, stderr)
}

func printDumpFrame(frame string, err error, stdout, stderr io.Writer) int {
	fmt.Fprintln(stdout, frame)
	if err != nil {
		fmt.Fprintf(stderr, "husk: --dump-frame: %v\n", err)
		return 1
	}
	return 0
}

// hostFactory supplies the binary's launch defaults before workbench options.
// Both interactive and frame-dump runners use this same constructor seam.
func hostFactory(id, dir string, docs map[string]map[string]any, profile *string, additions map[string][]string, converters ...string) func(contracts.Config, ...host.Option) (host.Looped, error) {
	return func(loop contracts.Config, opts ...host.Option) (host.Looped, error) {
		return newHost(loop, id, dir, docs, profile,
			append([]host.Option{host.WithVersion(buildVersion()), host.WithPluginAdditions(additions), host.WithConverters(converters)}, opts...)...)
	}
}

// newHost fills the binary's launch configuration around the UI callbacks.
func newHost(config contracts.Config, id, dir string, instanceDocs map[string]map[string]any, profileFlag *string, opts ...host.Option) (host.Looped, error) {
	config.InstanceID, config.RuntimeDir = id, dir
	config.InstanceDocs, config.ProfileFlag = instanceDocs, profileFlag
	return host.NewInstance(config, opts...)
}
