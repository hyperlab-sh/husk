package stem

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// claimedLoop models Root.do: either abandon a pending closure or wait for a
// running closure to finish, even after cancellation. Post remains unclaimed.
type claimedLoop struct {
	queue   chan *clearJob
	stopped chan struct{}
	once    sync.Once
	record  *order
	enter   chan struct{}
	release chan struct{}
}
type clearJob struct {
	mu        sync.Mutex
	claimed   bool
	abandoned bool
	unclaimed bool
	ctx       context.Context
	fn        func()
	done      chan struct{}
}

func newClaimedLoop(t *testing.T) *claimedLoop {
	l := &claimedLoop{queue: make(chan *clearJob, 1), stopped: make(chan struct{}), record: &order{}}
	t.Cleanup(func() { l.once.Do(func() { close(l.stopped) }) })
	return l
}
func (l *claimedLoop) Post(fn func()) bool {
	l.queue <- &clearJob{unclaimed: true, ctx: context.Background(), fn: fn, done: make(chan struct{})}
	return true
}
func (l *claimedLoop) Do(ctx context.Context, fn func()) error {
	j := &clearJob{ctx: ctx, fn: fn, done: make(chan struct{})}
	l.queue <- j
	reason := "abandoned via ctx"
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
	case <-l.stopped:
		reason = "abandoned via stop"
	}
	j.mu.Lock()
	if !j.claimed {
		j.abandoned = true
		l.record.add(reason)
		j.mu.Unlock()
		return errors.New(reason)
	}
	j.mu.Unlock()
	<-j.done
	return nil
}
func (l *claimedLoop) run(j *clearJob) {
	j.mu.Lock()
	if j.abandoned || j.ctx.Err() != nil {
		j.mu.Unlock()
		return
	}
	j.claimed = true
	j.mu.Unlock()
	if l.enter != nil {
		close(l.enter)
		<-l.release
	}
	j.fn()
	l.record.add("ran")
	close(j.done)
}

type cleanerFunc func(context.Context, string) error

func (f cleanerFunc) DeleteSlice(ctx context.Context, ns string) error { return f(ctx, ns) }

func TestClearContextEndsBeforeDelete(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, shutdown := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "allowed", true: "force"}[force], map[bool]string{false: "caller", true: "shutdown"}[shutdown]}, "/"), func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(ctxBG)
				defer cancel(nil)
				cause := error(context.Canceled)
				code := protocol.CodeCancelled
				if shutdown {
					cause = contracts.ErrShutdown
					code = protocol.CodeUnavailable
				}
				r := newClearRig(t, func(context.Context, string, map[string]any) (hooks.Outcome, error) {
					cancel(cause)
					return allowed(), nil
				})
				if force {
					cancel(cause)
				}
				wantPortCode(t, r.w.Clear(ctx, callerCLI, "atlas.state", force), code)
				r.untouched(t)
				if force && len(r.f.hooks) != 0 {
					t.Fatal("force fired hook")
				}
			})
		}
	}
}

func TestClearDeleteIgnoresCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(ctxBG)
	defer cancel()
	w, st, _ := newPort(t, StatePortOptions{Cleaner: cleanerFunc(func(deleteCtx context.Context, ns string) error {
		cancel()
		if deleteCtx.Err() != nil || deleteCtx.Done() != nil {
			return errors.New("delete inherited cancellation")
		}
		return nil
	})}, specFresh)
	if err := w.Clear(ctx, callerCLI, "atlas.state", true); err != nil {
		t.Fatal(err)
	}
	if st.Snapshot().Slices["atlas.state"].Revision != 2 {
		t.Fatal("reset did not commit")
	}
}

func TestClearQueuedResetCallerCancellation(t *testing.T) {
	l := newClaimedLoop(t)
	cl := &cleaner{}
	w, st, _ := newPort(t, StatePortOptions{Loop: l, Cleaner: cl}, specFresh)
	ctx, cancel := context.WithCancel(ctxBG)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Clear(ctx, callerCLI, "atlas.state", true) }()
	j := <-l.queue
	cancel()
	if j.unclaimed {
		// An unclaimed Post waits on the request, not the reset. Observe its
		// cancellation before releasing the closure, so a ready result cannot
		// mask the old cancellation reply.
		if err := <-done; err != nil {
			t.Fatalf("queued clear abandoned after deletion: %v", err)
		}
		t.Fatal("reset used unclaimed Post instead of Do")
	}
	l.run(j)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := l.record.got(); !reflect.DeepEqual(got, []string{"ran"}) {
		t.Fatalf("reset = %v", got)
	}
	if e := st.Snapshot().Slices["atlas.state"]; e.Revision != 2 || string(e.Value) != `{"fresh":true}` {
		t.Fatalf("reset = %+v", e)
	}
	if !reflect.DeepEqual(cl.deletedNS(), []string{"atlas.state"}) {
		t.Fatal("cleaner not called")
	}
}

