package services

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// rebuild prepares and applies one candidate, returning the final report.
func rebuild(t *testing.T, s Reloadable, in ReloadInput) Report {
	t.Helper()
	cand, r, err := s.Rebuild(context.Background(), in)
	if err != nil {
		t.Fatalf("rebuild %s: %v %+v", r.Service, err, r.Problems)
	}
	return cand.Apply()
}

// Report.Changed is true only when the candidate was applied and its value
// differs from the snapshot it replaced.
func TestReportChangedPerService(t *testing.T) {
	profiles := &fakeStore{}
	themeSettings := func(raw string) ReloadInput {
		s := newSet(settings.Options{})
		in := input(raw)
		in.Doc.Origins = map[string]store.Layer{"theme.palette.accent": store.LayerUser}
		apply(t, s, in)
		in.Settings = s.Snapshot()
		return in
	}
	keymap := func(key string) ReloadInput {
		in := input(`{}`)
		in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"` + key + `","action":"husk.home"}]}`)}}
		return in
	}
	for name, tc := range map[string]struct {
		svc    func() Reloadable
		a, b   func() ReloadInput
		before func(string) // prepares the fixture for input "a" or "b"
	}{
		"settings": {
			svc: func() Reloadable { return newSet(settings.Options{}) },
			a:   func() ReloadInput { return input(`{"theme":{"name":"One"}}`) },
			b:   func() ReloadInput { return input(`{"theme":{"name":"Two"}}`) },
		},
		"theme": {
			svc: func() Reloadable { return stheme.New() },
			a:   func() ReloadInput { return themeSettings(`{"theme":{"palette":{"accent":"#8fa8ff"}}}`) },
			b:   func() ReloadInput { return themeSettings(`{"theme":{"palette":{"accent":"#a0b4ff"}}}`) },
		},
		"keybinding": {
			svc: func() Reloadable { return keybinding.New() },
			a:   func() ReloadInput { return keymap("g") },
			b:   func() ReloadInput { return keymap("h") },
		},
		"secrets": {
			svc: func() Reloadable {
				s, err := secrets.New(secrets.Options{State: state.New(state.Options{}), Post: func(func()) bool { return true }, Crypto: &memoryCrypto{data: map[string][]byte{}}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.Close)
				return s
			},
			a: func() ReloadInput { return catalog("one") },
			b: func() ReloadInput { return catalog("two") },
		},
		"profile": {
			svc: func() Reloadable {
				p, err := profile.New(profile.Options{Store: profiles, State: state.New(state.Options{}), Post: func(func()) bool { return true }})
				if err != nil {
					t.Fatal(err)
				}
				return p
			},
			a: func() ReloadInput { return input(`{}`) },
			b: func() ReloadInput { return input(`{}`) },
			before: func(which string) {
				profiles.profiles = map[string][]string{"a": {"work"}, "b": {"work", "play"}}[which]
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := tc.svc()
			prep := func(which string) {
				if tc.before != nil {
					tc.before(which)
				}
			}
			prep("a")
			if r := rebuild(t, svc, tc.a()); !r.Applied {
				t.Fatalf("first load not applied: %+v", r)
			}
			if r := rebuild(t, svc, tc.a()); !r.Applied || r.Changed {
				t.Errorf("identical inputs: applied=%v changed=%v, want true,false", r.Applied, r.Changed)
			}
			prep("b")
			if r := rebuild(t, svc, tc.b()); !r.Applied || !r.Changed {
				t.Errorf("changed inputs: applied=%v changed=%v, want true,true", r.Applied, r.Changed)
			}
		})
	}
}

// A candidate that cannot publish is never Changed, whatever its value.
func TestChangedIsFalseWheneverNotApplied(t *testing.T) {
	clk := clock()
	for name, tc := range map[string]struct {
		ctx  func() (context.Context, context.CancelFunc)
		move func(s *settings.Service)
	}{
		"deadline passed": {ctx: func() (context.Context, context.CancelFunc) {
			ctx := c.WithDeadline(context.Background(), clk, clk.Now().Add(time.Second))
			return ctx, func() {}
		}, move: func(*settings.Service) { clk.advance(time.Second) }},
		"closed": {ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSet(settings.Options{})
			ctx, finish := tc.ctx()
			cand, _, err := s.Rebuild(ctx, input(`{"theme":{"name":"Late"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if tc.move != nil {
				tc.move(s)
			}
			finish()
			r := cand.Apply()
			if r.Applied || r.Changed || s.Generation() != 0 {
				t.Fatalf("late apply: %+v gen=%d", r, s.Generation())
			}
			if len(r.Problems) != 1 || r.Problems[0].Code != "candidate_superseded" || r.Problems[0].Severity != "error" {
				t.Fatalf("problems = %+v, want one candidate_superseded", r.Problems)
			}
		})
	}
}

