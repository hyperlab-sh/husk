package instance

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Source names the resolution rule that produced a socket.
type Source int

const (
	// SourceFlag: the --instance flag picked the instance.
	SourceFlag Source = iota + 1

	// SourceSocketFlag: the --socket flag carried the socket path itself.
	SourceSocketFlag

	// SourceEnv: HUSK_SOCKET_PATH or HUSK_INSTANCE picked it.
	SourceEnv

	// SourceDiscovery: exactly one live instance was found in the runtime
	// directory.
	SourceDiscovery
)

// String returns a short lower-case name for logs and CLI output.
func (s Source) String() string {
	switch s {
	case SourceFlag:
		return "flag"
	case SourceSocketFlag:
		return "socket-flag"
	case SourceEnv:
		return "env"
	case SourceDiscovery:
		return "discovery"
	default:
		return fmt.Sprintf("source(%d)", int(s))
	}
}

// Resolved is one instance chosen for a CLI command: the socket to connect
// to, where the choice came from, and — when the choice was an entry in the
// runtime directory — that entry.
type Resolved struct {
	// Entry is the registration entry of the chosen instance, nil when the
	// rule was a bare socket path (SourceSocketFlag, or HUSK_SOCKET_PATH):
	// such a path carries no registration and no pid.
	Entry *Entry

	// Socket is the socket path to connect to.
	Socket string

	// Source is the rule that won.
	Source Source

	// Skipped lists the unresponsive entries discovery passed over: they
	// accepted a connection but did not answer the ping. The CLI warns
	// about each one. Nil for every rule but discovery.
	Skipped []Status
}

// ResolveOptions carries the CLI's inputs to Resolve. The string fields are
// what the caller parsed from flags and read from the environment; Resolve
// reads no process state itself, so tests set the fields directly.
type ResolveOptions struct {
	// Dir is the runtime directory, already resolved by the caller (the CLI
	// uses LookupRuntimeDir). Required unless DirAbsent is set; an empty Dir
	// otherwise is a usage error.
	Dir string

	// DirAbsent says the runtime directory does not exist: the registry is
	// empty, so a lookup by id and discovery both find no instance. Resolve
	// never creates the directory.
	DirAbsent bool

	// Instance is the --instance flag value, empty when the flag is
	// absent. An id: a pid id or a name id.
	Instance string

	// Socket is the --socket flag value, empty when the flag is absent.
	Socket string

	// EnvSocketPath is the value of HUSK_SOCKET_PATH, empty when unset.
	EnvSocketPath string

	// EnvInstance is the value of HUSK_INSTANCE, empty when unset.
	EnvInstance string

	// Deps carries the test seams; nil is production.
	Deps *Deps
}

// ErrorKind classifies this package's failures so the CLI can pick exit
// codes without parsing text.
type ErrorKind int

const (
	// KindUsage: a flag or environment value is invalid, or two flags
	// conflict. The spec's exit 2.
	KindUsage ErrorKind = iota + 1

	// KindNoInstance: nothing to talk to — nothing is running, the named
	// instance is not registered, or its entry was stale and has been
	// removed. The spec's exit 3.
	KindNoInstance

	// KindAmbiguous: several live instances are running and no flag or
	// environment chose one. The spec's exit 4; Candidates carries them.
	KindAmbiguous

	// KindTransport: discovery found only unresponsive entries, or the
	// registry could not be read. The spec's exit 5.
	KindTransport

	// KindTaken: the instance name is held by a live process. The Server
	// reports it when bind finds the socket already bound and the existing
	// socket answers a ping (spec: dial on EADDRINUSE); Register itself
	// never probes. Not a CLI resolution code; ExitCode maps it to 1, the
	// generic failure.
	KindTaken
)

// String returns a short lower-case kind name for logs.
func (k ErrorKind) String() string {
	switch k {
	case KindUsage:
		return "usage"
	case KindNoInstance:
		return "no-instance"
	case KindAmbiguous:
		return "ambiguous"
	case KindTransport:
		return "transport"
	case KindTaken:
		return "taken"
	default:
		return fmt.Sprintf("error-kind(%d)", int(k))
	}
}

// ExitCode returns the spec's CLI exit code for the kind: 2 usage, 3 no
// instance, 4 ambiguous, 5 transport; KindTaken, which the CLI's resolution
// never produces, falls through to the generic 1.
func (k ErrorKind) ExitCode() int {
	switch k {
	case KindUsage:
		return 2
	case KindNoInstance:
		return 3
	case KindAmbiguous:
		return 4
	case KindTransport:
		return 5
	default:
		return 1
	}
}

// Error is the typed failure this package returns: the CLI maps Kind to an
// exit code, prints Message (adding its own program prefix), and for
// KindAmbiguous prints one line per candidate so the user can pick.
type Error struct {
	// Kind is the failure class.
	Kind ErrorKind

	// Message is the human sentence, without any program prefix.
	Message string

	// Candidates carries the live instances for KindAmbiguous, and is nil
	// otherwise: their ids, pids, socket paths and start times.
	Candidates []Status

	// Skipped lists the unresponsive entries discovery passed over, for a
	// failed discovery (KindNoInstance or KindAmbiguous); nil otherwise.
	Skipped []Status

	// Err is the wrapped cause, when there is one.
	Err error
}

