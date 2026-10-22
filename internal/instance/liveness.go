package instance

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// DiscoveryPingTimeout bounds one ping in the discovery fan-out: a live
// local instance answers in milliseconds, so discovery and stale checks
// wait no longer than this.
const DiscoveryPingTimeout = 500 * time.Millisecond

// LookupPingTimeout bounds one ping for an explicit target — a --socket
// or --instance (or HUSK_SOCKET_PATH / HUSK_INSTANCE) step — where the
// user named the instance and a slow answer beats a wrong "no instance".
// The spec's 2 s.
const LookupPingTimeout = 2 * time.Second

// Deps holds the seams tests inject so that no test needs a real socket
// or real network timing. The zero value is production: pings go out over
// real Unix sockets with the step's timeout. A nil *Deps anywhere means
// those production defaults.
type Deps struct {
	// Ping decides whether the socket at socketPath answers. It returns
	// nil when the instance replied, or an error this package classifies:
	// ErrSocketMissing or ErrSocketRefused (or a wrapped ENOENT /
	// ECONNREFUSED / ENOTSOCK) mark the entry stale; anything else marks
	// it unresponsive. When nil, DefaultPing is used with the step's
	// timeout (discovery fans out at 500 ms, explicit lookups wait 2 s).
	// Called from multiple goroutines at once.
	Ping func(socketPath string) error
}

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
}

// Survey lists the runtime directory's entries with a liveness verdict,
// pinging them in parallel, and removes every stale entry it meets — the
// spec's rule that any CLI call seeing a stale entry removes it, `husk
// instances` included. Stale entries do not appear in the result;
// unresponsive ones do, so the CLI can report them. The result is sorted by
// instance id.
func Survey(dir string, d *Deps) ([]Status, error) {
	entries, err := List(dir)
	if err != nil {
		return nil, err
	}
	statuses := pingAll(entries, d)
	out := make([]Status, 0, len(statuses))
	for _, st := range statuses {
		if st.Verdict == VerdictStale {
			_ = RemoveIfStale(dir, st.Entry) // best effort; the entry is skipped regardless
			continue
		}
		out = append(out, st)
	}
	return out, nil
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
// lookups ping at LookupPingTimeout and reuse pingVerdict directly.
func (d *Deps) classify(e Entry) Verdict {
	return pingVerdict(d.pingFor(e.Socket, DiscoveryPingTimeout))
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
