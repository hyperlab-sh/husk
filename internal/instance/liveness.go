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

// DiscoveryPingTimeout bounds one ping: a live local instance answers in
// milliseconds, so discovery and stale checks wait no longer than this.
const DiscoveryPingTimeout = 500 * time.Millisecond

// Deps holds the seams tests inject so that no test needs a real socket,
// a real clock or the real process table. The zero value is production:
// pings go out over real Unix sockets with the discovery timeout, the pid
// check is disabled (see ProcessAlive), and StartedAt stamps come from
// time.Now. A nil *Deps anywhere means those production defaults.
type Deps struct {
	// Ping decides whether the socket at socketPath answers. It returns
	// nil when the instance replied, or an error this package classifies:
	// ErrSocketMissing or ErrSocketRefused (or a wrapped ENOENT /
	// ECONNREFUSED / ENOTSOCK) mark the entry stale; anything else marks
	// it unresponsive. When nil, DefaultPing is used with
	// DiscoveryPingTimeout. Called from multiple goroutines at once.
	Ping func(socketPath string) error

	// ProcessAlive reports whether pid names a live process. It is a test
	// seam: production leaves it nil, because the spec rules that a dead
	// pid alone must never stale an entry (pid reuse makes the check
	// unsafe) and a live ping outranks it in every case. When set and
	// reporting false, the entry is stale without a ping.
	ProcessAlive func(pid int) bool
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

// classify decides one entry's verdict. An injected ProcessAlive that
// reports the pid dead marks the entry stale without a ping; that path
// exists for tests only (see Deps.ProcessAlive). Otherwise the ping is
// authoritative: a reply is live, a missing or refused socket is stale, and
// a dial that succeeds without a reply — or any other failure — is
// unresponsive, never stale.
func (d *Deps) classify(e Entry) Verdict {
	if d != nil && d.ProcessAlive != nil && !d.ProcessAlive(e.PID) {
		return VerdictStale
	}
	return pingVerdict(d.ping(e.Socket))
}

func (d *Deps) ping(socketPath string) error {
	if d != nil && d.Ping != nil {
		return d.Ping(socketPath)
	}
	return DefaultPing(socketPath, DiscoveryPingTimeout)
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
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return classifyDialError(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
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
