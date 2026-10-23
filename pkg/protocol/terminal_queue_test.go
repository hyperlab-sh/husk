package protocol

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTerminalAttachCaptureCompatibility(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		mode CaptureMode
	}{{`true`, CaptureText}, {`false`, CaptureNone}, {`"text"`, CaptureText}, {`"all"`, CaptureAll}} {
		var p TerminalAttachParams
		if err := json.Unmarshal([]byte(`{"surface":"main-stage","capture":`+tc.raw+`}`), &p); err != nil {
			t.Fatal(err)
		}
		if p.Capture == nil || p.Capture.Mode() != tc.mode || p.Validate() != nil {
			t.Fatalf("capture %s was not normalized", tc.raw)
		}
	}
	var p TerminalAttachParams
	if json.Unmarshal([]byte(`{"surface":"main-stage","capture":"invalid"}`), &p) == nil {
		t.Fatal("invalid capture accepted")
	}
	if CaptureBool(true).Mode() != CaptureText || CaptureBool(false).Mode() != CaptureNone {
		t.Fatal("boolean migration failed")
	}
}

func TestTerminalTrySendDropsWithoutFailingQueue(t *testing.T) {
	sink, clock := newGatedWriter(), newFakeClock()
	failed := make(chan error, 1)
	w := NewQueuedWriter(sink, QueueOptions{Capacity: 1, Clock: clock, OnFail: func(err error) { failed <- err }})
	defer func() { w.Abort(); <-w.Done() }()
	if err := w.TrySend(testNotification()); err != nil {
		t.Fatal(err)
	}
	<-sink.started
	if err := w.TrySend(testNotification()); err != nil {
		t.Fatal(err)
	}
	if err := w.TrySend(testNotification()); !errors.Is(err, ErrSendQueueFull) {
		t.Fatalf("full offer %v", err)
	}
	if w.Err() != nil {
		t.Fatal("droppable input failed the queue", w.Err())
	}
	sink.release()
	_ = w.Close()
	<-w.Done()
	select {
	case err := <-failed:
		t.Fatalf("full queue called OnFail: %v", err)
	default:
	}
}
