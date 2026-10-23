package services

import (
	"context"
	"errors"
	"testing"
)

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