func wantSkippedReset(t *testing.T, log *logLines) {
	t.Helper()
	lines := log.all()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "warn ") || !strings.Contains(lines[0], "atlas.state") || !strings.Contains(lines[0], "host is closing") || !strings.Contains(lines[0], "persisted copies are deleted") {
		t.Fatalf("warning = %q", lines)
	}
}
func TestClearQueuedResetShutdown(t *testing.T) {
	l := newClaimedLoop(t)
	cl, log := &cleaner{}, &logLines{}
	w, st, _ := newPort(t, StatePortOptions{Loop: l, Cleaner: cl, Logf: log.logf}, specFresh)
	ctx, cancel := context.WithCancelCause(ctxBG)
	defer cancel(nil)
	done := make(chan error, 1)
	go func() { done <- w.Clear(ctx, callerCLI, "atlas.state", true) }()
	j := <-l.queue
	cancel(contracts.ErrShutdown)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := l.record.got(); !reflect.DeepEqual(got, []string{"abandoned via ctx"}) {
		t.Fatalf("reset = %v", got)
	}
	l.run(j)
	if st.Snapshot().Slices["atlas.state"].Revision != 1 {
		t.Fatal("abandoned reset ran")
	}
	if !reflect.DeepEqual(cl.deletedNS(), []string{"atlas.state"}) {
		t.Fatal("cleaner not called")
	}
	wantSkippedReset(t, log)
}
func TestClearRunningResetShutdown(t *testing.T) {
	for _, unregister := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "not_found"}[unregister], func(t *testing.T) {
			l := newClaimedLoop(t)
			l.enter, l.release = make(chan struct{}), make(chan struct{})
			w, st, hs := newPort(t, StatePortOptions{Loop: l}, specFresh)
			ctx, cancel := context.WithCancelCause(ctxBG)
			defer cancel(nil)
			done := make(chan error, 1)
			go func() { done <- w.Clear(ctx, callerCLI, "atlas.state", true) }()
			j := <-l.queue
			ran := make(chan struct{})
			go func() { l.run(j); close(ran) }()
			<-l.enter
			cancel(contracts.ErrShutdown)
			// Synchronize with the derived context, not a timer.
			<-j.ctx.Done()
			select {
			case err := <-done:
				t.Fatalf("returned before running reset finished: %v", err)
			default:
			}
			if unregister {
				hs.live["atlas\x00atlas.state"].(*state.Handle).Unregister()
			}
			close(l.release)
			<-ran
			err := <-done
			if unregister {
				wantPortCode(t, err, protocol.CodeNotFound)
			} else if err != nil {
				t.Fatal(err)
			} else if st.Snapshot().Slices["atlas.state"].Revision != 2 {
				t.Fatal("reset did not commit")
			}
		})
	}
}

type refusingClearLoop struct{}

func (refusingClearLoop) Do(context.Context, func()) error { return errors.New("loop gone") }
func (refusingClearLoop) Post(func()) bool                 { return false }
func TestClearResetLoopGone(t *testing.T) {
	cl, log := &cleaner{}, &logLines{}
	w, _, _ := newPort(t, StatePortOptions{Loop: refusingClearLoop{}, Cleaner: cl, Logf: log.logf}, specFresh)
	if err := w.Clear(ctxBG, callerCLI, "atlas.state", true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cl.deletedNS(), []string{"atlas.state"}) {
		t.Fatal("cleaner not called")
	}
	wantSkippedReset(t, log)
}

func TestSetPatchStillCancelQueuedApply(t *testing.T) {
	for _, patch := range []bool{false, true} {
		t.Run(map[bool]string{false: "set", true: "patch"}[patch], func(t *testing.T) {
			l := newClaimedLoop(t)
			w, _, _ := newPort(t, StatePortOptions{Loop: l}, specFresh)
			ctx, cancel := context.WithCancel(ctxBG)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if patch {
					_, err = w.Patch(ctx, callerAtlas, "atlas.state", []byte(`{"x":1}`), 0)
				} else {
					_, err = w.Set(ctx, callerAtlas, "atlas.state", []byte(`{"x":1}`))
				}
				done <- err
			}()
			<-l.queue
			cancel()
			wantPortCode(t, <-done, protocol.CodeCancelled)
		})
	}
}
