package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriterLineLimit(t *testing.T) {
	const base = `{"method":"session.ping","params":{"x":""}}`
	for _, extra := range []int{0, 1} {
		var sink bytes.Buffer
		m := Message{Method: MethodSessionPing, Params: json.RawMessage(`{"x":"` + strings.Repeat("a", MaxLineBytes-len(base)+extra) + `"}`)}
		err := NewWriter(&sink).Send(m)
		if extra == 0 {
			if err != nil || sink.Len() != MaxLineBytes+1 {
				t.Fatalf("limit write: len=%d err=%v", sink.Len(), err)
			}
		} else if !errors.Is(err, ErrLineTooLong) || sink.Len() != 0 {
			t.Fatalf("oversize write: len=%d err=%v", sink.Len(), err)
		}
	}
}

func TestConcurrentSynchronousWritesAreWholeLines(t *testing.T) {
	var sink bytes.Buffer
	w := NewWriter(&sink)
	var group sync.WaitGroup
	failures := make(chan error, 50)
	for i := int64(0); i < 50; i++ {
		group.Add(1)
		go func(n int64) {
			defer group.Done()
			id, _ := NewIntID(n)
			if err := w.Send(Message{ID: &id, Result: json.RawMessage(`{}`)}); err != nil {
				failures <- err
			}
		}(i)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	messages, rejected := readMessages(t, sink.Bytes())
	if len(messages) != 50 || len(rejected) != 0 {
		t.Fatalf("messages=%d rejected=%v", len(messages), rejected)
	}
	seen := make(map[string]bool)
	for _, m := range messages {
		seen[m.ID.String()] = true
	}
	if len(seen) != 50 {
		t.Fatal("ids lost or duplicated")
	}
}

func TestQueueDefaults(t *testing.T) {
	w := NewQueuedWriter(io.Discard, QueueOptions{Clock: newFakeClock()})
	if !w.Queued() || cap(w.q.ch) != 1024 || w.q.timeout != 5*time.Second {
		t.Fatal("unexpected queue defaults")
	}
	w.Abort()
	<-w.Done()
	if !errors.Is(w.Err(), ErrWriterClosed) {
		t.Fatal(w.Err())
	}
}

type deadlineFailureWriter struct{ errorWriter }

func (w deadlineFailureWriter) SetWriteDeadline(time.Time) error { return w.err }

func TestQueueDeadlineSetupFailure(t *testing.T) {
	want := errors.New("cannot set deadline")
	failed := make(chan error, 1)
	w := NewQueuedWriter(deadlineFailureWriter{errorWriter{err: want}}, QueueOptions{Clock: newFakeClock(), OnFail: func(err error) { failed <- err }})
	if err := w.Send(testNotification()); err != nil {
		t.Fatal(err)
	}
	if err := <-failed; !errors.Is(err, want) {
		t.Fatal(err)
	}
	<-w.Done()
}
