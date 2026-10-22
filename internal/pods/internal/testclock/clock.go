// Package testclock provides a deterministic, concurrent clock for Pods tests.
package testclock

import (
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"sync"
	"time"
)

// Clock schedules callbacks without creating timers or sleeping.
type Clock struct {
	mu         sync.Mutex
	now        time.Time
	timers     []*timer
	registered chan struct{}
}
type timer struct {
	clock    *Clock
	at       time.Time
	duration time.Duration
	fn       func()
	active   bool
}

// New returns a clock with a stable nonzero epoch.
func New() *Clock { return &Clock{now: time.Unix(1000, 0), registered: make(chan struct{}, 128)} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// AfterFunc registers a callback for a later Advance.
func (c *Clock) AfterFunc(d time.Duration, fn func()) protocol.Timer {
	c.mu.Lock()
	t := &timer{clock: c, at: c.now.Add(d), duration: d, fn: fn, active: true}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	select {
	case c.registered <- struct{}{}:
	default:
	}
	return t
}
func (t *timer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

// Advance runs due callbacks outside the clock lock.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var work []func()
	for _, t := range c.timers {
		if t.active && !t.at.After(c.now) {
			t.active = false
			work = append(work, t.fn)
		}
	}
	c.mu.Unlock()
	for _, fn := range work {
		fn()
	}
}

// Active reports whether a duration has an outstanding timer.
func (c *Clock) Active(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.timers {
		if t.active && t.duration == d {
			return true
		}
	}
	return false
}

// Await waits for registration, not time, until a timer of d is active.
func (c *Clock) Await(d time.Duration) {
	for {
		if c.Active(d) {
			return
		}
		<-c.registered
	}
}
