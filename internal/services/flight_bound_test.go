package services

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// pollBound bounds even waits whose mutation leaves a worker stuck. No timer
// or sleep drives these tests: the fake clock drives the coordinator.
func pollBound(t *testing.T, what string, ready func() bool) {
	t.Helper()
	for range 1000000 {
		if ready() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("never observed %s", what)
}
func receiveBound[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	var out T
	pollBound(t, what, func() bool {
		select {
		case out = <-ch:
			return true
		default:
			return false
		}
	})
	return out
}
func releaseGate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return gate, release
}
func assertFlightError(t *testing.T, err error, code, reason string) {
	t.Helper()
	var coded *c.Error
	if !errors.As(err, &coded) || coded.Code != code || coded.Reason != reason {
		t.Fatalf("error = %v, want %s/%s", err, code, reason)
	}
}

type boundResult struct {
	reports []Report
	err     error
}

func startReload(co *Coordinator) <-chan boundResult {
	ch := make(chan boundResult, 1)
	go func() { r, err := co.Reload(context.Background(), ReasonCLI); ch <- boundResult{r, err} }()
	return ch
}
func assertApplied(t *testing.T, res boundResult) {
	t.Helper()
	if res.err != nil || len(res.reports) != 5 {
		t.Fatalf("reload = %+v", res)
	}
	for _, r := range res.reports {
		if !r.Applied {
			t.Fatalf("not applied: %+v", r)
		}
	}
}
func testAbandonedGatherReleasesTurn(t *testing.T, dry bool) {
	gate, release := releaseGate(t)
	defer release()
	entered := make(chan struct{}, 1)
	var callsMu sync.Mutex
	calls := map[string]int{}
	st := &fakeStore{blockOnce: gate, onCall: func(call string) {
		callsMu.Lock()
		calls[call]++
		callsMu.Unlock()
		if call == "profiles" {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
	}}
	clk := clock()
	co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clk})
	defer co.Close()
	first := make(chan error, 1)
	go func() {
		if dry {
			_, err := co.Inputs(context.Background(), Caller{Kind: "cli"})
			first <- err
		} else {
			_, err := co.Reload(context.Background(), ReasonCLI)
			first <- err
		}
	}()
	receiveBound(t, "first gather", entered)
	// Resolve has taken the first-only gate before the deadline is advanced.
	pollBound(t, "blocked Resolve", func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.blockOnce == nil })
	co.mu.Lock()
	lateInput := co.active.input
	co.mu.Unlock()
	clk.advance(2 * time.Second)
	reason := "reload_deadline"
	if dry {
		reason = "input_deadline"
	}
	assertFlightError(t, receiveBound(t, "timeout reply", first), "timeout", reason)
	second := startReload(co)
	assertApplied(t, receiveBound(t, "next flight before gate release", second))
	if got := strings.Join(st.trace(), ","); got != "invalidate,invalidate" {
		t.Fatalf("trace = %s", got)
	}
	release()
	if late := receiveBound(t, "abandoned gather returned", lateInput); !errors.Is(late.err, context.Canceled) {
		t.Fatalf("abandoned gather error = %v, want context.Canceled", late.err)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	for _, call := range []string{"invalidate", "profiles", "resolve"} {
		if calls[call] != 2 {
			t.Fatalf("%s called %d times, want once per gather", call, calls[call])
		}
	}
	for _, call := range []string{"layers:keymap:", "layers:config:", "declared:plugins", "declared:keymap"} {
		if calls[call] != 1 {
			t.Fatalf("%s called %d times, want only the successor's call; trace=%v", call, calls[call], calls)
		}
	}
}

// firstHeldService deliberately returns an unguarded candidate: an accidental
// late Apply cannot hide behind Prepared's own deadline defence.
type firstHeldService struct {
	calls       atomic.Int64
	gen         atomic.Uint64
	lateApplies atomic.Int64
	entered     chan struct{}
	gate        <-chan struct{}
	returned    chan error
}

func (s *firstHeldService) Generation() Generation { return Generation(s.gen.Load()) }
func (s *firstHeldService) Rebuild(ctx context.Context, _ ReloadInput) (Candidate, Report, error) {
	first := s.calls.Add(1) == 1
	if first {
		close(s.entered)
		<-s.gate
		s.returned <- ctx.Err()
	}
	cand := applyProbe(func() Report {
		if first {
			s.lateApplies.Add(1)
		}
		return Report{Service: "theme", Gen: Generation(s.gen.Add(1)), Applied: true}
	})
	return cand, Report{Service: "theme"}, nil
}
func TestTimedOutRebuildReleasesTurnAndCancelsLateWork(t *testing.T) {
	gate, release := releaseGate(t)
	hold := &firstHeldService{entered: make(chan struct{}), gate: gate, returned: make(chan error, 1)}
	set := fakeServices()
	set["theme"] = hold
	clk := clock()
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clk})
	defer co.Close()
	first := startReload(co)
	receiveBound(t, "theme Rebuild", hold.entered)
	clk.advance(2 * time.Second)
	assertFlightError(t, receiveBound(t, "first timeout", first).err, "timeout", "reload_deadline")
	second := receiveBound(t, "second theme before release", startReload(co))
	assertApplied(t, second)
	gen := hold.Generation()
	release()
	if err := receiveBound(t, "late rebuild cancellation", hold.returned); err != context.Canceled {
		t.Fatalf("late context = %v", err)
	}
	// Only the first coordinator flight could apply this late result, and
	// that flight has ended. The successor's generation must keep serving.
	if hold.lateApplies.Load() != 0 || hold.Generation() != gen || gen != second.reports[2].Gen {
		t.Fatalf("late Apply count=%d gen=%d want %d", hold.lateApplies.Load(), hold.Generation(), gen)
	}
}

