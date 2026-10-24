package workbench

import (
	tea "charm.land/bubbletea/v2"
	"sync"
	"sync/atomic"
)

// programRef lets the host's Post reach the tea.Program. The host is built
// around Post, so a worker may post before the program is set: post waits for
// set or for the gate. Once the gate closes (the program stopped reading, or
// shutdown began) post returns false without blocking, which Config.Post
// defines as "the loop is gone" (D62).
type programRef struct {
	mu     sync.RWMutex
	p      *tea.Program
	ready  chan struct{}
	gate   chan struct{}
	once   sync.Once
	closed atomic.Bool
	// waiting counts posts that reached the wait for set or the gate; tests
	// use it to know a post is parked without sleeping.
	waiting atomic.Int32
}

func newProgramRef() *programRef {
	return &programRef{ready: make(chan struct{}), gate: make(chan struct{})}
}

func (r *programRef) set(p *tea.Program) {
	r.mu.Lock()
	r.p = p
	r.mu.Unlock()
	select {
	case <-r.ready:
	default:
		close(r.ready)
	}
}

// closeGate is idempotent and safe from any goroutine.
func (r *programRef) closeGate() {
	r.closed.Store(true)
	r.once.Do(func() { close(r.gate) })
}

func (r *programRef) post(msg any) bool {
	if r.closed.Load() {
		return false
	}
	r.waiting.Add(1)
	defer r.waiting.Add(-1)
	select {
	case <-r.ready:
	case <-r.gate:
		return false
	}
	r.mu.RLock()
	p := r.p
	r.mu.RUnlock()
	p.Send(msg)
	return !r.closed.Load()
}
