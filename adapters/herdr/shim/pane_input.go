package shim

import (
	"context"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const maxPaneInputBytes = 4 * 1024 * 1024

// paneInput retains byte order without making the stdin reader wait for a PTY.
// The writer takes bounded chunks; the cap measures queued, not in-flight bytes.
type paneInput struct {
	mu          sync.Mutex
	data        []byte
	wake        chan struct{}
	warned      bool
	lastWarning time.Time
}

func newPaneInput() *paneInput { return &paneInput{wake: make(chan struct{}, 1)} }

// append drops a whole newest chunk rather than disrupting previously queued
// escape sequences. The return value requests a rate-limited warning.
func (q *paneInput) append(b []byte, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(b) > maxPaneInputBytes-len(q.data) {
		warn := !q.warned || now.Sub(q.lastWarning) >= time.Second
		if warn {
			q.warned = true
			q.lastWarning = now
		}
		return warn
	}
	q.data = append(q.data, b...)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return false
}

func (q *paneInput) next(ctx context.Context) ([]byte, bool) {
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		q.mu.Lock()
		if len(q.data) > 0 {
			n := min(len(q.data), protocol.MaxTerminalBytes)
			b := append([]byte(nil), q.data[:n]...)
			q.data = q.data[n:]
			if len(q.data) == 0 {
				q.data = nil
			}
			q.mu.Unlock()
			return b, true
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
			return nil, false
		}
	}
}
