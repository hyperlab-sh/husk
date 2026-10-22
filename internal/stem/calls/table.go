package calls

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type pendingCall struct {
	handle    Handle
	requester Requester
	method    string
	started   time.Time
	deadline  time.Time
	timeout   time.Duration
}

type orphanLog struct {
	last       time.Time
	suppressed uint64
}

// Table tracks every outstanding instance-to-plugin request. All methods are
// loop-only. Callers must deliver returned Results before handling another input
// message, and only after a method has returned may a requester reenter Table.
type Table struct {
	out                 Outbox
	clock               Clock
	logger              func(string, ...any)
	maxPending          int
	maxPendingPerPlugin int
	pending             map[Handle]pendingCall
	perPlugin           map[PluginID]int
	lastID              ID
	idExhaustedLogged   bool
	closed              bool
	sweepInFlight       bool
	timeouts            uint64
	orphans             uint64
	orphanLogs          map[PluginID]orphanLog
}

// New constructs a table with its own counter, caps, clock and diagnostics.
// A nil outbox fails Sends with ErrUnavailable. Negative caps panic.
func New(out Outbox, opts TableOptions) *Table {
	if opts.MaxPending < 0 || opts.MaxPendingPerPlugin < 0 {
		panic("calls: pending caps must not be negative")
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.MaxPending == 0 {
		opts.MaxPending = MaxPending
	}
	if opts.MaxPendingPerPlugin == 0 {
		opts.MaxPendingPerPlugin = MaxPendingPerPlugin
	}
	return &Table{
		out: out, clock: opts.Clock, logger: opts.Logger,
		maxPending: opts.MaxPending, maxPendingPerPlugin: opts.MaxPendingPerPlugin,
		pending: make(map[Handle]pendingCall), perPlugin: make(map[PluginID]int),
		orphanLogs: make(map[PluginID]orphanLog),
	}
}

// Send validates call options, burns an id, checks caps, records the call and
// enqueues it through Outbox. A failed Send returns a zero Handle, leaves no
// pending entry and never produces a Result. Method/params semantics and plugin
// liveness are the requester's responsibility; Outbox validates the wire line.
func (t *Table) Send(plugin PluginID, method string, params json.RawMessage, opts Options) (Handle, error) {
	if t.closed {
		return Handle{}, ErrClosed
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if opts.Requester == "" || timeout < MinTimeout || timeout > MaxTimeout {
		return Handle{}, fmt.Errorf("%w: requester required and timeout must be %s..%s (or zero)", ErrBadOptions, MinTimeout, MaxTimeout)
	}
	if t.lastID >= maxID {
		if !t.idExhaustedLogged {
			t.idExhaustedLogged = true
			t.logf("calls: numeric request id space exhausted; refusing further sends")
		}
		return Handle{}, ErrBusy
	}
	t.lastID++
	if err := t.checkCaps(plugin); err != nil {
		return Handle{}, err
	}
	h := Handle{Plugin: plugin, ID: t.lastID}
	now := t.clock.Now()
	t.pending[h] = pendingCall{handle: h, requester: opts.Requester, method: method, started: now, deadline: now.Add(timeout), timeout: timeout}
	t.perPlugin[plugin]++
	// The range guard above guarantees that this conversion is representable.
	wireID, _ := protocol.NewIntID(int64(h.ID))
	msg := protocol.Message{ID: &wireID, Method: method, Params: params}
	var err error
	if t.out == nil {
		err = errors.New("no outbox configured")
	} else {
		err = t.out.Send(plugin, msg)
	}
	if err != nil {
		t.remove(h)
		return Handle{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return h, nil
}

// CanSend preflights one additional request per argument, including repeated
// plugins. It neither reserves capacity nor burns ids; it checks caps and the
// remaining numeric id space, not liveness or transport availability. No other
// loop work may interleave between this check and the fan-out if its capacity
// guarantee is to hold.
func (t *Table) CanSend(plugins ...PluginID) error {
	if t.closed {
		return ErrClosed
	}
	if len(plugins) > 0 && (t.lastID >= maxID || uint64(len(plugins)) > uint64(maxID-t.lastID)) {
		return ErrBusy
	}
	return t.checkCaps(plugins...)
}

func (t *Table) checkCaps(plugins ...PluginID) error {
	if len(plugins) > t.maxPending-len(t.pending) {
		return ErrBusy
	}
	additional := make(map[PluginID]int)
	for _, plugin := range plugins {
		additional[plugin]++
		if additional[plugin] > t.maxPendingPerPlugin-t.perPlugin[plugin] {
			return ErrPluginBusy
		}
	}
	return nil
}

// Resolve matches a positive numeric id only within its source plugin channel.
// It removes a matched call before returning its result. Replies beat a deadline
// that passed before the loop could process a sweep. Orphans are counted, logged
// at most once per plugin per second and dropped, without disconnecting anyone.
func (t *Table) Resolve(plugin PluginID, reply protocol.Message) (Result, bool) {
	var rawID string
	if reply.ID != nil {
		rawID = reply.ID.String()
	}
	n, err := strconv.ParseUint(rawID, 10, 64)
	h := Handle{Plugin: plugin, ID: ID(n)}
	p, found := t.pending[h]
	if err != nil || n == 0 || n > uint64(maxID) || !found {
		t.orphan(plugin, rawID)
		return Result{}, false
	}
	t.remove(h)
	r := p.result(t.clock.Now())
	kind, invalid := reply.Kind()
	if invalid == nil && kind != protocol.KindReply {
		invalid = errors.New("expected a reply, not a request or notification")
	}
	if invalid == nil {
		invalid = reply.Validate()
	}
	if invalid != nil {
		r.Err = &Error{Code: protocol.CodeInternal, Data: json.RawMessage(`{"reason":"invalid_reply"}`),
			Message: fmt.Sprintf("plugin %s sent an invalid reply to %s: %v", plugin, p.method, invalid)}
		t.logf("calls: %s", r.Err.Message)
	} else {
		r.Value, r.Err = reply.Result, reply.Error
	}
	return r, true
}

// Expire removes every call whose deadline is at or before now, ordered by
// deadline, plugin, then id. No requester runs until all removals are complete.
func (t *Table) Expire(now time.Time) []Result {
	calls := t.selectCalls(func(p pendingCall) bool { return !p.deadline.After(now) })
	return t.fail(calls, now, func(p pendingCall) *Error {
		t.timeouts++
		return &Error{Code: protocol.CodeTimeout, Message: fmt.Sprintf("plugin %s did not reply to %s within %s", p.handle.Plugin, p.method, p.timeout)}
	})
}

// DropPlugin cancels all requests to plugin, ordered by id. The transport's
// diagnostic reason appears in Message; Data carries the stable plugin_exited
// reason used by programs. Other plugins' calls and the id counter are untouched.
func (t *Table) DropPlugin(plugin PluginID, reason string) []Result {
	delete(t.orphanLogs, plugin)
	calls := t.matchingCalls(func(p pendingCall) bool { return p.handle.Plugin == plugin })
	slices.SortFunc(calls, func(a, b pendingCall) int { return compareID(a.handle.ID, b.handle.ID) })
	return t.fail(calls, t.clock.Now(), func(p pendingCall) *Error {
		return &Error{Code: protocol.CodeCancelled, Data: json.RawMessage(`{"reason":"plugin_exited"}`), Message: fmt.Sprintf("plugin %s exited: %s", plugin, reason)}
	})
}

// Cancel forgets h without sending anything or producing a Result. It reports
// whether the call was pending; any later reply for it is an orphan.
func (t *Table) Cancel(h Handle) bool {
	if _, ok := t.pending[h]; !ok {
		return false
	}
	t.remove(h)
	return true
}

// Close permanently refuses Sends and returns shutdown cancellations for every
// pending request, ordered by deadline, plugin, id. Repeated Close is harmless.
func (t *Table) Close() []Result {
	t.closed = true
	calls := t.selectCalls(func(pendingCall) bool { return true })
	return t.fail(calls, t.clock.Now(), func(pendingCall) *Error {
		return &Error{Code: protocol.CodeCancelled, Data: json.RawMessage(`{"reason":"shutdown"}`), Message: "husk is shutting down"}
	})
}

// NextSweep claims the single sweep slot when calls pend and none is in flight.
// A true result means the caller must schedule a sweep after the returned delay.
// It schedules nothing itself; SweepCmd is the Bubble Tea adapter.
func (t *Table) NextSweep() (time.Duration, bool) {
	if t.closed || len(t.pending) == 0 || t.sweepInFlight {
		return 0, false
	}
	t.sweepInFlight = true
	return SweepInterval, true
}

// Sweep acknowledges a scheduled sweep, clears its in-flight flag, and expires
// calls using Clock.Now. The loop delivers all results before calling SweepCmd
// again, allowing result handlers to Send or Cancel on the same table.
func (t *Table) Sweep() []Result {
	t.sweepInFlight = false
	return t.Expire(t.clock.Now())
}

// Stats returns pending counts and lifetime timeout/orphan counters.
func (t *Table) Stats() Stats {
	counts := make(map[PluginID]int, len(t.perPlugin))
	for plugin, n := range t.perPlugin {
		counts[plugin] = n
	}
	return Stats{Pending: len(t.pending), PerPlugin: counts, Timeouts: t.timeouts, Orphans: t.orphans}
}

func (t *Table) remove(h Handle) {
	delete(t.pending, h)
	t.perPlugin[h.Plugin]--
	if t.perPlugin[h.Plugin] == 0 {
		delete(t.perPlugin, h.Plugin)
	}
}

func (p pendingCall) result(now time.Time) Result {
	return Result{Handle: p.handle, Requester: p.requester, Method: p.method, Elapsed: now.Sub(p.started)}
}

func (t *Table) matchingCalls(match func(pendingCall) bool) []pendingCall {
	var calls []pendingCall
	for _, p := range t.pending {
		if match(p) {
			calls = append(calls, p)
		}
	}
	return calls
}

func (t *Table) selectCalls(match func(pendingCall) bool) []pendingCall {
	calls := t.matchingCalls(match)
	slices.SortFunc(calls, func(a, b pendingCall) int {
		if c := a.deadline.Compare(b.deadline); c != 0 {
			return c
		}
		if a.handle.Plugin < b.handle.Plugin {
			return -1
		}
		if a.handle.Plugin > b.handle.Plugin {
			return 1
		}
		return compareID(a.handle.ID, b.handle.ID)
	})
	return calls
}

func compareID(a, b ID) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (t *Table) fail(calls []pendingCall, now time.Time, failure func(pendingCall) *Error) []Result {
	results := make([]Result, 0, len(calls))
	for _, p := range calls {
		t.remove(p.handle)
		r := p.result(now)
		r.Err = failure(p)
		results = append(results, r)
	}
	return results
}

func (t *Table) orphan(plugin PluginID, rawID string) {
	t.orphans++
	if t.logger == nil {
		return
	}
	now := t.clock.Now()
	previous, logged := t.orphanLogs[plugin]
	if logged && now.Sub(previous.last) < time.Second {
		previous.suppressed++
		t.orphanLogs[plugin] = previous
		return
	}
	if len(rawID) > 64 {
		rawID = rawID[:64] + "..."
	}
	t.logf("calls: orphan reply from plugin %q (id=%q; %d suppressed)", plugin, rawID, previous.suppressed)
	t.orphanLogs[plugin] = orphanLog{last: now}
}

func (t *Table) logf(format string, args ...any) {
	if t.logger != nil {
		t.logger(format, args...)
	}
}