// Error implements the error interface. For KindAmbiguous the message is
// followed by one indented line per candidate, in the spec's list format.
func (e *Error) Error() string {
	if e.Kind != KindAmbiguous || len(e.Candidates) == 0 {
		return e.Message
	}
	candidates := slices.Clone(e.Candidates)
	slices.SortStableFunc(candidates, func(a, b Status) int {
		return strings.Compare(a.Entry.ID, b.Entry.ID)
	})
	var b strings.Builder
	b.WriteString(e.Message)
	width := 0
	for _, c := range candidates {
		if l := len(c.Entry.ID); l > width {
			width = l
		}
	}
	for _, c := range candidates {
		fmt.Fprintf(&b, "\n  %-*s  started %s", width, c.Entry.ID, c.Entry.StartedAt.Format(time.RFC3339))
	}
	return b.String()
}

// Unwrap exposes the wrapped cause, when there is one.
func (e *Error) Unwrap() error { return e.Err }

// Resolve picks exactly one socket for a CLI command, applying the spec's
// order: --socket, --instance, HUSK_SOCKET_PATH, HUSK_INSTANCE, discovery.
// The first rule that applies wins even when it then fails: a stale
// HUSK_INSTANCE or an unregistered --instance is an error, never a silent
// switch to another rule. Explicit targets are not probed: the command's own
// connection (session.ping first) is their liveness check, and the caller
// runs stale recovery when that connection is refused. Only discovery pings,
// every entry in parallel, and it fails unless exactly one live instance
// remains.
func Resolve(opts ResolveOptions) (Resolved, error) {
	if opts.Dir == "" && !opts.DirAbsent {
		return Resolved{}, &Error{Kind: KindUsage, Message: "runtime directory is not set"}
	}
	if opts.Socket != "" && opts.Instance != "" {
		return Resolved{}, &Error{Kind: KindUsage, Message: "--socket and --instance are mutually exclusive"}
	}
	if opts.Socket != "" {
		return Resolved{Socket: opts.Socket, Source: SourceSocketFlag}, nil
	}
	if opts.Instance != "" {
		return lookup(opts, opts.Instance, SourceFlag)
	}
	if opts.EnvSocketPath != "" {
		return Resolved{Socket: opts.EnvSocketPath, Source: SourceEnv}, nil
	}
	if opts.EnvInstance != "" {
		return lookup(opts, opts.EnvInstance, SourceEnv)
	}
	return discover(opts)
}

// lookup resolves one id from the registry: the --instance flag and the
// HUSK_INSTANCE steps. The entry must exist; whether it answers is the
// connection's business.
func lookup(opts ResolveOptions, id string, src Source) (Resolved, error) {
	if err := ValidID(id); err != nil {
		return Resolved{}, &Error{Kind: KindUsage, Message: err.Error(), Err: err}
	}
	notRunning := &Error{Kind: KindNoInstance, Message: fmt.Sprintf("instance %q is not running", id)}
	if opts.DirAbsent {
		return Resolved{}, notRunning
	}
	e, err := readEntry(opts.Dir, id+".json")
	if err != nil {
		return Resolved{}, notRunning
	}
	return Resolved{Entry: &e, Socket: e.Socket, Source: src}, nil
}

// discover implements the last rule: survey the registry (stale entries are
// removed there) and resolve only when exactly one live instance remains.
// No entries at all is "no instance is running" (exit 3); only unresponsive
// ones is a transport failure naming them (exit 5), so a script that starts
// an instance on exit 3 never starts one beside a hung one; several live is
// the ambiguity error carrying the list. Unresponsive entries are never
// candidates.
func discover(opts ResolveOptions) (Resolved, error) {
	if opts.DirAbsent {
		return Resolved{}, &Error{Kind: KindNoInstance, Message: "no instance is running"}
	}
	statuses, err := Survey(opts.Dir, opts.Deps)
	if err != nil {
		return Resolved{}, &Error{Kind: KindTransport, Message: "read runtime dir: " + err.Error(), Err: err}
	}
	var live, skipped []Status
	for _, st := range statuses {
		switch st.Verdict {
		case VerdictLive:
			live = append(live, st)
		case VerdictUnresponsive:
			skipped = append(skipped, st)
		}
	}
	switch len(live) {
	case 1:
		e := live[0].Entry
		return Resolved{Entry: &e, Socket: e.Socket, Source: SourceDiscovery, Skipped: skipped}, nil
	case 0:
		if len(skipped) == 0 {
			return Resolved{}, &Error{Kind: KindNoInstance, Message: "no instance is running"}
		}
		ids := make([]string, len(skipped))
		for i, st := range skipped {
			ids[i] = st.Entry.ID
		}
		noun := "instances are"
		if len(ids) == 1 {
			noun = "instance is"
		}
		return Resolved{}, &Error{
			Kind:    KindTransport,
			Message: fmt.Sprintf("%d %s registered but none answered within %s: %s", len(ids), noun, ProbeTimeout, strings.Join(ids, ", ")),
			Skipped: skipped,
		}
	default:
		return Resolved{}, &Error{
			Kind:       KindAmbiguous,
			Message:    fmt.Sprintf("%d instances are running; choose one with --instance <id>", len(live)),
			Candidates: live,
			Skipped:    skipped,
		}
	}
}
