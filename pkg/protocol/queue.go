package protocol

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrSendQueueFull means the peer's bounded output queue overflowed.
	ErrSendQueueFull = errors.New("protocol: send queue full")
	// ErrWriteTimeout means a write exceeded its deadline.
	ErrWriteTimeout = errors.New("protocol: write timed out")
	// ErrWriterClosed means Close or Abort stopped a queued writer.
	ErrWriterClosed = errors.New("protocol: writer closed")
)

// Timer is the stoppable watchdog supplied by a Clock.
type Timer interface{ Stop() bool }

// Clock supplies time and watchdogs. Tests may inject a deterministic clock.
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}

type wallClock struct{}

func (wallClock) Now() time.Time                            { return time.Now() }
func (wallClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// QueueOptions configures queue bounds. The transport owns policy values.
type QueueOptions struct {
	// Capacity bounds waiting messages, excluding the in-progress write.
	// Nonpositive values default to 1024.
	Capacity int
	// WriteTimeout bounds each write; nonpositive values default to 5 seconds.
	WriteTimeout time.Duration
	// Closer must unblock Write when closed. If nil, w's io.Closer is used.
	// A blocking writer without a Closer cannot be forcibly interrupted.
	Closer io.Closer
	// OnFail runs once asynchronously on overflow, timeout or write failure.
	// Close and Abort do not call it.
	OnFail func(error)
	// Clock defaults to the system clock.
	Clock Clock
}

// NewQueuedWriter creates a bounded writer that fails permanently on overflow
// or a transport failure. Its Send never waits for I/O or for OnFail.
func NewQueuedWriter(w io.Writer, opts QueueOptions) *Writer {
	if opts.Capacity <= 0 {
		opts.Capacity = 1024
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 5 * time.Second
	}
	if opts.Clock == nil {
		opts.Clock = wallClock{}
	}
	if opts.Closer == nil {
		opts.Closer, _ = w.(io.Closer)
	}
	q := &sendQueue{w: w, closer: opts.Closer, timeout: opts.WriteTimeout,
		onFail: opts.OnFail, clock: opts.Clock, ch: make(chan []byte, opts.Capacity), done: make(chan struct{}), stopped: make(chan struct{})}
	go q.run()
	return &Writer{w: w, q: q}
}

// Close stops accepting messages and flushes queued lines before closing the
// transport. It does not wait for that flush. Synchronous writers are unchanged.
func (w *Writer) Close() error {
	if w.q != nil {
		w.q.mu.Lock()
		if !w.q.closing && w.q.err == nil {
			w.q.closing = true
			close(w.q.ch)
		}
		w.q.mu.Unlock()
	}
	return nil
}

// Abort discards queued lines and closes the transport asynchronously.
func (w *Writer) Abort() {
	if w.q != nil {
		w.q.finish(ErrWriterClosed, false)
	}
}

// Err returns the permanent queue failure, or nil while it is running.
func (w *Writer) Err() error {
	if w.q == nil {
		return nil
	}
	w.q.mu.Lock()
	defer w.q.mu.Unlock()
	return w.q.err
}

// Queued reports whether Send is asynchronous.
func (w *Writer) Queued() bool { return w.q != nil }

// Done returns a channel closed when the queue's worker exits. It is nil for a
// synchronous writer. Use Abort or Close before waiting for worker shutdown.
func (w *Writer) Done() <-chan struct{} {
	if w.q == nil {
		return nil
	}
	return w.q.stopped
}

type sendQueue struct {
	w       io.Writer
	closer  io.Closer
	timeout time.Duration
	onFail  func(error)
	clock   Clock
	ch      chan []byte
	done    chan struct{}
	stopped chan struct{}
	mu      sync.Mutex
	err     error
	closing bool
}

func (q *sendQueue) enqueue(b []byte) error { return q.offer(b, true) }

func (q *sendQueue) offer(b []byte, failFull bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	if q.closing {
		return ErrWriterClosed
	}
	select {
	case q.ch <- b:
		return nil
	default:
		if !failFull {
			return ErrSendQueueFull
		}
		q.stopLocked(fmt.Errorf("%w (%d messages)", ErrSendQueueFull, cap(q.ch)), true)
		return q.err
	}
}

func (q *sendQueue) finish(err error, notify bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err == nil {
		q.stopLocked(err, notify)
	}
}

func (q *sendQueue) stopLocked(err error, notify bool) {
	q.err = err
	close(q.done)
	go func() {
		if q.closer != nil {
			_ = q.closer.Close()
		}
		if notify && q.onFail != nil {
			q.onFail(err)
		}
	}()
}

func (q *sendQueue) run() {
	defer close(q.stopped)
	for {
		select {
		case <-q.done:
			return
		case b, ok := <-q.ch:
			if !ok {
				q.finish(ErrWriterClosed, false)
				return
			}
			select {
			case <-q.done:
				return
			default:
			}
			if err := q.write(b); err != nil {
				q.finish(err, true)
				return
			}
		}
	}
}

func (q *sendQueue) write(b []byte) error {
	if d, ok := q.w.(interface{ SetWriteDeadline(time.Time) error }); ok {
		if err := d.SetWriteDeadline(q.clock.Now().Add(q.timeout)); err != nil {
			return err
		}
	}
	timeoutErr := fmt.Errorf("%w after %s", ErrWriteTimeout, q.timeout)
	var expired atomic.Bool
	watchdog := q.clock.AfterFunc(q.timeout, func() {
		expired.Store(true)
		q.finish(timeoutErr, true)
	})
	err := writeLine(q.w, b)
	watchdog.Stop()
	if expired.Load() || errors.Is(err, os.ErrDeadlineExceeded) {
		return timeoutErr
	}
	return err
}
