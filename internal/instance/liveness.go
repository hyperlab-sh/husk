package instance

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// ProbeTimeout is the one probe deadline, covering connect, write and read:
// a live local instance answers in milliseconds, so discovery, `husk
// instances`, the Server's claim and stale recovery wait no longer than this.
const ProbeTimeout = 500 * time.Millisecond

// Probe sends one session.ping to the socket at socketPath within
// ProbeTimeout. It returns nil on a reply; classify the error with ClassOf.
func Probe(socketPath string) error { return DefaultPing(socketPath, ProbeTimeout) }

// Deps holds the seams tests inject so that no test needs a real socket
// or real network timing. The zero value is production: pings go out over
// real Unix sockets with the step's timeout. A nil *Deps anywhere means
// those production defaults.
type Deps struct {
	// Ping decides whether the socket at socketPath answers. It returns
	// nil when the instance replied, or an error this package classifies:
	// ErrSocketMissing or ErrSocketRefused (or a wrapped ENOENT /
	// ECONNREFUSED / ENOTSOCK) mark the entry stale; anything else marks
	// it unresponsive. When nil, DefaultPing is used with
	// ProbeTimeout.
	// Called from multiple goroutines at once.
	Ping func(socketPath string) error

	// LockWait overrides the stale-recovery lock deadline (LockWait);
	// zero is the production 2.5 s. Tests shorten it to exercise
	// contention without waiting.
	LockWait time.Duration

	// Remove unlinks a path in stale recovery; nil is os.Remove. Tests inject
	// failures such as EACCES or EBUSY.
	Remove func(path string) error
}

// remove unlinks path, safe on a nil *Deps.
func (d *Deps) remove(path string) error {
	if d != nil && d.Remove != nil {
		return d.Remove(path)
	}
	return os.Remove(path)
}

// lockWait returns the lock deadline, safe on a nil *Deps.
func (d *Deps) lockWait() time.Duration {
	if d != nil && d.LockWait > 0 {
		return d.LockWait
	}
	return LockWait
}

// Class is the liveness class of one registration entry: live, stale or
// unresponsive. It is the spec's classification; Verdict is its older name.
type Class = Verdict

// Verdict classifies one registration entry's liveness.
type Verdict int

const (
	// VerdictLive: the entry's socket answered a ping.
	VerdictLive Verdict = iota + 1

	// VerdictStale: the socket is missing or refused the connection. The
	// entry is removed where it is met.
	VerdictStale

	// VerdictUnresponsive: the socket accepted the connection but did not
	// answer within the ping timeout. The entry is kept — a busy instance
	// must not lose its registration to a 500 ms ping — and skipped by
	// discovery.
	VerdictUnresponsive
)

// String returns the lower-case verdict name used in tables and logs.
func (v Verdict) String() string {
	switch v {
	case VerdictLive:
		return "live"
	case VerdictStale:
		return "stale"
	case VerdictUnresponsive:
		return "unresponsive"
	default:
		return fmt.Sprintf("verdict(%d)", int(v))
	}
}

// Status is one registration entry with its liveness verdict: one row of
// the instance survey that backs `husk instances` and discovery.
type Status struct {
	// Entry is the registration entry as read from disk.
	Entry Entry

	// Verdict is the liveness classification; stale entries never appear
	// in a Survey result because they are removed first.
	Verdict Verdict

	// Locked is set on an unresponsive status that is really a stale
	// entry left in place because the runtime-directory lock was not
	// acquired in time (spec: listed as unresponsive, with a warning that
	// names the lock). Nothing was removed.
	Locked bool

	// NotSocket is the socket path when the entry's path holds something
	// that is not a socket. Recovery refuses to unlink that path (the entry
	// is removed like any stale one), so the status stays in the result,
	// classed stale; callers skip it and warn naming the path.
	NotSocket string

	// RecoverErr is the removal failure (an *fs.PathError naming the entry or
	// socket path) stale recovery hit for this entry, such as EACCES or EBUSY.
	// The status stays in the result, classed stale; callers skip it and warn
	// naming the path.
	RecoverErr error
}

// Skipped reports whether recovery could not clear this stale entry, so
// callers skip it and warn instead of treating it as gone.
func (s Status) Skipped() bool { return s.NotSocket != "" || s.RecoverErr != nil }

// Survey lists the runtime directory's entries with a liveness class,
// probing them in parallel, then runs stale recovery once, under one lock,
// for the whole stale set. Stale entries are removed and do not appear in
// the result; live and unresponsive ones do, so the CLI can report them. An
// entry whose re-probe under the lock found it live or unresponsive is
// listed as such. If the lock is not acquired in time the stale entries are
// listed unresponsive with Locked set and nothing is removed. An entry whose
// socket path is not a socket is skipped: it stays in the result as a
// stale status with NotSocket set, and the path itself is left alone. The result is sorted by instance
// id.
func Survey(dir string, d *Deps) ([]Status, error) {
	entries, err := List(dir)
	if err != nil {
		return nil, err
	}
	statuses := pingAll(entries, d)
	var stale []*Entry
	var staleAt []int
	for i, st := range statuses {
		if st.Verdict == VerdictStale {
			e := st.Entry
			stale = append(stale, &e)
			staleAt = append(staleAt, i)
		}
	}
	if len(stale) > 0 {
		classes, err := RecoverStaleWith(dir, stale, d)
		switch {
		case errors.Is(err, ErrLocked):
			for _, i := range staleAt {
				statuses[i].Verdict, statuses[i].Locked = VerdictUnresponsive, true
			}
		case classes == nil:
			return nil, err
		default:
			for k, i := range staleAt {
				statuses[i].Verdict = classes[k]
			}
			// A removal failure leaves that entry stale; carry it out so the
			// caller can warn naming the path, and keep surveying.
			for _, re := range leafErrors(err) {
				var ns *NotSocketError
				var pe *fs.PathError
				var path string
				switch {
				case errors.As(re, &ns):
					path = ns.Path
				case errors.As(re, &pe):
					path = pe.Path
				default:
					continue
				}
				for _, i := range staleAt {
					id := statuses[i].Entry.ID
					if statuses[i].Verdict != VerdictStale || (path != SocketPath(dir, id) && path != EntryPath(dir, id)) {
						continue
					}
					if ns != nil {
						statuses[i].NotSocket = path
					} else {
						statuses[i].RecoverErr = re
					}
				}
			}
		}
	}
	out := make([]Status, 0, len(statuses))
	for _, st := range statuses {
		if st.Verdict != VerdictStale || st.Skipped() {
			out = append(out, st)
		}
	}
	return out, nil
}

