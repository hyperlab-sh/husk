package huskipc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Reasons a queued writer stops. Errors returned by Send, Err and passed to
// QueueOptions.OnFail wrap one of these (or the underlying write error), so
// callers test them with errors.Is.
var (
	// ErrSendQueueFull means the peer fell so far behind that the bounded
	// outbound queue overflowed. The connection is closed rather than
	// dropping individual messages, so a peer never sees a silently
	// inconsistent stream.
	ErrSendQueueFull = errors.New("huskipc: send queue full")
	// ErrWriteTimeout means a single write did not complete within
	// QueueOptions.WriteTimeout (the peer stopped reading).
	ErrWriteTimeout = errors.New("huskipc: write timed out")
	// ErrWriterClosed is returned by Send after Close or Abort.
	ErrWriterClosed = errors.New("huskipc: writer closed")
)

const (
	// DefaultSendQueueCapacity is the queue bound used when
	// QueueOptions.Capacity is zero.
	DefaultSendQueueCapacity = 256
	// DefaultWriteTimeout is the per-write deadline used when
	// QueueOptions.WriteTimeout is zero.
	DefaultWriteTimeout = 5 * time.Second
)

// QueueOptions configures NewQueuedConnWriter.
type QueueOptions struct {
	// Capacity is the maximum number of envelopes waiting to be written.
	Capacity int
	// WriteTimeout bounds each individual write. If w has a
	// SetWriteDeadline method (net.Conn) it is used; a watchdog that closes
	// Closer covers writers without one (pipes).
	WriteTimeout time.Duration
	// Closer is closed when the writer fails or is closed, unblocking any
	// write in progress. It is usually the connection itself.
	Closer io.Closer
	// OnFail is called once, on its own goroutine, when the writer fails
	// (queue overflow, write timeout or write error). It is not called for
	// Close or Abort.
	OnFail func(error)
}

// NewQueuedConnWriter returns a writer whose Send never blocks: envelopes are
// marshalled on the caller's goroutine, appended to a bounded queue and written
// by a dedicated goroutine. On overflow, write timeout or write error the writer
// fails permanently: Closer is closed, OnFail receives the reason, and every
// later Send returns it.
func NewQueuedConnWriter(w io.Writer, opts QueueOptions) *ConnWriter {
	if opts.Capacity <= 0 {
		opts.Capacity = DefaultSendQueueCapacity
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	q := &sendQueue{
		w:       w,
		closer:  opts.Closer,
		timeout: opts.WriteTimeout,
		onFail:  opts.OnFail,
		ch:      make(chan []byte, opts.Capacity),
		done:    make(chan struct{}),
	}
	go q.run()
	return &ConnWriter{w: w, q: q}
}

// Close flushes envelopes already queued, then closes the Closer. Send fails
// with ErrWriterClosed afterwards. It does not wait for the flush. On a
// synchronous writer it does nothing.
func (w *ConnWriter) Close() error {
	if w.q != nil {
		w.q.close()
	}
	return nil
}

// Abort discards queued envelopes and closes the Closer immediately, without
// calling OnFail. On a synchronous writer it does nothing.
func (w *ConnWriter) Abort() {
	if w.q != nil {
		w.q.finish(ErrWriterClosed, false)
	}
}

// Err reports why a queued writer stopped, or nil while it is running. It is
// always nil for a synchronous writer.
func (w *ConnWriter) Err() error {
	if w.q == nil {
		return nil
	}
	w.q.mu.Lock()
	defer w.q.mu.Unlock()
	return w.q.err
}

// Queued reports whether Send is asynchronous.
func (w *ConnWriter) Queued() bool { return w.q != nil }

type sendQueue struct {
	w       io.Writer
	closer  io.Closer
	timeout time.Duration
	onFail  func(error)
	ch      chan []byte // a nil entry asks run to close after the flush

	mu      sync.Mutex
	err     error
	closing bool
	done    chan struct{}
}

func (q *sendQueue) enqueue(data []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	if q.closing {
		return ErrWriterClosed
	}
	select {
	case q.ch <- data:
		return nil
	default:
		q.stopLocked(fmt.Errorf("%w (%d messages)", ErrSendQueueFull, cap(q.ch)), true)
		return q.err
	}
}

func (q *sendQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil || q.closing {
		return
	}
	q.closing = true
	select {
	case q.ch <- nil:
	default: // no room for the flush marker: close now
		q.stopLocked(ErrWriterClosed, false)
	}
}

func (q *sendQueue) finish(err error, notify bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err == nil {
		q.stopLocked(err, notify)
	}
}

// stopLocked records the terminal error and releases the connection. The
// close and callback run on a new goroutine so a failing Send never blocks
// its caller.
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
	for {
		select {
		case <-q.done:
			return
		case data := <-q.ch:
			if data == nil {
				q.finish(ErrWriterClosed, false)
				return
			}
			if err := q.write(data); err != nil {
				q.finish(err, true)
				return
			}
		}
	}
}

func (q *sendQueue) write(data []byte) error {
	if d, ok := q.w.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = d.SetWriteDeadline(time.Now().Add(q.timeout))
	}
	timeoutErr := fmt.Errorf("%w after %s", ErrWriteTimeout, q.timeout)
	var timedOut atomic.Bool
	watchdog := time.AfterFunc(q.timeout, func() {
		timedOut.Store(true)
		q.finish(timeoutErr, true)
	})
	_, err := q.w.Write(data)
	watchdog.Stop()
	switch {
	case timedOut.Load(), errors.Is(err, os.ErrDeadlineExceeded):
		return timeoutErr
	case err != nil:
		return fmt.Errorf("huskipc: write: %w", err)
	}
	return nil
}
