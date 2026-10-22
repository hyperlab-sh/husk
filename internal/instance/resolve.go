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
}

// ResolveOptions carries the CLI's inputs to Resolve. The string fields are
// what the caller parsed from flags and read from the environment; Resolve
// reads no process state itself, so tests set the fields directly.
type ResolveOptions struct {
	// Dir is the runtime directory, already resolved by the caller with
	// RuntimeDir. Required; an empty Dir is a usage error.
	Dir string

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

	// KindTransport: a target was named but did not answer (or the
	// runtime directory could not be read). The spec's exit 5.
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
// The first rule that applies wins even when it then fails — a stale
// HUSK_INSTANCE or an unregistered --instance is an error, never a silent
// switch to another rule. Discovery pings every entry in parallel, removes
// the stale ones, and fails unless exactly one live instance remains.
func Resolve(opts ResolveOptions) (Resolved, error) {
	if opts.Dir == "" {
		return Resolved{}, &Error{Kind: KindUsage, Message: "runtime directory is not set"}
	}
	if opts.Socket != "" && opts.Instance != "" {
		return Resolved{}, &Error{Kind: KindUsage, Message: "--socket and --instance are mutually exclusive"}
	}
	if opts.Socket != "" {
		return lookupSocketPath(opts.Socket, SourceSocketFlag, opts.Deps)
	}
	if opts.Instance != "" {
		return lookup(opts.Dir, opts.Instance, SourceFlag, opts.Deps)
	}
	if opts.EnvSocketPath != "" {
		return lookupSocketPath(opts.EnvSocketPath, SourceEnv, opts.Deps)
	}
	if opts.EnvInstance != "" {
		return lookup(opts.Dir, opts.EnvInstance, SourceEnv, opts.Deps)
	}
	return discover(opts.Dir, opts.Deps)
}

// lookupSocketPath applies a bare socket-path rule (--socket or
// HUSK_SOCKET_PATH): no registration backs the path, so a ping is the only
// proof anyone is listening. Missing or refused is exit 3 — for the env
// step the spec's own wording, and it never falls through to discovery;
// a path that does not answer is exit 5.
func lookupSocketPath(path string, src Source, d *Deps) (Resolved, error) {
	err := d.pingFor(path, LookupPingTimeout)
	switch pingVerdict(err) {
	case VerdictLive:
		return Resolved{Socket: path, Source: src}, nil
	case VerdictStale:
		if src == SourceEnv {
			return Resolved{}, &Error{
				Kind:    KindNoInstance,
				Message: "HUSK_SOCKET_PATH points at a socket nobody is listening on; unset it or pass --instance",
				Err:     err,
			}
		}
		return Resolved{}, &Error{
			Kind:    KindNoInstance,
			Message: fmt.Sprintf("socket %q points at a socket nobody is listening on", path),
			Err:     err,
		}
	default:
		return Resolved{}, &Error{
			Kind:    KindTransport,
			Message: fmt.Sprintf("socket %q did not answer within %s", path, LookupPingTimeout),
			Err:     err,
		}
	}
}

// lookup resolves one id from the runtime directory: the --instance flag
// and the HUSK_INSTANCE steps. The entry must exist and answer a ping; a
// stale entry is removed and reported, an unresponsive one is kept and
// reported as a transport failure. Explicit lookups wait the spec's 2 s
// (LookupPingTimeout), not the discovery 500 ms.
func lookup(dir, id string, src Source, d *Deps) (Resolved, error) {
	if err := ValidID(id); err != nil {
		return Resolved{}, &Error{Kind: KindUsage, Message: err.Error(), Err: err}
	}
	e, err := readEntry(dir, id+".json")
	if err != nil {
		return Resolved{}, &Error{Kind: KindNoInstance, Message: fmt.Sprintf("instance %q is not running", id)}
	}
	switch pingVerdict(d.pingFor(e.Socket, LookupPingTimeout)) {
	case VerdictLive:
		return Resolved{Entry: &e, Socket: e.Socket, Source: src}, nil
	case VerdictStale:
		_ = RemoveIfStale(dir, e)
		return Resolved{}, &Error{
			Kind:    KindNoInstance,
			Message: fmt.Sprintf("instance %q is not running (stale entry removed)", id),
		}
	default:
		return Resolved{}, &Error{
			Kind:    KindTransport,
			Message: fmt.Sprintf("instance %q did not answer within %s", id, LookupPingTimeout),
		}
	}
}

// discover implements the last rule: list the runtime directory, remove
// stale entries, and resolve only when exactly one live instance remains.
// Zero is "no instance is running"; several is the ambiguity error carrying
// the list. Unresponsive entries are skipped, never candidates.
func discover(dir string, d *Deps) (Resolved, error) {
	statuses, err := Survey(dir, d)
	if err != nil {
		return Resolved{}, &Error{Kind: KindTransport, Message: "read runtime dir: " + err.Error(), Err: err}
	}
	var live []Status
	for _, st := range statuses {
		if st.Verdict == VerdictLive {
			live = append(live, st)
		}
	}
	switch len(live) {
	case 1:
		e := live[0].Entry
		return Resolved{Entry: &e, Socket: e.Socket, Source: SourceDiscovery}, nil
	case 0:
		return Resolved{}, &Error{Kind: KindNoInstance, Message: "no instance is running"}
	default:
		return Resolved{}, &Error{
			Kind:       KindAmbiguous,
			Message:    fmt.Sprintf("%d instances are running; choose one with --instance <id>", len(live)),
			Candidates: live,
		}
	}
}
