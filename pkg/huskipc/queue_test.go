package huskipc

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// failRecorder captures OnFail calls.
type failRecorder struct {
	mu   sync.Mutex
	errs []error
	ch   chan error
}

func newFailRecorder() *failRecorder { return &failRecorder{ch: make(chan error, 8)} }

func (f *failRecorder) record(err error) {
	f.mu.Lock()
	f.errs = append(f.errs, err)
	f.mu.Unlock()
	f.ch <- err
}

func (f *failRecorder) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-f.ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("OnFail was not called")
		return nil
	}
}

func (f *failRecorder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.errs)
}

func toastEnvelope(i int) Envelope {
	return Envelope{Type: "toast", Toast: &ToastPayload{PluginID: "p", Title: strings.Repeat("x", i)}}
}

// A peer that never reads must not block Send: net.Pipe has no buffering, so a
// synchronous writer would hang on the first line.
func TestQueuedSendDoesNotBlockOnNonReadingPeer(t *testing.T) {
	host, peer := net.Pipe()
	defer peer.Close()
	fails := newFailRecorder()
	w := NewQueuedConnWriter(host, QueueOptions{Capacity: 64, WriteTimeout: 10 * time.Second, Closer: host, OnFail: fails.record})

	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := w.Send(toastEnvelope(i)); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("50 sends to a non-reading peer took %s", elapsed)
	}

	// The peer starts reading late and still receives every line, in order.
	r := bufio.NewReader(peer)
	for i := 0; i < 50; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		want := `{"type":"toast","toast":{"plugin_id":"p","level":"","title":"` + strings.Repeat("x", i) + `","message":""}}` + "\n"
		if line != want {
			t.Fatalf("line %d = %q, want %q", i, line, want)
		}
	}
	if fails.count() != 0 || w.Err() != nil {
		t.Fatalf("healthy writer failed: %v", w.Err())
	}
}

func TestQueuedSendOverflowDisconnects(t *testing.T) {
	host, peer := net.Pipe()
	defer peer.Close()
	fails := newFailRecorder()
	const capacity = 4
	w := NewQueuedConnWriter(host, QueueOptions{Capacity: capacity, WriteTimeout: 10 * time.Second, Closer: host, OnFail: fails.record})

	// At most one line is in flight inside the writer goroutine and capacity
	// lines wait in the queue; capacity+2 sends must overflow.
	var overflowAt = -1
	for i := 0; i < capacity+2; i++ {
		err := w.Send(toastEnvelope(1))
		if err != nil {
			if !errors.Is(err, ErrSendQueueFull) {
				t.Fatalf("send %d: got %v, want ErrSendQueueFull", i, err)
			}
			overflowAt = i
			break
		}
	}
	if overflowAt < capacity {
		t.Fatalf("overflow at send %d, want >= %d", overflowAt, capacity)
	}
	if err := fails.wait(t); !errors.Is(err, ErrSendQueueFull) {
		t.Fatalf("OnFail reason = %v, want ErrSendQueueFull", err)
	}
	if !errors.Is(w.Err(), ErrSendQueueFull) {
		t.Fatalf("Err() = %v", w.Err())
	}
	if err := w.Send(toastEnvelope(1)); !errors.Is(err, ErrSendQueueFull) {
		t.Fatalf("send after failure = %v, want ErrSendQueueFull", err)
	}
	// The connection was closed: the peer sees EOF once it drains.
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.Copy(io.Discard, peer); err != nil {
		t.Fatalf("peer did not see EOF after overflow: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if fails.count() != 1 {
		t.Fatalf("OnFail called %d times, want 1", fails.count())
	}
}

// net.Conn path: SetWriteDeadline fires.
func TestQueuedWriteDeadlineFires(t *testing.T) {
	host, peer := net.Pipe()
	defer peer.Close()
	fails := newFailRecorder()
	w := NewQueuedConnWriter(host, QueueOptions{WriteTimeout: 50 * time.Millisecond, Closer: host, OnFail: fails.record})
	start := time.Now()
	if err := w.Send(toastEnvelope(1)); err != nil {
		t.Fatal(err)
	}
	err := fails.wait(t)
	if !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("OnFail reason = %v, want ErrWriteTimeout", err)
	}
	if !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("timeout reason %q does not name the deadline", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("deadline fired after %s", elapsed)
	}
	if err := w.Send(toastEnvelope(1)); !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("send after timeout = %v", err)
	}
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection still open after write timeout")
	}
}

