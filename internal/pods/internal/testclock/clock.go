// Package testclock provides a deterministic, concurrent clock for Pods tests.
package testclock

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
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

// Await waits for registration, not simulated time. The context deadline is
// only a test-hang guard; process behavior still uses the injected clock.
func (c *Clock) Await(t testing.TB, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		if c.Active(d) {
			return
		}
		select {
		case <-c.registered:
		case <-ctx.Done():
			t.Fatalf("timer %s was not registered: %v", d, ctx.Err())
		}
	}
}

// Receive bounds a test's channel wait without sleeping or advancing behavior.
func Receive[T any](t testing.TB, ch <-chan T) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("channel wait: %v", ctx.Err())
		var zero T
		return zero
	}
}