// Every Store boundary is checked, including the two Declared calls. Store's
// interface has no context; it observes cancellation through the context the
// test passed to loadInput, exactly as an uninterruptible call would on return.
func TestGatherStopsBetweenStoreCalls(t *testing.T) {
	calls := []string{"invalidate", "profiles", "layers:config:", "resolve", "resolve:themes", "layers:themes:", "layers:keymap:", "layers:config:", "declared:plugins", "declared:keymap"}
	for stop := range calls {
		t.Run(strings.ReplaceAll(calls[stop], ":", "-")+string(rune('0'+stop)), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate, release := releaseGate(t)
			entered := make(chan struct{})
			var mu sync.Mutex
			var trace []string
			observed := make(chan error, 1)
			st := &fakeStore{onCall: func(call string) {
				mu.Lock()
				trace = append(trace, call)
				n := len(trace)
				mu.Unlock()
				if n == stop+1 {
					close(entered)
					<-gate
					observed <- ctx.Err()
				}
			}}
			co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clock(), Profile: func(bool, []store.LayerDoc, []string) (string, []Problem, bool) { return "", nil, false }})
			defer co.Close()
			done := make(chan inputResult, 1)
			go func() { done <- co.loadInput(ctx, nil) }()
			receiveBound(t, "Store boundary", entered)
			cancel()
			release()
			if err := receiveBound(t, "Store cancellation", observed); err != context.Canceled {
				t.Fatalf("Store context = %v", err)
			}
			if res := receiveBound(t, "stopped gather", done); !errors.Is(res.err, context.Canceled) {
				t.Fatalf("gather = %+v", res)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(trace) != stop+1 {
				t.Fatalf("calls after cancellation: %v", trace)
			}
		})
	}
}

func TestLateGatherCannotOverwriteSelection(t *testing.T) {
	gate, release := releaseGate(t)
	entered := make(chan struct{})
	var choice atomic.Value
	choice.Store("a")
	var calls atomic.Int64
	profile := func(bool, []store.LayerDoc, []string) (string, []Problem, bool) {
		name := choice.Load().(string)
		if calls.Add(1) == 1 {
			close(entered)
			<-gate
		}
		return name, nil, false
	}
	st := &fakeStore{}
	clk := clock()
	co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clk, Profile: profile})
	defer co.Close()
	first := startReload(co)
	receiveBound(t, "profile selected a", entered)
	co.mu.Lock()
	late := co.active.input
	co.mu.Unlock()
	clk.advance(2 * time.Second)
	assertFlightError(t, receiveBound(t, "a flight timeout", first).err, "timeout", "reload_deadline")
	choice.Store("b")
	assertApplied(t, receiveBound(t, "b flight", startReload(co)))
	release()
	if res := receiveBound(t, "late gather return", late); !errors.Is(res.err, context.Canceled) {
		t.Fatalf("late gather = %+v", res)
	}
	if !co.Settled("b") {
		t.Fatal("late gather overwrote b")
	}
	co.profileChanged([]byte(`{"active":"b"}`))
	co.mu.Lock()
	pending := co.active != nil || len(co.queue) != 0
	co.mu.Unlock()
	if pending {
		t.Fatal("writing b scheduled a redundant flight")
	}
}

func TestCloseReleasesWaitersBehindStuckGather(t *testing.T) {
	gate, _ := releaseGate(t)
	st := &fakeStore{blockOnce: gate}
	co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clock()})
	defer co.Close()
	first := startReload(co)
	pollBound(t, "first gather blocked", func() bool { st.mu.Lock(); defer st.mu.Unlock(); return st.blockOnce == nil })
	queued := startReload(co)
	dry := make(chan error, 1)
	go func() { _, err := co.Inputs(context.Background(), Caller{Kind: "cli"}); dry <- err }()
	pollBound(t, "queued reload and dry run", func() bool { co.mu.Lock(); defer co.mu.Unlock(); return len(co.queue) == 2 })
	co.Close()
	for _, ch := range []<-chan boundResult{first, queued} {
		res := receiveBound(t, "shutdown flight reply", ch)
		assertFlightError(t, res.err, "cancelled", "shutdown")
		if len(res.reports) != 5 {
			t.Fatalf("reports = %+v", res.reports)
		}
		for _, r := range res.reports {
			if r.Applied || len(r.Problems) != 1 || r.Problems[0].Code != "cancelled" {
				t.Fatalf("report = %+v", r)
			}
		}
	}
	assertFlightError(t, receiveBound(t, "shutdown dry reply", dry), "cancelled", "shutdown")
	pollBound(t, "shutdown idle", func() bool { co.mu.Lock(); defer co.mu.Unlock(); return co.active == nil && len(co.queue) == 0 })
}
