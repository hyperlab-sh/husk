package protocol

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	created chan struct{}
}
type fakeTimer struct {
	clock  *fakeClock
	at     time.Time
	fn     func()
	active bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1000, 0), created: make(chan struct{}, 20)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	timer := &fakeTimer{clock: c, at: c.now.Add(d), fn: fn, active: true}
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	c.created <- struct{}{}
	return timer
}
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := t.active
	t.active = false
	return active
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var callbacks []func()
	for _, timer := range c.timers {
		if timer.active && !timer.at.After(c.now) {
			timer.active = false
			callbacks = append(callbacks, timer.fn)
		}
	}
	c.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
}

type gatedWriter struct {
	started                           chan struct{}
	unblock                           chan struct{}
	closed                            chan struct{}
	startOnce, closeOnce, releaseOnce sync.Once
	mu                                sync.Mutex
	buffer                            bytes.Buffer
	deadline                          time.Time
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{started: make(chan struct{}), unblock: make(chan struct{}), closed: make(chan struct{})}
}
func (w *gatedWriter) Write(b []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.unblock
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(b)
}
func (w *gatedWriter) release() { w.releaseOnce.Do(func() { close(w.unblock) }) }
func (w *gatedWriter) Close() error {
	w.release()
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}
func (w *gatedWriter) SetWriteDeadline(at time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = at
	return nil
}

func testNotification() Message { return Message{Method: MethodSessionPing} }

func TestQueueOverflow(t *testing.T) {
	clock, sink := newFakeClock(), newGatedWriter()
	failed := make(chan error, 2)
	w := NewQueuedWriter(sink, QueueOptions{Capacity: 1, Clock: clock, OnFail: func(err error) { failed <- err }})
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	<-sink.started
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	if err := w.Send(testNotification()); !errors.Is(err, ErrSendQueueFull) {
		t.Fatal(err)
	}
	if err := <-failed; !errors.Is(err, ErrSendQueueFull) {
		t.Fatal(err)
	}
	<-w.Done()
	if !errors.Is(w.Err(), ErrSendQueueFull) || !errors.Is(w.Send(testNotification()), ErrSendQueueFull) {
		t.Fatal(w.Err())
	}
	w.Abort()
	_ = w.Close()
	select {
	case err := <-failed:
		t.Fatalf("duplicate failure: %v", err)
	default:
	}
}

func TestQueueWriteTimeoutInjectedClock(t *testing.T) {
	clock, sink := newFakeClock(), newGatedWriter()
	failed := make(chan error, 1)
	timeout := 10 * time.Second
	deadline := clock.Now().Add(timeout)
	w := NewQueuedWriter(sink, QueueOptions{Capacity: 2, WriteTimeout: timeout, Clock: clock, OnFail: func(err error) { failed <- err }})
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	<-clock.created
	<-sink.started
	sink.mu.Lock()
	gotDeadline := sink.deadline
	sink.mu.Unlock()
	if gotDeadline != deadline {
		t.Fatalf("deadline = %v want %v", gotDeadline, deadline)
	}
	clock.advance(timeout - time.Nanosecond)
	if w.Err() != nil {
		t.Fatal("timed out early")
	}
	clock.advance(time.Nanosecond)
	if err := <-failed; !errors.Is(err, ErrWriteTimeout) {
		t.Fatal(err)
	}
	<-w.Done()
	if !errors.Is(w.Send(testNotification()), ErrWriteTimeout) {
		t.Fatal(w.Err())
	}
}

func TestQueueCloseFlushesFullQueue(t *testing.T) {
	clock, sink := newFakeClock(), newGatedWriter()
	w := NewQueuedWriter(sink, QueueOptions{Capacity: 1, Clock: clock, OnFail: func(err error) { t.Errorf("unexpected OnFail: %v", err) }})
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	<-sink.started
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Send(testNotification()); !errors.Is(err, ErrWriterClosed) {
		t.Fatal(err)
	}
	sink.release()
	<-w.Done()
	<-sink.closed
	sink.mu.Lock()
	got := sink.buffer.String()
	sink.mu.Unlock()
	want := "{\"method\":\"session.ping\"}\n"
	if got != want+want {
		t.Fatalf("flush = %q", got)
	}
	clock.advance(time.Hour)
	if !errors.Is(w.Err(), ErrWriterClosed) {
		t.Fatal(w.Err())
	}
}

func TestQueueAbort(t *testing.T) {
	sink := newGatedWriter()
	w := NewQueuedWriter(sink, QueueOptions{Clock: newFakeClock(), OnFail: func(err error) { t.Errorf("unexpected OnFail: %v", err) }})
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	<-sink.started
	w.Abort()
	<-w.Done()
	<-sink.closed
	if !errors.Is(w.Send(testNotification()), ErrWriterClosed) {
		t.Fatal(w.Err())
	}
}

type errorWriter struct {
	err   error
	short bool
}

func (w errorWriter) Write(b []byte) (int, error) {
	if w.short {
		return len(b) - 1, nil
	}
	return 0, w.err
}

func TestWriterFailures(t *testing.T) {
	writeErr := errors.New("broken pipe")
	for _, tc := range []struct {
		sink io.Writer
		want error
	}{
		{errorWriter{err: writeErr}, writeErr}, {errorWriter{short: true}, io.ErrShortWrite},
	} {
		if err := NewWriter(tc.sink).Send(testNotification()); !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
		failed := make(chan error, 1)
		w := NewQueuedWriter(tc.sink, QueueOptions{Clock: newFakeClock(), OnFail: func(err error) { failed <- err }})
		if err := w.Send(testNotification()); err != nil {
			t.Fatal(err)
		}
		if err := <-failed; !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
		<-w.Done()
	}
}

func TestSynchronousWriter(t *testing.T) {
	var sink bytes.Buffer
	w := NewWriter(&sink)
	if w.Queued() || w.Done() != nil || w.Err() != nil {
		t.Fatal("synchronous writer has queue state")
	}
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "{\"method\":\"session.ping\"}\n" {
		t.Fatal(sink.String())
	}
	w.Abort()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Send(Message{}); err == nil {
		t.Fatal("invalid envelope written")
	}
}
