package stem

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// A write that already ran must be reported as applied even when the request
// context ended first (select alone picks at random between two ready cases).
func TestStateWriteResultBeatsEndedContext(t *testing.T) {
	w, _, _ := newPort(t, StatePortOptions{}, specAtlas) // syncLoop: the write runs inside Post
	ctx, cancel := context.WithCancelCause(ctxBG)
	cancel(contracts.ErrShutdown)
	for i := 0; i < 100; i++ {
		if _, err := w.Set(ctx, callerAtlas, "atlas.state", json.RawMessage(`{"done":true}`)); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
}

// A write whose closure never ran answers by cause: shutdown says so, a
// caller that left is a plain cancellation.
func TestStateWriteNotRunAnswersByCause(t *testing.T) {
	w, _, _ := newPort(t, StatePortOptions{Loop: &blockedLoop{}}, specAtlas)
	ctx, cancel := context.WithCancelCause(ctxBG)
	cancel(contracts.ErrShutdown)
	_, err := w.Set(ctx, callerAtlas, "atlas.state", json.RawMessage(`{}`))
	wantPortCode(t, err, protocol.CodeCancelled)
	if d, ok := err.(contracts.DataError); !ok || d.Data()["reason"] != "shutdown" {
		t.Fatalf("err = %#v, want reason shutdown", err)
	}
	gone, quit := context.WithCancel(ctxBG)
	quit()
	_, err = w.Set(gone, callerAtlas, "atlas.state", json.RawMessage(`{}`))
	wantPortCode(t, err, protocol.CodeCancelled)
	if d, ok := err.(contracts.DataError); ok && d.Data() != nil {
		t.Fatalf("caller gone carries data %v", d.Data())
	}
}

func TestAwaitPrefersAReadyResult(t *testing.T) {
	ctx, cancel := context.WithCancel(ctxBG)
	cancel()
	for i := 0; i < 100; i++ {
		done := make(chan int, 1)
		done <- 7
		if v, ok := await(ctx, done); !ok || v != 7 {
			t.Fatalf("iteration %d: v=%d ok=%v", i, v, ok)
		}
	}
	if _, ok := await(ctx, make(chan int)); ok {
		t.Fatal("await returned a result nobody sent")
	}
}
