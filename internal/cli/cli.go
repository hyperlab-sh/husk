// Package cli is the husk binary's subcommand side: every subcommand is a
// socket client of a running instance, or a local command registered by
// another package. It resolves one instance with internal/instance, speaks
// pkg/protocol and nothing else, and maps every failure to the spec's exit
// codes (docs/docs/design/instances.md). It imports no Stem, Services,
// Substrate or Pods package; formats, errors and exit codes live in the leaf
// package internal/cli/output.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/instance"
	"golang.org/x/term"
)

// Env is everything Main reads from the process, so tests drive it without
// touching the real one.
type Env struct {
	// Stdout carries results only; Stderr everything else; Stdin is for
	// commands that read a payload.
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	// StdoutIsTTY picks the default output format.
	StdoutIsTTY bool
	// StdinIsTTY enables the no-echo secret reader; ReadPassword is injected
	// so callers never need a real terminal in tests.
	StdinIsTTY   bool
	ReadPassword func() ([]byte, error)
	// Getenv reads an environment variable.
	Getenv func(string) string
	// RuntimeDir looks up the runtime directory without creating it: the
	// checked path, whether it exists, and an error when it is unusable.
	RuntimeDir func() (dir string, found bool, err error)
	// Deps are the instance liveness seams; nil is production.
	Deps *instance.Deps
}

// OSEnv is the Env of the running process.
func OSEnv() Env {
	return Env{
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Stdin:        os.Stdin,
		StdoutIsTTY:  isTerminal(os.Stdout),
		StdinIsTTY:   isTerminal(os.Stdin),
		ReadPassword: func() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) },
		Getenv:       os.Getenv,
		RuntimeDir:   instance.LookupRuntimeDir,
	}
}

// isTerminal reports whether f is a terminal. A character-device check is not
// enough: /dev/null is one.
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// command is one subcommand of the table.
type command struct {
	// usage is the one-line synopsis shown in the usage text.
	usage string
	// timeout is the default --timeout; zero means none.
	timeout time.Duration
	// local commands talk to no single instance: they reject --instance,
	// --socket and --timeout.
	local bool
	// run executes the command; args are the positional arguments left
	// after the global flags. A noun with verbs and no run of its own needs
	// a verb.
	run func(ctx context.Context, inv *Invocation, args []string) error
	// verbs are the subcommands of a noun (husk state get): the first
	// positional argument picks one. A noun with verbs and a run of its own
	// runs it when no verb is given (husk state).
	verbs map[string]command
	// accepts names the command-specific flags this command takes (the
	// others in extraFlags are a usage error for it).
	accepts []string
	// stream marks a watch: it rejects --timeout, and timeout is only the
	// opening bound.
	stream bool
}

// builtins is the socket command table of this package.
func builtins() map[string]command {
	return map[string]command{
		"instances": {usage: "husk instances", local: true, run: runInstances},
		"ping":      {usage: "husk ping", timeout: 2 * time.Second, run: runPing},
		"rpc":       {usage: "husk rpc <method> [<params-json>]", timeout: 30 * time.Second, run: runRPC},
		"state":     stateCommand(),
		"action":    actionCommand(),
		"event":     eventCommand(),
		"plugin":    pluginCommand(),
		"hook":      hookCommand(),
		"settings":  settingsCommand(),
	}
}

var (
	registryMu sync.Mutex
	registered = map[string]output.Command{}
)

// Register adds a command package's noun to the table, before Main runs. It
// is the seam through which commands that must not live in this package (the
// store commands import Store, which this package may not) join the CLI; it
// panics on an empty, duplicate or built-in noun, which is a wiring mistake.
func Register(noun string, cmd output.Command) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, builtin := builtins()[noun]; noun == "" || noun == "help" || builtin {
		panic("cli: cannot register noun " + fmt.Sprintf("%q", noun))
	}
	if _, dup := registered[noun]; dup {
		panic("cli: noun registered twice: " + noun)
	}
	registered[noun] = cmd
}

