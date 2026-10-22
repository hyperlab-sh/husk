// Package calls owns outstanding Husk-to-plugin requests. Its table is confined
// to the host loop: no goroutines, locks, blocking I/O or requester callbacks.
// Routing, liveness checks and delivery of returned Results belong to the loop.
package calls

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// PluginID identifies the plugin whose stdin receives a request.
type PluginID string

// ID is a positive numeric wire id below 2^53, unique for this table's lifetime.
type ID uint64

// Requester is an opaque module name resolved by the loop, not by Calls.
type Requester string

// Handle identifies one request within the plugin's channel.
type Handle struct {
	Plugin PluginID
	ID     ID
}

// Options configures one Send. Zero Timeout selects DefaultTimeout.
type Options struct {
	Timeout   time.Duration
	Requester Requester
}

// TableOptions configures one instance's table. Nil Clock uses the system clock;
// nil Logger is a no-op. Zero caps select the specification's defaults. Negative
// caps are programmer errors and cause New to panic. Logger must not block or
// reenter the table. Clock remains a value interface for model-purity tests.
type TableOptions struct {
	Clock               Clock
	Logger              func(format string, args ...any)
	MaxPending          int
	MaxPendingPerPlugin int
}

// Result is the sole outcome of a successful Send, unless cancelled locally.
// Exactly one of Value or Err is present; JSON null is a valid, non-nil Value.
type Result struct {
	Handle    Handle
	Requester Requester
	Method    string
	Value     json.RawMessage
	Err       *Error
	Elapsed   time.Duration
}

// Error is the protocol error object, including code-specific structured data.
type Error = protocol.Error

// Outbox queues a request on a plugin's stdin. Send must not block or reenter the
// table. A failed Send reports a missing/failed writer, not a pending request.
type Outbox interface {
	Send(plugin PluginID, req protocol.Message) error
}

// Clock provides the loop's notion of time. It never schedules work.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

const (
	// DefaultTimeout applies when Options.Timeout is zero.
	DefaultTimeout = 10 * time.Second
	// MinTimeout is the shortest allowed nonzero request timeout.
	MinTimeout = time.Second
	// MaxTimeout is the longest allowed request timeout.
	MaxTimeout = 60 * time.Second
	// MaxPending bounds requests across the instance by default.
	MaxPending = 1024
	// MaxPendingPerPlugin bounds requests to one plugin by default.
	MaxPendingPerPlugin = 64
	// SweepInterval bounds deadline lateness while calls are pending.
	SweepInterval = 250 * time.Millisecond
)

const maxID ID = 1<<53 - 1

var (
	// ErrBusy means the overall cap is full, or the numeric id space exhausted.
	ErrBusy = errors.New("calls: pending capacity exhausted")
	// ErrPluginBusy means the target's per-plugin cap is full.
	ErrPluginBusy = errors.New("calls: plugin pending capacity exhausted")
	// ErrUnavailable wraps an outbox failure and its original cause.
	ErrUnavailable = errors.New("calls: plugin writer unavailable")
	// ErrBadOptions means the requester is empty or the timeout outside bounds.
	ErrBadOptions = errors.New("calls: invalid request options")
	// ErrClosed means shutdown has closed the table.
	ErrClosed = errors.New("calls: table closed")
)

// Stats is a detached snapshot of pending counts and lifetime failure counters.
// Mutating PerPlugin never changes the table. It contains only nonzero counts.
type Stats struct {
	Pending   int              `json:"pending"`
	PerPlugin map[PluginID]int `json:"per_plugin"`
	Timeouts  uint64           `json:"timeouts"`
	Orphans   uint64           `json:"orphans"`
}

// ReplyMsg is posted by a plugin stdout reader; only the loop resolves it.
type ReplyMsg struct {
	Plugin PluginID
	Reply  protocol.Message
}

// SweepMsg is produced by SweepCmd; the loop handles it with Table.Sweep.
type SweepMsg struct{}