// A candidate whose Service moved on since its rebuild is superseded too.
func TestApplyAfterTheGenerationMovedSwapsNothing(t *testing.T) {
	s := newSet(settings.Options{})
	first, _, _ := s.Rebuild(context.Background(), input(`{"theme":{"name":"One"}}`))
	second, _, _ := s.Rebuild(context.Background(), input(`{"theme":{"name":"Two"}}`))
	if r := first.Apply(); !r.Applied || !r.Changed {
		t.Fatalf("first: %+v", r)
	}
	r := second.Apply()
	if r.Applied || r.Changed || r.Gen != 1 || len(r.Problems) != 1 || r.Problems[0].Code != "candidate_superseded" {
		t.Fatalf("second: %+v", r)
	}
}

// holdService blocks inside Rebuild until released, then delegates to a real
// Service and hands the candidate to the test.
type holdService struct {
	Reloadable
	entered chan struct{}
	gate    chan struct{}
	cands   chan Candidate
}

func (h *holdService) Rebuild(ctx context.Context, in ReloadInput) (Candidate, Report, error) {
	// Prepare inside the deadline, then be slow to hand the candidate back.
	cand, r, err := h.Reloadable.Rebuild(ctx, in)
	close(h.entered)
	<-h.gate
	h.cands <- cand
	return cand, r, err
}

// A Rebuild held past the flight's deadline, released and then applied by
// someone else, can never change the Service's generation.
func TestLateRebuildCanNeverMoveAGeneration(t *testing.T) {
	set := fakeServices()
	real := newSet(settings.Options{})
	hold := &holdService{Reloadable: real, entered: make(chan struct{}), gate: make(chan struct{}), cands: make(chan Candidate, 1)}
	set["settings"] = hold
	clk := clock()
	co, _ := NewCoordinator(Options{Store: &fakeStore{resolved: store.Resolved{Doc: doc(`{"theme":{"name":"Late"}}`)}}, Services: set, Clock: clk})
	defer co.Close()
	done := make(chan []Report, 1)
	go func() { r, _ := co.Reload(context.Background(), ReasonCLI); done <- r }()
	<-hold.entered
	clk.advance(2 * time.Second)
	reports := <-done
	if reports[1].Applied || reports[1].Problems[0].Code != "timed_out" {
		t.Fatalf("settings report: %+v", reports[1])
	}
	close(hold.gate)
	late := <-hold.cands
	if r := late.Apply(); r.Applied || r.Changed {
		t.Fatalf("late Apply: %+v", r)
	}
	if real.Generation() != 0 {
		t.Fatalf("late candidate moved the generation to %d", real.Generation())
	}
}

// The flight itself failing is the only error of config.reload; each failure
// has its own code and reason, and the reports travel with it.
func TestFlightErrorsAreTypedAndReportsStillCarryProblems(t *testing.T) {
	type want struct{ code, reason, problem string }
	check := func(t *testing.T, err error, w want) {
		t.Helper()
		var coded *c.Error
		if !errors.As(err, &coded) || coded.Code != w.code || coded.Reason != w.reason {
			t.Fatalf("error = %#v, want %s/%s", err, w.code, w.reason)
		}
	}
	t.Run("store_error", func(t *testing.T) {
		set := fakeServices()
		st := &fakeStore{resolved: store.Resolved{Problems: []store.Problem{{Code: "invalid_document", File: "profiles/work/config.json", Message: "secret-ish detail"}}}}
		co, _ := NewCoordinator(Options{Store: st, Services: set, Clock: clock()})
		defer co.Close()
		reports, err := co.Reload(context.Background(), ReasonCLI)
		check(t, err, want{"unavailable", "store_error", ""})
		for _, r := range reports {
			if r.Applied || len(r.Problems) != 1 || r.Problems[0].Code != "store_error" || !strings.Contains(r.Problems[0].Message, "profiles/work/config.json") || strings.Contains(r.Problems[0].Message, "secret-ish") {
				t.Fatalf("%s: %+v", r.Service, r.Problems)
			}
		}
	})
	t.Run("reload_deadline", func(t *testing.T) {
		set := fakeServices()
		gate := make(chan struct{})
		entered := make(chan struct{})
		set["theme"].(*fakeService).run = func(context.Context) { close(entered); <-gate }
		clk := clock()
		co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clk})
		defer co.Close()
		done := make(chan struct{})
		var reports []Report
		var err error
		go func() { reports, err = co.Reload(context.Background(), ReasonCLI); close(done) }()
		<-entered
		clk.advance(2 * time.Second)
		<-done
		close(gate)
		check(t, err, want{"timeout", "reload_deadline", ""})
		if len(reports) != 5 || reports[2].Problems[0].Code != "timed_out" || reports[3].Problems[0].Code != "timed_out" || !reports[0].Applied || !reports[1].Applied {
			t.Fatalf("reports: %+v", reports)
		}
	})
	t.Run("a Service that kept its snapshot is a success", func(t *testing.T) {
		set := fakeServices()
		set["keybinding"] = &rebuildErrService{fakeService{name: "keybinding"}, errors.New("boom")}
		co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clock()})
		defer co.Close()
		reports, err := co.Reload(context.Background(), ReasonCLI)
		if err != nil || reports[3].Applied {
			t.Fatalf("err=%v keybinding=%+v", err, reports[3])
		}
	})
}

