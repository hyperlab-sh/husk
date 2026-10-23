package services

import (
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

// WindowLimiter is the production contracts.Limiter for the per-plugin
// budget shared by every plugin-callable Services method. It keeps one tumbling one-second
// window per key: the first call under a key opens the window, the first
// limit calls inside it pass, and the window resets — whole, not partially —
// once a full second has passed since it opened. This is the fixed-window
// reading of services.md's "20 per second per plugin": a plugin may spend its
// whole budget in one burst and then waits out the remainder of that second;
// it cannot average above the cap. Time comes from the injected Clock, so
// tests move the window without timers or sleeps, and the mutex makes Allow
// safe under the Server's concurrent readers.
type WindowLimiter struct {
	clock contracts.Clock
	limit int

	mu     sync.Mutex
	window map[string]limWindow
}

type limWindow struct {
	start time.Time
	count int
}

// NewWindowLimiter returns a limiter allowing limit calls per key per second.
// It panics on a non-positive limit or a nil clock: both are composition
// bugs, not inputs.
func NewWindowLimiter(clock contracts.Clock, limit int) *WindowLimiter {
	if clock == nil {
		panic("services: rate limiter requires a clock")
	}
	if limit <= 0 {
		panic("services: rate limit must be positive")
	}
	return &WindowLimiter{clock: clock, limit: limit, window: map[string]limWindow{}}
}

// Allow reports whether one more call under key may proceed now, and counts
// it. Keys are plugin ids resolved by the Server, never params values.
func (l *WindowLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	w, ok := l.window[key]
	if !ok || now.Sub(w.start) >= time.Second {
		w = limWindow{start: now}
	}
	if w.count >= l.limit {
		l.window[key] = w
		return false
	}
	w.count++
	l.window[key] = w
	return true
}