// table is the full command table: the built-ins plus what was registered.
// Registered commands are local: they never talk to one chosen instance.
func table() map[string]command {
	t := builtins()
	registryMu.Lock()
	defer registryMu.Unlock()
	for noun, c := range registered {
		run := c.Run
		t[noun] = command{
			usage: c.Usage,
			local: true,
			run: func(ctx context.Context, inv *Invocation, args []string) error {
				return run(ctx, args, inv.out)
			},
		}
	}
	return t
}

// Invocation is what a running command works with: the parsed global flags,
// the output options and the helpers every command shares.
type Invocation struct {
	env Env
	out output.Options
	// instance and socket are the target flags.
	instance, socket string
	// timeout is the effective --timeout, zero for none.
	timeout time.Duration
	// dir is the runtime directory resolution read, for stale recovery.
	dir string
	// The command-specific flags; flagSet says which were given.
	owner, argsJSON, data, ifRevision string
	args                              []string
	force                             bool
	action, focus                     string
	shadowed, problems, source        bool
	flagSet                           map[string]bool
}

// withTimeout bounds ctx by the command's --timeout, from now on.
func (inv *Invocation) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if inv.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, inv.timeout)
}

// Main runs one command (args[0] is its noun) and returns the process exit
// code. Results go to env.Stdout, everything else to env.Stderr. The CLI
// installs no signal handler: an interrupt ends the process by the signal.
func Main(ctx context.Context, args []string, env Env) int {
	cmds := table()
	out := output.Options{
		Format: defaultFormat(env), Stdout: env.Stdout, Stderr: env.Stderr,
		Stdin: env.Stdin, StdoutIsTTY: env.StdoutIsTTY, Getenv: env.Getenv,
	}
	if len(args) == 0 {
		out.Format = peekFormat(args, out.Format)
		return failUsage(out, cmds, "no command")
	}
	noun := args[0]
	if noun == "help" {
		usage(env.Stdout, cmds)
		return output.ExitOK
	}
	cmd, ok := cmds[noun]
	if !ok {
		out.Format = peekFormat(args[1:], out.Format)
		return failUsage(out, cmds, "unknown command %q", noun)
	}

	inv := &Invocation{env: env, out: out}
	var format string
	var timeout time.Duration
	fs := flag.NewFlagSet("husk "+noun, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&inv.instance, "instance", "", "")
	fs.StringVar(&inv.socket, "socket", "", "")
	fs.StringVar(&format, "format", "", "")
	fs.DurationVar(&timeout, "timeout", 0, "")
	fs.StringVar(&inv.owner, "owner", "", "")
	fs.StringVar(&inv.argsJSON, "args-json", "", "")
	fs.StringVar(&inv.data, "data", "", "")
	fs.StringVar(&inv.ifRevision, "if-revision", "", "")
	fs.BoolVar(&inv.force, "force", false, "")
	fs.StringVar(&inv.action, "action", "", "")
	fs.StringVar(&inv.focus, "focus", "", "")
	fs.BoolVar(&inv.shadowed, "shadowed", false, "")
	fs.BoolVar(&inv.problems, "problems", false, "")
	fs.BoolVar(&inv.source, "source", false, "")
	fs.Func("arg", "", func(v string) error { inv.args = append(inv.args, v); return nil })
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			commandUsage(env.Stdout, cmd, noun)
			return output.ExitOK
		}
		inv.out.Format = peekFormat(args[1:], out.Format)
		return inv.out.Report(output.Usage("%v", err))
	}
	if format != "" {
		f, err := output.ParseFormat(format)
		if err != nil {
			return inv.out.Report(err)
		}
		inv.out.Format = f
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	inv.flagSet = set
	if len(cmd.verbs) > 0 && len(pos) > 0 {
		verb, ok := cmd.verbs[pos[0]]
		if !ok {
			return inv.out.Report(output.Usage("unknown command %q", noun+" "+pos[0]))
		}
		cmd, pos = verb, pos[1:]
	} else if cmd.run == nil {
		return inv.out.Report(output.Usage("husk %s needs a command", noun))
	}
	for name := range set {
		if isGlobalFlag(name) || slices.Contains(cmd.accepts, name) {
			continue
		}
		return inv.out.Report(output.Usage("this command does not take --%s", name))
	}
	if cmd.stream && set["timeout"] {
		return inv.out.Report(output.Usage("watch commands end only by signal or disconnect and do not take --timeout"))
	}
	if cmd.local {
		for _, name := range []string{"instance", "socket", "timeout"} {
			if set[name] {
				return inv.out.Report(output.Usage("husk %s does not take --%s", noun, name))
			}
		}
	}
	if inv.instance != "" && inv.socket != "" {
		return inv.out.Report(output.Usage("--instance and --socket are mutually exclusive"))
	}
	if inv.timeout, err = effectiveTimeout(timeout, cmd.timeout); err != nil {
		return inv.out.Report(err)
	}

	if err := cmd.run(ctx, inv, pos); err != nil {
		return inv.out.Report(inv.finish(err))
	}
	return output.ExitOK
}