// A Close during a flight reports cancelled, never timed_out; what was already
// published stays, waiters (queued ones included) get cancelled/shutdown, and
// the report slice still has five rows.
func TestCloseDuringAFlightReportsCancelled(t *testing.T) {
	set := fakeServices()
	entered := make(chan struct{})
	set["keybinding"].(*fakeService).run = func(ctx context.Context) { close(entered); <-ctx.Done() }
	clk := clock()
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clk})
	type result struct {
		reports []Report
		err     error
	}
	first := make(chan result, 1)
	go func() { r, err := co.Reload(context.Background(), ReasonCLI); first <- result{r, err} }()
	<-entered
	queuedCtx, parked := parkedCtx(context.Background())
	queued := make(chan result, 1)
	go func() { r, err := co.Reload(queuedCtx, ReasonPlugin); queued <- result{r, err} }()
	<-parked
	co.Close()
	for name, ch := range map[string]chan result{"running": first, "queued": queued} {
		res := <-ch
		var coded *c.Error
		if !errors.As(res.err, &coded) || coded.Code != "cancelled" || coded.Reason != "shutdown" {
			t.Fatalf("%s waiter error = %#v", name, res.err)
		}
		if len(res.reports) != 5 {
			t.Fatalf("%s: %d reports, want 5", name, len(res.reports))
		}
		for i, r := range res.reports {
			published := name == "running" && i < 3
			switch {
			case published && !r.Applied:
				t.Errorf("%s: %s lost its published candidate: %+v", name, r.Service, r)
			case !published && (r.Applied || len(r.Problems) != 1 || r.Problems[0].Code != "cancelled" || r.Problems[0].Severity != "error"):
				t.Errorf("%s: %s = %+v, want applied=false with one cancelled", name, r.Service, r)
			}
		}
	}
	for i, n := range order() {
		want := Generation(0)
		if i < 3 {
			want = 1
		}
		if got := set[n].Generation(); got != want {
			t.Errorf("%s generation = %d, want %d", n, got, want)
		}
	}
}

// Theme is skipped with a warning when Settings produced no candidate; the
// only applied:false report that carries no error.
func TestSettingsUnchangedSkipsTheme(t *testing.T) {
	set := fakeServices()
	set["settings"] = &rebuildErrService{fakeService{name: "settings"}, errors.New("boom")}
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clock()})
	defer co.Close()
	reports, err := co.Reload(context.Background(), ReasonCLI)
	if err != nil {
		t.Fatal(err)
	}
	r := reports[2]
	if r.Service != "theme" || r.Applied || r.Changed || len(r.Problems) != 1 {
		t.Fatalf("theme report: %+v", r)
	}
	p := r.Problems[0]
	if p.Code != "settings_unchanged" || p.Severity != "warning" || p.Message != "settings unchanged; palette unchanged" {
		t.Fatalf("problem: %+v", p)
	}
	if set["theme"].(*fakeService).calls.Load() != 0 {
		t.Fatal("theme rebuilt although settings produced no candidate")
	}
}

// The reload event carries each Service's changed flag.
func TestReloadedEventCarriesChanged(t *testing.T) {
	set := fakeServices()
	events := make(chan any, 2)
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: set, Clock: clock(), Publisher: c.PublishFunc(func(_ string, v any) { events <- v })})
	defer co.Close()
	if _, err := co.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(<-events)
	if got := strings.Count(string(raw), `"changed":true`); got != 5 {
		t.Fatalf("event = %s", raw)
	}
}

// waitQueued spins (no sleeps) until n requests are parked behind the active
// turn.
func waitQueued(co *Coordinator, n int) {
	for {
		co.mu.Lock()
		got := len(co.queue)
		co.mu.Unlock()
		if got >= n {
			return
		}
		runtime.Gosched()
	}
}

