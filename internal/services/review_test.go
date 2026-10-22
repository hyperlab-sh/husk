package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
)

type applyProbe func() Report

func (f applyProbe) Apply() Report { return f() }

type lateProbeService struct {
	gen      atomic.Uint64
	entered  chan struct{}
	gate     chan struct{}
	prepared chan Candidate
}

func (s *lateProbeService) Generation() Generation { return Generation(s.gen.Load()) }
func (s *lateProbeService) Rebuild(context.Context, ReloadInput) (Candidate, Report, error) {
	close(s.entered)
	<-s.gate
	candidate := applyProbe(func() Report { return Report{Service: "theme", Gen: Generation(s.gen.Add(1)), Applied: true} })
	s.prepared <- candidate
	return candidate, Report{Service: "theme"}, nil
}

var _ c.Reloadable = (*lateProbeService)(nil)

func TestReviewFlightLifetimeIsIndependentOfRequesters(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "running-owner", true: "queued-owner"}[queued], func(t *testing.T) {
			set := fakeServices()
			clk := clock()
			gate := make(chan struct{})
			entered := make(chan struct{})
			if queued {
				profile := set["profile"].(*fakeService)
				profile.run = func(context.Context) {
					if profile.calls.Load() == 1 {
						close(entered)
						<-gate
					}
				}
			} else {
				clk.gate = gate
			}
			co, err := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clk})
			if err != nil {
				t.Fatal(err)
			}
			defer co.Close()
			type result struct {
				reports []Report
				err     error
			}
			first := make(chan result, 1)
			owner, cancel := context.WithCancel(context.Background())
			defer cancel()
			if queued {
				go func() { r, e := co.Reload(context.Background(), ReasonCLI); first <- result{r, e} }()
				<-entered
			} else {
				go func() { r, e := co.Reload(owner, ReasonCLI); first <- result{r, e} }()
				<-clk.created
			}
			cancelled := first
			if queued {
				cancelled = make(chan result, 1)
				go func() { r, e := co.Reload(owner, ReasonCLI); cancelled <- result{r, e} }()
				<-co.joined
			}
			joined := make(chan result, 1)
			go func() { r, e := co.Reload(context.Background(), ReasonPlugin); joined <- result{r, e} }()
			<-co.joined
			cancel()
			close(gate)
			departed := <-cancelled
			if !errors.Is(departed.err, context.Canceled) {
				t.Errorf("requester did not leave with its context: %v", departed.err)
			}
			remaining := <-joined
			if remaining.err != nil || len(remaining.reports) != 5 {
				t.Fatalf("other waiter lost its flight: %+v %v", remaining.reports, remaining.err)
			}
			for _, r := range remaining.reports {
				if !r.Applied {
					t.Errorf("cancelled owner poisoned shared flight: %+v", r)
				}
			}
			if queued {
				initial := <-first
				if initial.err != nil {
					t.Fatal(initial.err)
				}
			}
		})
	}
}