// effectiveTimeout applies the --timeout rules: negative is a usage error and
// 0 means the command's default.
func effectiveTimeout(flagValue, def time.Duration) (time.Duration, error) {
	if flagValue < 0 {
		return 0, output.Usage("--timeout must not be negative")
	}
	if flagValue == 0 {
		return def, nil
	}
	return flagValue, nil
}

// defaultFormat is table on a terminal and json otherwise.
func defaultFormat(env Env) output.Format {
	if env.StdoutIsTTY {
		return output.Table
	}
	return output.JSON
}

// peekFormat reads a valid --format out of args that failed to parse, so the
// usage error itself is printed in the format the caller asked for.
func peekFormat(args []string, fallback output.Format) output.Format {
	f := fallback
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name != "format" || !strings.HasPrefix(args[i], "-") {
			continue
		}
		if !hasVal {
			if i+1 == len(args) {
				break
			}
			i++
			val = args[i]
		}
		if p, err := output.ParseFormat(val); err == nil {
			f = p
		}
	}
	return f
}

// failUsage reports a usage error: the error object in JSON mode, the
// message and the usage text in table mode, both on stderr.
func failUsage(out output.Options, cmds map[string]command, format string, a ...any) int {
	code := out.Report(output.Usage(format, a...))
	if out.Format == output.Table {
		usage(out.Stderr, cmds)
	}
	return code
}

// finish maps a command's error: a deadline becomes the transport failure
// naming the effective timeout.
func (inv *Invocation) finish(err error) error {
	if inv.timeout > 0 && isDeadline(err) {
		return output.Transport(err, "instance did not answer within %s", inv.timeout)
	}
	return err
}

// parseInterspersed parses flags that may appear between positional
// arguments, which the standard flag package stops at. "--" ends flag
// parsing; a lone "-" is a positional argument.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// isGlobalFlag reports whether name is one of the flags every command takes.
func isGlobalFlag(name string) bool {
	switch name {
	case "instance", "socket", "format", "timeout":
		return true
	}
	return false
}

// synopses are the usage lines of a command: its own, or one per verb.
func synopses(cmd command) []string {
	var out []string
	if cmd.run != nil {
		out = append(out, cmd.usage)
	}
	verbs := make([]string, 0, len(cmd.verbs))
	for v := range cmd.verbs {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	for _, v := range verbs {
		out = append(out, cmd.verbs[v].usage)
	}
	return out
}

func commandUsage(w io.Writer, cmd command, _ string) {
	for i, line := range synopses(cmd) {
		prefix := "usage: "
		if i > 0 {
			prefix = "       "
		}
		fmt.Fprintf(w, "%s%s\n", prefix, line)
	}
	if !cmd.local {
		fmt.Fprint(w, "       [--instance <id> | --socket <path>] [--timeout <duration>]\n")
	}
	fmt.Fprint(w, "       [--format json|table]\n")
}

func usage(w io.Writer, cmds map[string]command) {
	nouns := make([]string, 0, len(cmds))
	for n := range cmds {
		nouns = append(nouns, n)
	}
	sort.Strings(nouns)
	var b strings.Builder
	b.WriteString("usage: husk [flags]                 start an instance\n")
	for _, n := range nouns {
		for _, line := range synopses(cmds[n]) {
			fmt.Fprintf(&b, "       %s\n", line)
		}
	}
	b.WriteString("       husk help\n")
	b.WriteString("\nglobal flags follow the command: --instance <id> | --socket <path>, --format json|table, --timeout <duration>\n")
	fmt.Fprint(w, b.String())
}