// Dry runs take the coordinator's turn: they wait for a running flight, a
// flight requested during a dry run waits for it, and neither coalesces with
// the other. Store.Invalidate (capture) never runs under another turn.
func TestDryRunsTakeTheCoordinatorsTurn(t *testing.T) {
	user := Caller{Kind: "cli"}
	t.Run("dry run waits for a running flight", func(t *testing.T) {
		st := &fakeStore{}
		set := fakeServices()
		gate := make(chan struct{})
		entered := make(chan struct{})
		set["profile"].(*fakeService).run = func(context.Context) { close(entered); <-gate }
		var events int
		co, _ := NewCoordinator(Options{Store: st, Services: set, Clock: clock(), Publisher: c.PublishFunc(func(string, any) { events++; st.note("published") })})
		defer co.Close()
		flight := make(chan error, 1)
		go func() { _, err := co.Reload(context.Background(), ReasonCLI); flight <- err }()
		<-entered
		dry := make(chan error, 1)
		ctx, parked := parkedCtx(context.Background())
		go func() { _, err := co.Inputs(ctx, user); dry <- err }()
		<-parked
		if got := st.trace(); len(got) != 1 {
			t.Fatalf("dry run captured under a running flight: %v", got)
		}
		close(gate)
		if err := <-dry; err != nil {
			t.Fatal(err)
		}
		if err := <-flight; err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(st.trace(), ","); got != "invalidate,published,invalidate" {
			t.Fatalf("order = %s, want the flight to publish before the dry run captures", got)
		}
		if events != 1 {
			t.Fatalf("%d services.reloaded events; a dry run must emit none", events)
		}
	})
	t.Run("flight requested during a dry run waits for it", func(t *testing.T) {
		st := &fakeStore{}
		gate := make(chan struct{})
		st.block = gate
		set := fakeServices()
		co, _ := NewCoordinator(Options{Store: st, Services: set, Clock: clock()})
		defer co.Close()
		dry := make(chan error, 1)
		go func() { _, err := co.Inputs(context.Background(), user); dry <- err }()
		for len(st.trace()) == 0 {
			runtime.Gosched()
		}
		flight := make(chan error, 1)
		go func() { _, err := co.Reload(context.Background(), ReasonCLI); flight <- err }()
		waitQueued(co, 1)
		for _, s := range set {
			if s.(*fakeService).calls.Load() != 0 {
				t.Fatal("flight ran during a dry run")
			}
		}
		if got := len(st.trace()); got != 1 {
			t.Fatalf("flight captured during a dry run: %v", st.trace())
		}
		close(gate)
		if err := <-dry; err != nil {
			t.Fatal(err)
		}
		if err := <-flight; err != nil {
			t.Fatal(err)
		}
		if got := st.trace(); len(got) != 2 {
			t.Fatalf("trace = %v", got)
		}
	})
	t.Run("dry runs never coalesce with each other or with a flight", func(t *testing.T) {
		st := &fakeStore{}
		gate := make(chan struct{})
		st.block = gate
		co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clock()})
		defer co.Close()
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); co.Inputs(context.Background(), user) }()
		}
		for len(st.trace()) == 0 {
			runtime.Gosched()
		}
		waitQueued(co, 1)
		close(gate)
		wg.Wait()
		if got := len(st.trace()); got != 2 {
			t.Fatalf("two dry runs captured %d times, want 2", got)
		}
	})
	t.Run("step 2 shares the flight deadline and typing", func(t *testing.T) {
		gate := make(chan struct{})
		st := &fakeStore{block: gate}
		clk := clock()
		co, _ := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clk})
		defer co.Close()
		done := make(chan error, 1)
		go func() { _, err := co.Inputs(context.Background(), user); done <- err }()
		<-clk.created
		clk.advance(2 * time.Second)
		var coded *c.Error
		if err := <-done; !errors.As(err, &coded) || coded.Code != "timeout" || coded.Reason != "input_deadline" {
			t.Fatalf("err = %v", err)
		}
		close(gate)
	})
	t.Run("store failure is unavailable", func(t *testing.T) {
		co, _ := NewCoordinator(Options{Store: &fakeStore{err: errors.New("x")}, Services: fakeServices(), Clock: clock()})
		defer co.Close()
		var coded *c.Error
		if _, err := co.Inputs(context.Background(), user); !errors.As(err, &coded) || coded.Code != "unavailable" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a waiter that gives up leaves the turn to the next", func(t *testing.T) {
		st := &fakeStore{}
		gate := make(chan struct{})
		entered := make(chan struct{})
		set := fakeServices()
		set["profile"].(*fakeService).run = func(context.Context) { close(entered); <-gate }
		co, _ := NewCoordinator(Options{Store: st, Services: set, Clock: clock()})
		defer co.Close()
		go co.Reload(context.Background(), ReasonCLI)
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		pctx, parked := parkedCtx(ctx)
		gone := make(chan error, 1)
		go func() { _, err := co.Inputs(pctx, user); gone <- err }()
		<-parked
		cancel()
		if err := <-gone; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		close(gate)
		if _, err := co.Inputs(context.Background(), user); err != nil {
			t.Fatalf("turn lost: %v", err)
		}
	})
}