// Pipe path (stdio transport): no SetWriteDeadline, the watchdog closes the
// pipe to unblock the write.
func TestQueuedWriteWatchdogWithoutDeadlineSupport(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	fails := newFailRecorder()
	w := NewQueuedConnWriter(pw, QueueOptions{WriteTimeout: 50 * time.Millisecond, Closer: pw, OnFail: fails.record})
	if err := w.Send(toastEnvelope(1)); err != nil {
		t.Fatal(err)
	}
	if err := fails.wait(t); !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("OnFail reason = %v, want ErrWriteTimeout", err)
	}
	if _, err := pr.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("pipe read after watchdog = %v, want EOF", err)
	}
}

func TestQueuedWriteErrorIsSurfaced(t *testing.T) {
	host, peer := net.Pipe()
	_ = peer.Close()
	fails := newFailRecorder()
	w := NewQueuedConnWriter(host, QueueOptions{WriteTimeout: 10 * time.Second, Closer: host, OnFail: fails.record})
	if err := w.Send(toastEnvelope(1)); err != nil {
		t.Fatal(err)
	}
	err := fails.wait(t)
	if errors.Is(err, ErrWriteTimeout) || errors.Is(err, ErrSendQueueFull) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("OnFail reason = %v, want wrapped io.ErrClosedPipe", err)
	}
}

func TestQueuedCloseFlushesThenCloses(t *testing.T) {
	host, peer := net.Pipe()
	defer peer.Close()
	fails := newFailRecorder()
	w := NewQueuedConnWriter(host, QueueOptions{WriteTimeout: 10 * time.Second, Closer: host, OnFail: fails.record})
	for i := 0; i < 3; i++ {
		if err := w.Send(toastEnvelope(i)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	if err := w.Send(toastEnvelope(9)); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("send after Close = %v, want ErrWriterClosed", err)
	}
	var got []string
	if err := ReadLoop(peer, func(e Envelope) { got = append(got, e.Toast.Title) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != ",x,xx" {
		t.Fatalf("flushed titles = %q", got)
	}
	if !errors.Is(w.Err(), ErrWriterClosed) {
		t.Fatalf("Err() after Close = %v", w.Err())
	}
	time.Sleep(20 * time.Millisecond)
	if fails.count() != 0 {
		t.Fatal("OnFail called for an orderly Close")
	}
}

func TestQueuedAbortDoesNotNotify(t *testing.T) {
	host, peer := net.Pipe()
	defer peer.Close()
	fails := newFailRecorder()
	w := NewQueuedConnWriter(host, QueueOptions{WriteTimeout: 10 * time.Second, Closer: host, OnFail: fails.record})
	_ = w.Send(toastEnvelope(1))
	w.Abort()
	if err := w.Send(toastEnvelope(1)); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("send after Abort = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if fails.count() != 0 {
		t.Fatalf("OnFail called after Abort: %v", fails.errs)
	}
}

func TestSyncConnWriterUnchanged(t *testing.T) {
	var b strings.Builder
	w := NewConnWriter(&b)
	if w.Queued() {
		t.Fatal("NewConnWriter must be synchronous")
	}
	if err := w.Send(toastEnvelope(2)); err != nil {
		t.Fatal(err)
	}
	if b.String() != `{"type":"toast","toast":{"plugin_id":"p","level":"","title":"xx","message":""}}`+"\n" {
		t.Fatalf("sync write = %q", b.String())
	}
	_ = w.Close()
	w.Abort()
	if w.Err() != nil {
		t.Fatal("sync writer reports an error")
	}
}
