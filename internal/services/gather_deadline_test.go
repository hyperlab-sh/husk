package services

import (
	"context"
	"errors"
	"testing"
)

// inputSelectGate pauses the caller while it evaluates its second select,
// after the gather has started but before any ready case can be chosen.
type inputSelectGate struct {
	context.Context
	calls   int
	entered chan struct{}
	gate    <-chan struct{}
}

func (ctx *inputSelectGate) Done() <-chan struct{} {
	ctx.calls++
	if ctx.calls == 2 {
		close(ctx.entered)
		<-ctx.gate
	}
	return ctx.Context.Done()
}

// errOnlyContext keeps an already-cancelled context's Err but disables its
// select case, allowing the regression test to force the result arm.
type errOnlyContext struct{ context.Context }

func (errOnlyContext) Done() <-chan struct{} { return nil }

func TestDryRunReadyGatherResultPreservesShutdown(t *testing.T) {
	for _, forceResult := range []bool{false, true} {
		name := "both cases ready"
		if forceResult {
			name = "result arm"
		}
		t.Run(name, func(t *testing.T) {
			gatherGate, releaseGather := releaseGate(t)
			defer releaseGather()
			selectGate, releaseSelect := releaseGate(t)
			defer releaseSelect()
			profiles := make(chan struct{})
			st := &fakeStore{onCall: func(call string) {
				if call == "profiles" {
					close(profiles)
					<-gatherGate
				}
			}}
			co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clock()})
			defer co.Close()
			ctx := &inputSelectGate{Context: context.Background(), entered: make(chan struct{}), gate: selectGate}
			done := make(chan error, 1)
			go func() { _, err := co.Inputs(ctx, Caller{Kind: "cli"}); done <- err }()
			receiveBound(t, "gather in Profiles", profiles)
			receiveBound(t, "caller before input select", ctx.entered)
			co.mu.Lock()
			input := co.active.input
			co.mu.Unlock()
			co.Close()
			releaseGather()
			pollBound(t, "cancelled gather result ready", func() bool { return len(input) == 1 })
			if forceResult {
				// The caller is paused and the gather no longer reads co.ctx.
				// Keep Err cancelled but suppress the competing Done case so
				// this assertion does not depend on select's random choice.
				co.ctx = errOnlyContext{co.ctx}
			}
			releaseSelect()
			assertFlightError(t, receiveBound(t, "shutdown reply from ready result", done), "cancelled", "shutdown")
		})
	}
}

func TestDryRunCancellationReleasesStuckGather(t *testing.T) {
	for _, closeCoordinator := range []bool{false, true} {
		name := "caller"
		if closeCoordinator {
			name = "coordinator"
		}
		t.Run(name, func(t *testing.T) {
			gate, _ := releaseGate(t)
			st := &fakeStore{blockOnce: gate}
			co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clock()})
			defer co.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := co.Inputs(ctx, Caller{Kind: "cli"}); done <- err }()
			pollBound(t, "blocked dry gather", func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.blockOnce == nil })
			if closeCoordinator {
				co.Close()
			} else {
				cancel()
			}
			err := receiveBound(t, "cancelled dry reply", done)
			if closeCoordinator {
				assertFlightError(t, err, "cancelled", "shutdown")
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			pollBound(t, "released dry turn", func() bool { co.mu.Lock(); defer co.mu.Unlock(); return co.active == nil })
			if !closeCoordinator {
				assertApplied(t, receiveBound(t, "flight after caller cancellation", startReload(co)))
			}
		})
	}
}

func TestCloseReleasesWaitersBehindStuckRebuild(t *testing.T) {
	gate, _ := releaseGate(t)
	hold := &firstHeldService{entered: make(chan struct{}), gate: gate, returned: make(chan error, 1)}
	set := fakeServices()
	set["theme"] = hold
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clock()})
	defer co.Close()
	first := startReload(co)
	receiveBound(t, "stuck Rebuild", hold.entered)
	second := startReload(co)
	pollBound(t, "queued reload", func() bool { co.mu.Lock(); defer co.mu.Unlock(); return len(co.queue) == 1 })
	co.Close()
	for _, ch := range []<-chan boundResult{first, second} {
		res := receiveBound(t, "shutdown reply before rebuild returns", ch)
		assertFlightError(t, res.err, "cancelled", "shutdown")
		if len(res.reports) != 5 {
			t.Fatalf("reports = %+v", res.reports)
		}
	}
}
