// Package cli is the husk binary's subcommand side: every subcommand is a
// socket client of a running instance. It resolves one instance with
// internal/instance, speaks pkg/protocol and nothing else, and maps every
// failure to the spec's exit codes (docs/docs/design/instances.md). It
// imports no Stem, Services, Substrate or Pods package.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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
	// Getenv reads an environment variable.
	Getenv func(string) string
	// RuntimeDir returns the checked runtime directory.
	RuntimeDir func() (string, error)
	// Deps are the instance liveness seams; nil is production.
	Deps *instance.Deps
}

// OSEnv is the Env of the running process.
func OSEnv() Env {
	return Env{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		StdoutIsTTY: IsTTY(os.Stdout),
		Getenv:      os.Getenv,
		RuntimeDir:  instance.RuntimeDir,
	}
}

// command is one subcommand. A later unit adds its commands to the table
// below and implements run on the Invocation.
type command struct {
	// usage is the one-line synopsis shown in the usage text.
	usage string
	// timeout is the default --timeout; zero means none.
	timeout time.Duration
	// run executes the command; args are the positional arguments left
	// after the global flags.
	run func(ctx context.Context, inv *Invocation, args []string) error
}

// commands is the subcommand table, keyed by the first non-flag argument.
func commands() map[string]command {
	return map[string]command{
		"instances": {usage: "husk instances", timeout: 2 * time.Second, run: runInstances},
		"ping":      {usage: "husk ping", timeout: 2 * time.Second, run: runPing},
		"rpc":       {usage: "husk rpc <method> [params-json]", timeout: 30 * time.Second, run: runRPC},
	}
}

// Invocation is what a running command works with: the parsed global flags
// and the helpers every command shares.
type Invocation struct {
	env      Env
	format   string
	instance string
	socket   string
}

// Resolve picks the one instance this command talks to, through
// instance.Resolve. Stale entries are removed there.
func (inv *Invocation) Resolve() (instance.Resolved, error) {
	dir, err := inv.env.RuntimeDir()
	if err != nil {
		return instance.Resolved{}, &Error{Exit: ExitRemote, Code: "runtime_dir", Message: err.Error(), Err: err}
	}
	res, err := instance.Resolve(instance.ResolveOptions{
		Dir:           dir,
		Instance:      inv.instance,
		Socket:        inv.socket,
		EnvSocketPath: inv.env.Getenv(instance.EnvSocketPath),
		EnvInstance:   inv.env.Getenv(instance.EnvInstance),
		Deps:          inv.env.Deps,
	})
	if err != nil {
		return instance.Resolved{}, resolveErr(err)
	}
	if res.Entry != nil && res.Entry.ProtocolVersion != "" {
		if err := checkMajor(res.Entry.ProtocolVersion); err != nil {
			return instance.Resolved{}, err
		}
	}
	return res, nil
}

// Dial resolves the target and connects to it.
func (inv *Invocation) Dial(ctx context.Context) (*Client, instance.Resolved, error) {
	res, err := inv.Resolve()
	if err != nil {
		return nil, res, err
	}
	c, err := Dial(ctx, res.Socket, ClientOptions{Token: inv.env.Getenv("HUSK_PLUGIN_TOKEN")})
	return c, res, err
}

// checkMajor is the spec's protocol check: a different major is exit 6.
func checkMajor(version string) error {
	major, _, err := protocol.ParseVersion(version)
	if err == nil && major == protocol.Major {
		return nil
	}
	return protocolErr(err, "instance speaks protocol %s, this CLI speaks %d.x", versionLabel(version), protocol.Major)
}

// versionLabel renders an instance's version as "MAJOR.x" when it parses.
func versionLabel(v string) string {
	if major, _, err := protocol.ParseVersion(v); err == nil {
		return fmt.Sprintf("%d.x", major)
	}
	return fmt.Sprintf("%q", v)
}

// Main runs one subcommand (args[0] is its name) and returns the process
// exit code. Results go to env.Stdout, everything else to env.Stderr.
func Main(ctx context.Context, args []string, env Env) int {
	table := commands()
	name := args[0]
	if name == "help" {
		usage(env.Stdout, table)
		return ExitOK
	}
	cmd, ok := table[name]
	if !ok {
		fmt.Fprintf(env.Stderr, "husk: unknown command %q\n", name)
		usage(env.Stderr, table)
		return ExitUsage
	}

	inv := &Invocation{env: env}
	var format string
	timeout := time.Duration(0)
	fs := flag.NewFlagSet("husk "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.StringVar(&inv.instance, "instance", "", "target instance `id`")
	fs.StringVar(&inv.socket, "socket", "", "target socket `path`")
	fs.StringVar(&format, "format", "", "output format: json | table (default by TTY)")
	fs.DurationVar(&timeout, "timeout", 0, "total time for the command after resolution (default per command)")
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "usage: %s [--instance id | --socket path] [--format json|table] [--timeout duration]\n", cmd.usage)
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			return ExitOK
		}
		return ExitUsage // the flag package already printed the reason
	}

	jsonErrors := false // errors follow the format once it is known
	report := func(err error, timeoutText string) int {
		e := asError(err, timeoutText)
		writeError(env.Stderr, e, jsonErrors)
		return e.Exit
	}

	switch format {
	case "":
		format = formatTable
		if !env.StdoutIsTTY {
			format = formatJSON
		}
	case formatJSON, formatTable:
	default:
		return report(usageErr("invalid --format %q: use json or table", format), "")
	}
	inv.format = format
	jsonErrors = format == formatJSON
	if inv.instance != "" && inv.socket != "" {
		return report(usageErr("--instance and --socket are mutually exclusive"), "")
	}
	if timeout < 0 {
		return report(usageErr("--timeout must not be negative"), "")
	}
	if timeout == 0 {
		timeout = cmd.timeout
	}
	timeoutText := ""
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		timeoutText = timeout.String()
	}

	if err := cmd.run(ctx, inv, pos); err != nil {
		return report(err, timeoutText)
	}
	return ExitOK
}

// parseInterspersed parses flags that may appear between positional
// arguments, which the standard flag package stops at; "--" ends flag
// parsing.
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

// writeError prints a failure to w: one JSON object in JSON mode, one
// "husk: <message>" line in table mode.
func writeError(w io.Writer, e *Error, asJSON bool) {
	if !asJSON {
		fmt.Fprintf(w, "husk: %s\n", e.Message)
		return
	}
	body := map[string]any{"code": e.Code, "message": e.Message}
	if len(e.Data) > 0 {
		body["data"] = e.Data
	}
	b, _ := json.Marshal(map[string]any{"error": body})
	fmt.Fprintf(w, "%s\n", b)
}

func usage(w io.Writer, table map[string]command) {
	names := make([]string, 0, len(table))
	for n := range table {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("usage: husk [flags]                 start an instance\n")
	for _, n := range names {
		fmt.Fprintf(&b, "       %s\n", table[n].usage)
	}
	b.WriteString("\nflags of every command: --instance <id> | --socket <path>, --format json|table, --timeout <duration>\n")
	fmt.Fprint(w, b.String())
}
