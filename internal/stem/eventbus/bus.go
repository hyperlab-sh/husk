// Package eventbus is the Stem Event bus: fire-and-forget fan-out of named
// events to manifest subscribers and connection subscribers, coalesced per
// loop tick. A tick is the end of one Update call; the loop calls Flush there.
//
// The bus writes only through contracts.Notifier and reads liveness through an
// injected view. It imports neither Pods, State, Store nor Calls. It holds no
// history. Publish and Flush are meant for the loop goroutine; Subscribe,
// cancel, SetTable and Stats are safe from any goroutine.
package eventbus

import (
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// MaxNonCoalescing is how many non-coalescing events one subscriber receives
// per tick; the excess is dropped.
const MaxNonCoalescing = 256

// Event is one published event.
type Event struct {
	// Name is the event name, "state.changed" or "notes.saved".
	Name string
	// Key is an optional coalescing key; "" means the name alone.
	Key string
	// Source is the plugin id, "husk" for core publishers, "user" for a CLI emit.
	Source string
	// Data is the payload object. Publish takes ownership; do not mutate it.
	Data map[string]any
}

// Liveness is the read-only view of plugin liveness (the husk.pods slice).
type Liveness interface {
	// Live reports whether the plugin's status is live.
	Live(pluginID string) bool
}

// Sinks resolves the notifier that writes to a plugin's stdin.
type Sinks interface {
	// Notifier returns the plugin's notifier, or false when it has none.
	Notifier(pluginID string) (contracts.Notifier, bool)
}

// Table is the manifest-derived configuration, replaced as a whole on config
// reload or plugin add.
type Table struct {
	// Subscribe maps a plugin id to its subscription patterns.
	Subscribe map[string][]string
	// NoCoalesce lists event names a publisher declared with "coalesce": false.
	NoCoalesce []string
}

// Options configures a Bus. Liveness and Sinks are required for manifest
// subscribers; a nil Liveness treats every plugin as live. A nil Logger is
// a no-op and must not block or call back into the bus.
type Options struct {
	Liveness Liveness
	Sinks    Sinks
	Logger   func(format string, args ...any)
}

// Stats counts what the bus did, for diagnostics and tests.
type Stats struct {
	Published   uint64 // events accepted by Publish
	Delivered   uint64 // event.deliver notifications handed to a Notifier successfully
	Coalesced   uint64 // events replaced by a later one with the same (Name, Key)
	NotLive     uint64 // plugin deliveries skipped because the plugin was not live
	Overflow    uint64 // non-coalescing events dropped past MaxNonCoalescing
	NotifyError uint64 // deliveries a Notifier refused or that could not be encoded
	NoSink      uint64 // queued deliveries dropped because the target had no sink
}

type conn struct {
	id        uint64
	n         contracts.Notifier
	pattern   string
	cancelled atomic.Bool
}

type target struct {
	plugin string // set for manifest subscribers
	conn   *conn  // set for connection subscribers
}

type entry struct {
	ev     Event
	params json.RawMessage
	err    error
	done   bool
}

type item struct{ e *entry }

type batch struct {
	items   []item
	index   map[[2]string]int
	nonCoal int
	dropped int
}

// Bus is the Event bus.
type Bus struct {
	opts Options

	mu         sync.Mutex
	subs       map[string][]string
	noCoalesce map[string]struct{}
	conns      []*conn
	nextConn   uint64
	pending    map[target]*batch
	order      []target
	stats      Stats
}

// New returns an empty bus.
func New(opts Options) *Bus {
	return &Bus{opts: opts, pending: map[target]*batch{}}
}

func (b *Bus) logf(format string, args ...any) {
	if b.opts.Logger != nil {
		b.opts.Logger(format, args...)
	}
}

// SetTable replaces the manifest subscriptions and non-coalescing names. A
// malformed pattern or name refuses the whole table and changes nothing.
func (b *Bus) SetTable(t Table) error {
	subs := make(map[string][]string, len(t.Subscribe))
	for id, patterns := range t.Subscribe {
		if id == "" {
			return invalid("empty plugin id in subscription table")
		}
		for _, p := range patterns {
			if err := ValidatePattern(p); err != nil {
				return err
			}
		}
		subs[id] = slices.Clone(patterns)
	}
	nc := make(map[string]struct{}, len(t.NoCoalesce))
	for _, n := range t.NoCoalesce {
		if err := ValidateName(n); err != nil {
			return err
		}
		nc[n] = struct{}{}
	}
	b.mu.Lock()
	b.subs, b.noCoalesce = subs, nc
	b.mu.Unlock()
	return nil
}

// Subscribe adds a connection-lifetime subscription. cancel removes it and is
// idempotent; after cancel returns no new delivery starts (one already inside
// Notify may complete). A malformed pattern is an invalid_params error and
// registers nothing. Connection subscribers are not liveness-gated.
func (b *Bus) Subscribe(n contracts.Notifier, pattern string) (cancel func(), err error) {
	if n == nil {
		return nil, invalid("nil notifier")
	}
	if err := ValidatePattern(pattern); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.nextConn++
	c := &conn{id: b.nextConn, n: n, pattern: pattern}
	b.conns = append(b.conns, c)
	b.mu.Unlock()
	return func() {
		c.cancelled.Store(true)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.conns = slices.DeleteFunc(b.conns, func(x *conn) bool { return x == c })
		delete(b.pending, target{conn: c})
		b.order = slices.DeleteFunc(b.order, func(t target) bool { return t.conn == c })
	}, nil
}

// Publish queues e for every matching subscriber and never blocks or reports
// delivery. Plugin subscribers that are not live now are skipped (no replay).
// An event with a malformed name is dropped and logged.
func (b *Bus) Publish(e Event) {
	if err := ValidateName(e.Name); err != nil {
		b.logf("eventbus: dropping event: %v", err)
		return
	}
	en := &entry{ev: e}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.Published++
	_, noCo := b.noCoalesce[e.Name]

	ids := make([]string, 0, len(b.subs))
	for id := range b.subs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if !slices.ContainsFunc(b.subs[id], func(p string) bool { return Matches(p, e.Name) }) {
			continue
		}
		if b.opts.Liveness != nil && !b.opts.Liveness.Live(id) {
			b.stats.NotLive++
			continue
		}
		b.enqueue(target{plugin: id}, en, noCo)
	}
	for _, c := range b.conns {
		if Matches(c.pattern, e.Name) {
			b.enqueue(target{conn: c}, en, noCo)
		}
	}
}