// leafErrors flattens a joined error into its members.
func leafErrors(err error) []error {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, c := range j.Unwrap() {
			out = append(out, leafErrors(c)...)
		}
		return out
	}
	return []error{err}
}

// pingAll classifies every entry, pinging concurrently: discovery must not
// feel slow, and the spec caps the whole fan-out at one ping timeout.
// Workers hand their results back over a channel and the caller writes the
// result slice, so exactly one goroutine touches it.
func pingAll(entries []Entry, d *Deps) []Status {
	type indexedStatus struct {
		index  int
		status Status
	}
	ch := make(chan indexedStatus, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			ch <- indexedStatus{index: i, status: Status{Entry: e, Verdict: d.classify(e)}}
		}(i, e)
	}
	wg.Wait()
	close(ch)
	out := make([]Status, len(entries))
	for is := range ch {
		out[is.index] = is.status
	}
	return out
}

// classify decides one entry's verdict from a ping at the discovery
// timeout. The ping is the only evidence: a reply is live, a missing or
// refused socket is stale, and a dial that succeeds without a reply — or
// any other failure — is unresponsive, never stale. There is no pid
// check anywhere in liveness: the spec rules that a dead pid alone must
// never stale an entry, because pid reuse makes the check unsafe, and a
// stale-looking pid says nothing a ping does not say better. Explicit
// lookups are not probed at all: the CLI's own connection is their check.
func (d *Deps) classify(e Entry) Verdict {
	return pingVerdict(d.pingFor(e.Socket, ProbeTimeout))
}

// pingFor pings at an explicit timeout: an injected Ping always wins
// (tests fix the outcome, not the timing); otherwise DefaultPing runs
// with the given timeout.
func (d *Deps) pingFor(socketPath string, timeout time.Duration) error {
	if d != nil && d.Ping != nil {
		return d.Ping(socketPath)
	}
	return DefaultPing(socketPath, timeout)
}

// Sentinels DefaultPing (and any well-behaved injected Ping) wraps its
// failures in, so classification does not depend on raw syscall errors.
var (
	// ErrSocketMissing means the socket file does not exist.
	ErrSocketMissing = errors.New("socket does not exist")

	// ErrSocketRefused means nothing is listening, or the file is not a
	// socket: the entry is stale.
	ErrSocketRefused = errors.New("connection refused by socket")

	// ErrPingTimeout means the socket was reached but did not answer in
	// time: the entry is unresponsive, never stale.
	ErrPingTimeout = errors.New("no ping reply in time")
)

// DefaultPing is the production Ping: it dials the Unix socket, writes one
// session.ping request and waits for one reply line, all within timeout. It
// checks reachability, not protocol validity — a line read back in time
// means a live instance, and the CLI's real session.ping owns the protocol
// check. Dial and read failures are wrapped in the sentinels above.
func DefaultPing(socketPath string, timeout time.Duration) error {
	// One deadline covers dial, write and read: a slow dial consumes the
	// budget instead of leaving a fresh read window, so the total wait is
	// never more than timeout.
	deadline := time.Now().Add(timeout)
	d := net.Dialer{Deadline: deadline}
	conn, err := d.Dial("unix", socketPath)
	if err != nil {
		return classifyDialError(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	req := []byte(`{"id":1,"method":"session.ping","params":{}}` + "\n")
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("%w: %v", ErrPingTimeout, err)
	}
	r := bufio.NewReader(io.LimitReader(conn, 64<<10))
	if _, err := r.ReadString('\n'); err != nil {
		return fmt.Errorf("%w: %v", ErrPingTimeout, err)
	}
	return nil
}

// classifyDialError sorts a failed dial into the stale and unresponsive
// buckets. Only a missing socket and a refused (or non-socket) connection
// mark an entry stale; a timeout and every other failure keep it.
func classifyDialError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOENT):
		return fmt.Errorf("%w: %v", ErrSocketMissing, err)
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOTSOCK):
		return fmt.Errorf("%w: %v", ErrSocketRefused, err)
	default:
		return fmt.Errorf("%w: %v", ErrPingTimeout, err)
	}
}

// pingVerdict maps a ping outcome to a verdict. Injected pings may return
// raw syscall errors, so the sentinel check and the errno check are both
// applied.
func pingVerdict(err error) Verdict {
	if err == nil {
		return VerdictLive
	}
	switch {
	case errors.Is(err, ErrSocketMissing), errors.Is(err, ErrSocketRefused),
		errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOENT),
		errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOTSOCK):
		return VerdictStale
	default:
		return VerdictUnresponsive
	}
}

// ClassOf classifies the outcome of Probe (or DefaultPing): nil is live, a
// missing or refused socket is stale, anything else is unresponsive.
func ClassOf(probeErr error) Class { return pingVerdict(probeErr) }
