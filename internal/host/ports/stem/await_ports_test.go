package stem_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	stemports "github.com/hyperlab-sh/husk/internal/host/ports/stem"
)

// inline runs the posted closure at once, so the result is already in the
// wait's channel when the (already ended) request context is looked at.
func inline(fn func()) bool { fn(); return true }

func endedByShutdown() context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(contracts.ErrShutdown)
	return ctx
}

// A reply the loop already produced must win over an ended context in every
// stem port wait: the husk.quit invoke reply itself depends on it.
func TestReadyResultBeatsEndedContext(t *testing.T) {
	r := newRig(t)
	calls := stemports.NewCalls(r.disp, inline)
	hr := newHookRig(t, hookSubs{"a.h": nil}, live{})
	hooksPort := stemports.NewHooks(hr.runner, inline)
	for i := 0; i < 100; i++ {
		if out, err := calls.ActionInvoke(endedByShutdown(), cli, "husk.quit", nil); err != nil || string(out) == "" {
			t.Fatalf("ActionInvoke iteration %d: %s, %v", i, out, err)
		}
		if _, err := calls.ActionList(endedByShutdown(), cli); err != nil {
			t.Fatalf("ActionList iteration %d: %v", i, err)
		}
		if _, err := hooksPort.HookList(endedByShutdown(), cli); err != nil {
			t.Fatalf("HookList iteration %d: %v", i, err)
		}
		if _, err := hooksPort.HookCall(endedByShutdown(), ownerOf("a.h"), "a.h", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("HookCall iteration %d: %v", i, err)
		}
	}
}

// A request ended by the host shutting down says so; one whose caller left
// stays a plain cancellation.
func TestEndedContextAnswersByCause(t *testing.T) {
	r := newRig(t)
	calls := stemports.NewCalls(r.disp, func(func()) bool { return true }) // the loop never runs it
	_, err := calls.ActionList(endedByShutdown(), cli)
	d, ok := err.(contracts.DataError)
	if contracts.CodeOf(err) != "cancelled" || !ok || d.Data()["reason"] != "shutdown" {
		t.Fatalf("shutdown: %#v", err)
	}
	gone, quit := context.WithCancel(context.Background())
	quit()
	_, err = calls.ActionList(gone, cli)
	if d, ok := err.(contracts.DataError); contracts.CodeOf(err) != "cancelled" || (ok && d.Data() != nil) {
		t.Fatalf("caller gone: %#v", err)
	}
}