func (b *Bus) enqueue(t target, en *entry, noCo bool) {
	bt := b.pending[t]
	if bt == nil {
		bt = &batch{index: map[[2]string]int{}}
		b.pending[t] = bt
		b.order = append(b.order, t)
	}
	if noCo {
		if bt.nonCoal >= MaxNonCoalescing {
			bt.dropped++
			b.stats.Overflow++
			return
		}
		bt.nonCoal++
		bt.items = append(bt.items, item{en})
		return
	}
	k := [2]string{en.ev.Name, en.ev.Key}
	if i, ok := bt.index[k]; ok {
		bt.items[i] = item{en}
		b.stats.Coalesced++
		return
	}
	bt.index[k] = len(bt.items)
	bt.items = append(bt.items, item{en})
}

// Stats returns a copy of the counters.
func (b *Bus) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

type deliverParams struct {
	Name   string         `json:"name"`
	Source string         `json:"source"`
	Data   map[string]any `json:"data"`
}

func (en *entry) message() (protocol.Message, error) {
	if !en.done {
		en.done = true
		d := en.ev.Data
		if d == nil {
			d = map[string]any{}
		}
		en.params, en.err = json.Marshal(deliverParams{Name: en.ev.Name, Source: en.ev.Source, Data: d})
	}
	if en.err != nil {
		return protocol.Message{}, en.err
	}
	return protocol.Message{Method: protocol.MethodEventDeliver, Params: en.params}, nil
}

type work struct {
	t  target
	bt *batch
}

// Flush delivers the tick's coalesced batches as event.deliver notifications
// and clears them. It never waits on a subscriber: a refused notification is
// counted and dropped, and the next one is still tried. Call it on the loop
// at the end of an Update, after State's flush.
func (b *Bus) Flush() {
	b.mu.Lock()
	jobs := make([]work, 0, len(b.order))
	for _, t := range b.order {
		jobs = append(jobs, work{t, b.pending[t]})
	}
	b.pending = map[target]*batch{}
	b.order = nil
	b.mu.Unlock()

	var delivered, failed, noSink uint64
	for _, j := range jobs {
		var n contracts.Notifier
		switch {
		case j.t.conn != nil:
			if j.t.conn.cancelled.Load() {
				continue
			}
			n = j.t.conn.n
		case b.opts.Sinks != nil:
			n, _ = b.opts.Sinks.Notifier(j.t.plugin)
		}
		if j.bt.dropped > 0 {
			b.logf("eventbus: dropped %d non-coalescing events for %s past the cap of %d", j.bt.dropped, label(j.t), MaxNonCoalescing)
		}
		if n == nil {
			noSink += uint64(len(j.bt.items))
			continue
		}
		for _, it := range j.bt.items {
			if j.t.conn != nil && j.t.conn.cancelled.Load() {
				break
			}
			m, err := it.e.message()
			if err == nil {
				err = n.Notify(m)
			}
			if err != nil {
				failed++
				continue
			}
			delivered++
		}
	}
	b.mu.Lock()
	b.stats.Delivered += delivered
	b.stats.NotifyError += failed
	b.stats.NoSink += noSink
	b.mu.Unlock()
}

func label(t target) string {
	if t.conn != nil {
		return "a watching connection"
	}
	return "plugin " + t.plugin
}
