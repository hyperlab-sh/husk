package pods

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var cliCaller = contracts.Caller{Kind: contracts.CallerCLI}

// workLoop is a Loop with a real single-goroutine executor: Do runs on it,
// Post queues onto it, and the fixture's runner is the only goroutine that
// touches the Host, as the host loop is in production.
type workLoop struct {
	*fakeLoop
	work  chan func()
	doing atomic.Int32 // Do calls, to prove a refusal never reached the loop
}

func (l *workLoop) Do(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	select {
	case l.work <- func() { fn(); close(done) }:
		l.doing.Add(1) // counted once queued: a later Do is behind it
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// removalRecorder records everything the teardown tick does, in order. It is
// the Registry, the Publisher and the Remover at once, so one slice shows the
// whole sequence. Calls on the loop append directly; the reload worker
// reports through counters.
type removalRecorder struct {
	host  *Host
	order []string // loop-confined
	data  map[string]any

	mu          sync.Mutex
	reloads     int
	reloadCtx   context.Context
	reload      func(ctx context.Context) ([]contracts.ReloadReport, error)
	undeclared  []string
	tokenGoneAt bool // the credential was already revoked when Undeclare ran
}

func (r *removalRecorder) Reserve(context.Context, string) error { return nil }
func (r *removalRecorder) SetStatus(context.Context, string, contracts.PodStatusUpdate) error {
	return nil
}
func (r *removalRecorder) Release(_ context.Context, id string) { r.order = append(r.order, "release") }

func (r *removalRecorder) Publish(name string, data any) {
	r.order = append(r.order, name)
	if name == PluginRemovedEvent {
		r.data = data.(map[string]any)
	}
}

func (r *removalRecorder) Undeclare(id string) {
	r.order = append(r.order, "undeclare")
	r.mu.Lock()
	r.undeclared = append(r.undeclared, id)
	r.mu.Unlock()
}

func (r *removalRecorder) Reload(ctx context.Context) ([]contracts.ReloadReport, error) {
	r.mu.Lock()
	r.reloads++
	r.reloadCtx = ctx
	fn := r.reload
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return []contracts.ReloadReport{{Service: "profile", Gen: 1, Applied: true}}, nil
}

func (r *removalRecorder) Added(string) {}

func (r *removalRecorder) Go(cmd tea.Cmd) {
	go func() {
		if m := cmd(); m != nil {
			r.host.loop.Post(m)
		}
	}()
}

func (r *removalRecorder) reloadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

// removalFixture is a Host over a workLoop with the recorder wired as
// Registry, Publisher and Remover, and an executor goroutine running the
// loop. Fake processes come from the same factory as hostFixture.
type removalFixture struct {
	t     *testing.T
	h     *Host
	loop  *workLoop
	clock *testclock.Clock
	rec   *removalRecorder
	procs map[string]*fakeProcess
	trace chan tea.Msg // every message the executor handled, after handling
}

func newRemovalFixture(t *testing.T) *removalFixture {
	t.Helper()
	rec := &removalRecorder{}
	loop := &workLoop{fakeLoop: &fakeLoop{events: make(chan tea.Msg, 128), ctx: t.Context()}, work: make(chan func(), 128)}
	clock := testclock.New()
	f := &removalFixture{t: t, loop: loop, clock: clock, rec: rec, procs: map[string]*fakeProcess{}, trace: make(chan tea.Msg, 256)}
	h, err := New(loop, Options{
		Wrapper:   wrapper.Options{Clock: clock},
		Registry:  rec,
		Publisher: rec,
		Workers:   rec,
		Factory: func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
			p := &fakeProcess{id: s.ID, info: wrapper.Info{PluginID: s.ID, Status: wrapper.Starting, LogPath: "/logs/" + s.ID + ".log"}, opts: o, done: make(chan struct{}), term: make(chan struct{})}
			f.procs[s.ID] = p
			return p, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.cancel)
	f.h, rec.host = h, h
	go func() {
		for {
			select {
			case fn := <-loop.work:
				fn()
			case m := <-loop.events:
				h.Handle(m)
				select {
				case f.trace <- m:
				default:
				}
			case <-t.Context().Done():
				return
			}
		}
	}()
	return f
}

// onLoop runs fn on the executor and waits for it.
func (f *removalFixture) onLoop(fn func()) {
	f.t.Helper()
	if err := f.loop.Do(f.t.Context(), fn); err != nil {
		f.t.Fatal(err)
	}
}

// add registers a Pod and, when launch is set, runs its gate so it is live.
func (f *removalFixture) add(id string, launch bool) {
	f.t.Helper()
	f.onLoop(func() { addPod(f.t, f.h, id) })
	if !launch {
		return
	}
	var cmd tea.Cmd
	f.onLoop(func() { cmd = prepareAll(f.h) })
	msg := cmd()
	f.onLoop(func() { f.h.Handle(msg) })
}

// await returns once the executor has handled a message of type T.
func awaitMsg[T tea.Msg](f *removalFixture) T {
	f.t.Helper()
	for {
		m := testclock.Receive(f.t, f.trace)
		if v, ok := m.(T); ok {
			return v
		}
	}
}

// remove starts plugin.remove from a handler goroutine and returns its reply
// channel.
type removeReply struct {
	res contracts.PluginRemoval
	err error
}

func (f *removalFixture) remove(ctx context.Context, id string) <-chan removeReply {
	out := make(chan removeReply, 1)
	go func() {
		res, err := f.h.Remove(ctx, cliCaller, id)
		out <- removeReply{res, err}
	}()
	return out
}

func (f *removalFixture) podStatus(id string) (wrapper.Liveness, bool) {
	var status wrapper.Liveness
	var ok bool
	f.onLoop(func() {
		if p, found := f.h.Pod(id); found {
			status, ok = p.Info().Status, true
		}
	})
	return status, ok
}

func (f *removalFixture) removingCount() int {
	n := 0
	f.onLoop(func() { n = len(f.h.removing) })
	return n
}

// ---- admission and lookup (steps 1-2) ----

func TestRemoveAdmissionAndLookup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		caller   contracts.Caller
		id       string
		closing  bool
		wantCode protocol.Code
		wantDo   int32
		wantData string
	}{
		{"plugin caller is denied before any lookup", contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "a"}, "a", false, protocol.CodeDenied, 0, ""},
		{"host caller reaches lookup", contracts.Caller{Kind: contracts.CallerHost}, "ghost", false, protocol.CodeNotFound, 1, ""},
		{"input caller is denied", contracts.Caller{Kind: contracts.CallerInput}, "a", false, protocol.CodeDenied, 0, ""},
		{"zero caller is denied", contracts.Caller{}, "a", false, protocol.CodeDenied, 0, ""},
		{"empty id is invalid_params", cliCaller, "", false, protocol.CodeInvalidParams, 0, ""},
		{"unknown id is not_found", cliCaller, "ghost", false, protocol.CodeNotFound, 1, ""},
		{"shutdown begun is unavailable", cliCaller, "a", true, protocol.CodeUnavailable, 1, "shutdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemovalFixture(t)
			f.add("a", true)
			if tc.closing {
				f.onLoop(func() { f.h.Stop() })
			}
			base := f.loop.doing.Load()
			_, err := f.h.Remove(t.Context(), tc.caller, tc.id)
			if contracts.CodeOf(err) != string(tc.wantCode) {
				t.Fatalf("code = %q, want %q (%v)", contracts.CodeOf(err), tc.wantCode, err)
			}
			if tc.wantData != "" {
				de, ok := err.(contracts.DataError)
				if !ok || de.Data()["reason"] != tc.wantData {
					t.Fatalf("error data = %v, want reason %q", err, tc.wantData)
				}
			}
			if got := f.loop.doing.Load() - base; got != tc.wantDo {
				t.Fatalf("Do calls by Remove = %d, want %d", got, tc.wantDo)
			}
			if status, ok := f.podStatus("a"); !ok || (status != wrapper.Live) {
				t.Fatalf("a refusal changed the Pod: %v %v", status, ok)
			}
		})
	}
}

func TestRemoveWithoutALoopIsUnavailable(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	addPod(t, h, "a")
	h.loop = deadLoop{}
	if _, err := h.Remove(t.Context(), cliCaller, "a"); contracts.CodeOf(err) != string(protocol.CodeUnavailable) {
		t.Fatalf("remove without a loop: %v", err)
	}
}

// ---- Pod states at step 3 ----

// An exited or failed Pod has no process: the teardown runs inline in the
// same Do, so no message is posted for it, and the reply carries the status
// the Pod had.
func TestRemoveOfExitedAndFailedPodTearsDownInline(t *testing.T) {
	for _, status := range []wrapper.Liveness{wrapper.Exited, wrapper.Failed} {
		t.Run(string(status), func(t *testing.T) {
			f := newRemovalFixture(t)
			f.add("a", false)
			f.onLoop(func() {
				p, _ := f.h.Pod("a")
				p.info.Status, p.info.LogPath, p.info.LastError = status, "/logs/a.log", "boom"
			})
			// The reload is held so no worker message can be mistaken for a
			// post made by on-loop code.
			release := make(chan struct{})
			f.rec.mu.Lock()
			f.rec.reload = func(context.Context) ([]contracts.ReloadReport, error) {
				<-release
				return []contracts.ReloadReport{{Service: "profile", Applied: true}}, nil
			}
			f.rec.mu.Unlock()
			var gone bool
			var posted int
			f.onLoop(func() {
				before := len(f.loop.events)
				if _, err := f.h.beginRemove("a"); err != nil {
					t.Error(err)
				}
				_, found := f.h.Pod("a")
				gone = !found
				posted = len(f.loop.events) - before
			})
			if !gone {
				t.Fatal("the Pod survived its inline teardown")
			}
			if posted != 0 {
				t.Fatalf("inline teardown posted %d message(s); on-loop code never posts", posted)
			}
			close(release)
			done := awaitMsg[RemoveDoneMsg](f)
			if done.PluginID != "a" || len(done.Reports) != 1 {
				t.Fatalf("RemoveDoneMsg = %+v", done)
			}
			// The reload worker's RemoveDoneMsg is the only message: no process, no TeardownMsg.
			if f.procs["a"] != nil {
				t.Fatal("a process was built for a Pod that never launched")
			}
		})
	}
}

func TestRemoveReplyCarriesStatusAndLogOfExitedAndFailedPods(t *testing.T) {
	for _, status := range []wrapper.Liveness{wrapper.Exited, wrapper.Failed} {
		t.Run(string(status), func(t *testing.T) {
			f := newRemovalFixture(t)
			f.add("a", false)
			f.onLoop(func() {
				p, _ := f.h.Pod("a")
				p.info.Status, p.info.LogPath = status, "/logs/a.log"
			})
			res, err := f.h.Remove(t.Context(), cliCaller, "a")
			if err != nil {
				t.Fatal(err)
			}
			if res.Plugin != "a" || res.Status != contracts.PodStatus(status) || res.Log != "/logs/a.log" || len(res.Reports) != 1 {
				t.Fatalf("reply = %+v", res)
			}
			if _, err := f.h.Remove(t.Context(), cliCaller, "a"); contracts.CodeOf(err) != string(protocol.CodeNotFound) {
				t.Fatalf("a request after the deletion = %v, want not_found", err)
			}
		})
	}
}

// A live Pod gets the shutdown stop for itself alone: marked stopped by Husk,
// stdin closed, SIGTERM; the worker ends by posting TeardownMsg. The shared
// process context (every other plugin's) is never cancelled.
func TestRemoveOfLivePodStopsOnlyThatProcess(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	f.add("b", true)
	res, err := f.h.Remove(t.Context(), cliCaller, "a")
	if err != nil {
		t.Fatal(err)
	}
	pa, pb := f.procs["a"], f.procs["b"]
	if !pa.marked || !pa.closed || !pa.terminated {
		t.Fatalf("stop policy: marked=%v closed=%v terminated=%v", pa.marked, pa.closed, pa.terminated)
	}
	if pb.marked || pb.closed || pb.terminated {
		t.Fatal("removing a stopped another plugin's process")
	}
	if f.h.ctx.Err() != nil {
		t.Fatal("removal cancelled the shared process context")
	}
	if res.Status != contracts.PodExited || res.Log != "/logs/a.log" {
		t.Fatalf("reply = %+v", res)
	}
	if _, ok := f.h.Resolve(pa.Token()); ok {
		t.Fatal("the removed plugin's token still resolves")
	}
	if status, _ := f.podStatus("b"); status != wrapper.Live {
		t.Fatalf("b = %v", status)
	}
}

// A starting Pod belongs to a launch gate, pending or not yet requested. The
// removal waits for the gate, the gate does not start the Pod, and the
// gate-completion handler tears it down directly (no TeardownMsg, nothing
// written to husk.pods).
func TestRemoveOfStartingPodWaitsForGateAndNeverStartsIt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		gateRequired bool // the gate was requested before the removal
	}{
		{"gate pending", true},
		{"gate not yet requested", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemovalFixture(t)
			f.add("a", false)
			f.add("b", false)
			var cmd tea.Cmd
			if tc.gateRequired {
				f.onLoop(func() { cmd = prepareAll(f.h) })
			}
			reply := f.remove(t.Context(), "a")
			// The removal is registered and waiting; the Pod is untouched.
			for f.removingCount() == 0 {
				select {
				case r := <-reply:
					t.Fatalf("replied before the gate completed: %+v", r)
				default:
				}
			}
			if status, ok := f.podStatus("a"); !ok || status != wrapper.Starting {
				t.Fatalf("a = %v %v while its gate is open", status, ok)
			}
			select {
			case r := <-reply:
				t.Fatalf("replied before the gate completed: %+v", r)
			default:
			}
			if !tc.gateRequired {
				f.onLoop(func() { cmd = prepareAll(f.h) })
			}
			var writes int
			f.onLoop(func() { writes = len(f.loop.statuses) })
			msg := cmd()
			f.onLoop(func() { f.h.Handle(msg) })
			r := testclock.Receive(t, reply)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.res.Status != contracts.PodStarting {
				t.Fatalf("status = %q, want starting", r.res.Status)
			}
			if f.procs["a"] != nil {
				t.Fatal("the gate started a removing Pod")
			}
			if status, _ := f.podStatus("b"); status != wrapper.Live || f.procs["b"] == nil {
				t.Fatal("the rest of the gate's batch did not launch")
			}
			// Only b's live write; nothing for the removed Pod.
			f.onLoop(func() {
				for _, s := range f.loop.statuses[writes:] {
					if s.Info.PluginID == "a" {
						t.Errorf("husk.pods written for a removing Pod: %+v", s)
					}
				}
			})
		})
	}
}

// ---- step 4: stop ----

func TestRemoveOfUnreapableProcessContinuesAsFailed(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	f.procs["a"].ignore, f.procs["a"].stubborn = true, true
	reply := f.remove(t.Context(), "a")
	f.clock.Await(t, wrapper.StopGrace)
	f.clock.Advance(wrapper.StopGrace)
	f.clock.Await(t, wrapper.KillWait)
	f.clock.Advance(wrapper.KillWait)
	r := testclock.Receive(t, reply)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.Status != contracts.PodFailed {
		t.Fatalf("status = %q, want failed", r.res.Status)
	}
	if !f.procs["a"].killed {
		t.Fatal("no SIGKILL after the grace")
	}
	if _, ok := f.podStatus("a"); ok {
		t.Fatal("removal did not continue past the unreapable process")
	}
	var warned int
	f.onLoop(func() {
		for _, l := range f.loop.logs {
			if strings.Contains(l.Message, "did not exit within 1s of SIGKILL") {
				warned++
			}
		}
	})
	if warned != 1 {
		t.Fatalf("SIGKILL warnings = %d, want one", warned)
	}
	var failedWritten bool
	f.onLoop(func() {
		for _, m := range f.loop.statuses {
			if m.Info.Status == wrapper.Failed && m.Info.LastError == unreapedError {
				failedWritten = true
			}
		}
	})
	if !failedWritten {
		t.Fatal("liveness failed with last_error was never written before the entry was released")
	}
	if f.rec.reloadCount() != 1 {
		t.Fatal("no reload after the unreapable process")
	}
}

// ---- steps 5-7: the teardown tick ----

// The whole tick, in order: credential revoked, slice unregistered, registry
// id released, Pod deleted, documents undeclared (after the Pod is gone),
// pods.plugin-exited before pods.plugin-removed, and the reload last.
func TestTeardownTickOrderAndContent(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", false)
	handle := &fakeHandle{namespace: "a.state"}
	var gate uint64
	f.onLoop(func() { gate = f.h.nextGate + 1; prepareAll(f.h) })
	var done chan Registered = make(chan Registered, 1)
	f.onLoop(func() {
		f.h.Handle(RegisterSlicesMsg{Gate: gate, Register: map[string]func() (contracts.SliceHandle, error){"a": func() (contracts.SliceHandle, error) { return handle, nil }}, Done: done})
	})
	testclock.Receive(t, done)
	// Launch it: the gate completes with a live process.
	f.onLoop(func() { f.h.Handle(PreparedMsg{Gate: gate}) })
	if f.procs["a"] == nil {
		t.Fatal("a did not launch")
	}
	token := f.procs["a"].Token()

	var atUndeclare struct{ tokenLive, podPresent, slicePresent bool }
	orig := f.h.opts.Workers
	f.h.opts.Workers = undeclareProbe{Workers: orig, probe: func() {
		_, atUndeclare.tokenLive = f.h.Resolve(token)
		_, atUndeclare.podPresent = f.h.Pod("a")
		atUndeclare.slicePresent = !handle.unregistered
	}}
	res, err := f.h.Remove(t.Context(), cliCaller, "a")
	if err != nil {
		t.Fatal(err)
	}
	if atUndeclare.tokenLive || atUndeclare.podPresent || atUndeclare.slicePresent {
		t.Fatalf("at undeclare: token live=%v pod present=%v slice registered=%v", atUndeclare.tokenLive, atUndeclare.podPresent, atUndeclare.slicePresent)
	}
	var order []string
	f.onLoop(func() { order = slices.Clone(f.rec.order) })
	// The fake process posts no ExitMsg, so the teardown synthesizes the
	// exited write (a Starter-owned process): exit, then the tick, then removed.
	want := []string{PluginExitedEvent, "release", "undeclare", PluginRemovedEvent}
	if !slices.Equal(order, want) {
		t.Fatalf("tick order = %v, want %v", order, want)
	}
	if f.rec.data["plugin_id"] != "a" || f.rec.data["status"] != "exited" {
		t.Fatalf("pods.plugin-removed payload = %v", f.rec.data)
	}
	if res.Status != contracts.PodExited {
		t.Fatal("reply status", res.Status)
	}
	f.rec.mu.Lock()
	reloadCtx := f.rec.reloadCtx
	f.rec.mu.Unlock()
	if reloadCtx != f.h.ctx {
		t.Fatal("the reload did not run under the Host's context")
	}
}

type undeclareProbe struct {
	Workers
	probe func()
}

func (u undeclareProbe) Undeclare(id string) { u.probe(); u.Workers.Undeclare(id) }

// A TeardownMsg for an id with no removing entry is stale (shutdown completed
// the removal, or a test posted it) and a RemoveDoneMsg likewise.
func TestStaleRemovalMessagesAreIgnored(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	f.onLoop(func() {
		f.h.Handle(TeardownMsg{PluginID: "a"})
		f.h.Handle(RemoveDoneMsg{PluginID: "a"})
		f.h.Handle(TeardownMsg{PluginID: "ghost"})
		f.h.Handle(RemoveDoneMsg{PluginID: "ghost"})
		if p, ok := f.h.Pod("a"); !ok || p.Info().Status != wrapper.Live {
			t.Error("a stale TeardownMsg tore the Pod down")
		}
		if len(f.rec.order) != 0 {
			t.Errorf("stale messages did work: %v", f.rec.order)
		}
	})
	if f.rec.reloadCount() != 0 {
		t.Fatal("a stale message started a reload")
	}
}

// ---- joining, interleaving, disconnect ----

// Two requests for one id share one removal: one stop, one teardown, one
// reload, one reply. Holding the stop open guarantees the second request
// arrives mid-removal.
func TestConcurrentRemovesOfOneIdJoin(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	release := make(chan struct{})
	stopping := make(chan struct{})
	f.procs["a"].termStep = func() { close(stopping); <-release }
	first := f.remove(t.Context(), "a")
	<-stopping
	base := f.loop.doing.Load()
	second := f.remove(t.Context(), "a")
	for f.loop.doing.Load() == base { // the second request's Do is queued...
		select {
		case r := <-second:
			t.Fatalf("the joined request replied early: %+v", r)
		default:
		}
	}
	f.onLoop(func() {}) // ...and has run once this barrier behind it returns
	select {
	case r := <-second:
		t.Fatalf("the joined request replied before the removal finished: %+v", r)
	default:
	}
	close(release)
	r1, r2 := testclock.Receive(t, first), testclock.Receive(t, second)
	if r1.err != nil || r2.err != nil {
		t.Fatalf("errors: %v %v", r1.err, r2.err)
	}
	if r1.res.Plugin != "a" || r1.res.Status != contracts.PodExited || r1.res.Log != r2.res.Log || r1.res.Status != r2.res.Status || len(r1.res.Reports) != len(r2.res.Reports) {
		t.Fatalf("replies differ: %+v vs %+v", r1.res, r2.res)
	}
	if n := f.rec.reloadCount(); n != 1 {
		t.Fatalf("reloads = %d, want 1", n)
	}
	var removed int
	f.onLoop(func() {
		for _, e := range f.rec.order {
			if e == PluginRemovedEvent {
				removed++
			}
		}
	})
	if removed != 1 {
		t.Fatalf("pods.plugin-removed published %d times", removed)
	}
}

// Removals of different ids run independently: each has its own stop worker
// and reload, and one's slow stop does not hold the other's reply.
func TestRemovesOfDifferentIdsInterleave(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	f.add("b", true)
	release := make(chan struct{})
	stopping := make(chan struct{})
	f.procs["a"].termStep = func() { close(stopping); <-release }
	slow := f.remove(t.Context(), "a")
	<-stopping
	fast := f.remove(t.Context(), "b")
	rb := testclock.Receive(t, fast)
	if rb.err != nil || rb.res.Plugin != "b" {
		t.Fatalf("b = %+v", rb)
	}
	select {
	case r := <-slow:
		t.Fatalf("a replied while its stop was held: %+v", r)
	default:
	}
	if status, ok := f.podStatus("a"); !ok || status != wrapper.Live {
		t.Fatalf("a = %v %v mid-stop", status, ok)
	}
	close(release)
	ra := testclock.Receive(t, slow)
	if ra.err != nil || ra.res.Plugin != "a" {
		t.Fatalf("a = %+v", ra)
	}
	if n := f.rec.reloadCount(); n != 2 {
		t.Fatalf("reloads = %d, want 2", n)
	}
}

// A caller whose ctx ends after step 2 detaches only itself: the removal runs
// to completion and a later list shows the plugin gone.
func TestRemoveRunsToCompletionAfterCallerDisconnects(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	release := make(chan struct{})
	stopping := make(chan struct{})
	f.procs["a"].termStep = func() { close(stopping); <-release }
	ctx, cancel := context.WithCancel(t.Context())
	reply := f.remove(ctx, "a")
	<-stopping
	cancel()
	if r := testclock.Receive(t, reply); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("a disconnected caller got %+v", r)
	}
	close(release)
	awaitMsg[RemoveDoneMsg](f)
	if _, ok := f.podStatus("a"); ok {
		t.Fatal("the removal undid itself when its caller left")
	}
	list, err := f.h.List(t.Context(), cliCaller)
	if err != nil || len(list) != 0 {
		t.Fatalf("plugin.list after the removal = %v %v", list, err)
	}
	if f.removingCount() != 0 {
		t.Fatal("the removing entry outlived the removal")
	}
}

// A reload that applied nothing is still a successful removal: the reports
// say so.
func TestRemoveWithUnappliedReloadIsASuccess(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", false)
	f.rec.reload = func(context.Context) ([]contracts.ReloadReport, error) {
		return []contracts.ReloadReport{{Service: "settings", Gen: 3, Applied: false, Problems: []protocol.Problem{{Severity: "error", Message: "timed out"}}}}, nil
	}
	f.onLoop(func() { p, _ := f.h.Pod("a"); p.info.Status = wrapper.Exited })
	res, err := f.h.Remove(t.Context(), cliCaller, "a")
	if err != nil {
		t.Fatalf("a reload with applied:false made the call an error: %v", err)
	}
	if len(res.Reports) != 1 || res.Reports[0].Applied || len(res.Reports[0].Problems) != 1 {
		t.Fatalf("reports = %+v", res.Reports)
	}
}

// ---- shutdown takeover (invariant 7) ----

// Shutdown before step 5: the mark completes the entry with cancelled /
// shutdown and no reports; nothing runs after it, even when the stop worker
// finishes and posts its TeardownMsg.
func TestShutdownBeforeTeardownCancelsAndNothingFurtherRuns(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	handle := &fakeHandle{namespace: "a.state"}
	f.onLoop(func() { f.h.handles["a"] = handle })
	release := make(chan struct{})
	stopping := make(chan struct{})
	f.procs["a"].termStep = func() { close(stopping); <-release }
	reply := f.remove(t.Context(), "a")
	<-stopping
	f.onLoop(func() { f.h.Stop() })
	r := testclock.Receive(t, reply)
	if contracts.CodeOf(r.err) != string(protocol.CodeCancelled) {
		t.Fatalf("reply = %+v, want cancelled", r)
	}
	if de, ok := r.err.(contracts.DataError); !ok || de.Data()["reason"] != "shutdown" {
		t.Fatalf("cancelled without data.reason shutdown: %v", r.err)
	}
	if len(r.res.Reports) != 0 {
		t.Fatal("reports on a cancelled removal")
	}
	close(release)
	awaitMsg[TeardownMsg](f)
	f.onLoop(func() {
		if _, ok := f.h.Pod("a"); !ok {
			t.Error("a late TeardownMsg removed the Pod after the shutdown mark")
		}
		if len(f.h.removing) != 0 {
			t.Error("the entry survived the mark")
		}
	})
	if handle.unregistered || len(f.rec.undeclared) != 0 || f.rec.reloadCount() != 0 {
		t.Fatalf("work after the shutdown mark: unregistered=%v undeclared=%v reloads=%d", handle.unregistered, f.rec.undeclared, f.rec.reloadCount())
	}
}

// Shutdown during step 7: teardown and undeclaration already happened, the
// waiting handler is answered cancelled, and the late RemoveDoneMsg is
// ignored.
func TestShutdownDuringReloadCancelsTheWaiter(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	inReload := make(chan struct{})
	release := make(chan struct{})
	f.rec.reload = func(context.Context) ([]contracts.ReloadReport, error) {
		close(inReload)
		<-release
		return []contracts.ReloadReport{{Service: "profile", Applied: true}}, nil
	}
	reply := f.remove(t.Context(), "a")
	<-inReload
	f.onLoop(func() { f.h.Stop() })
	r := testclock.Receive(t, reply)
	if contracts.CodeOf(r.err) != string(protocol.CodeCancelled) || len(r.res.Reports) != 0 {
		t.Fatalf("reply = %+v, want cancelled with no reports", r)
	}
	if got := f.rec.undeclared; len(got) != 1 || got[0] != "a" {
		t.Fatalf("undeclared = %v: step 6 happens before step 7", got)
	}
	close(release)
	awaitMsg[RemoveDoneMsg](f)
	if n := f.removingCount(); n != 0 {
		t.Fatalf("removing entries = %d", n)
	}
}

// A reload cut short with no report at all (the coordinator closed) is the
// cancellation too, with no mark involved.
func TestReloadCutShortAnswersCancelled(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", false)
	f.onLoop(func() { p, _ := f.h.Pod("a"); p.info.Status = wrapper.Exited })
	f.rec.reload = func(ctx context.Context) ([]contracts.ReloadReport, error) { return nil, context.Canceled }
	_, err := f.h.Remove(t.Context(), cliCaller, "a")
	if contracts.CodeOf(err) != string(protocol.CodeCancelled) {
		t.Fatalf("err = %v", err)
	}
}

// The shutdown mark is checked first, whatever the entry map holds: a
// teardown or completion that reaches a closing Host does nothing even for an
// id that still has an entry.
func TestRemovalHandlersCheckTheShutdownMarkFirst(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", true)
	handle := &fakeHandle{namespace: "a.state"}
	rm := &removal{done: make(chan struct{})}
	f.onLoop(func() {
		f.h.handles["a"] = handle
		f.h.removing["a"] = rm
		f.h.closing = true
		f.h.Handle(TeardownMsg{PluginID: "a"})
		f.h.Handle(RemoveDoneMsg{PluginID: "a"})
		if _, ok := f.h.Pod("a"); !ok || handle.unregistered || len(f.rec.undeclared) != 0 {
			t.Error("teardown ran after the shutdown mark")
		}
		select {
		case <-rm.done:
			t.Error("the entry completed after the shutdown mark")
		default:
		}
	})
	if f.rec.data != nil {
		t.Fatal("removed event after the shutdown mark")
	}
}

// A reload that returns no report for a reason other than a context ending is
// not a shutdown: it must not be labelled cancelled/shutdown.
func TestReloadFailureWithoutReportsIsInternalNotCancelled(t *testing.T) {
	f := newRemovalFixture(t)
	f.add("a", false)
	f.onLoop(func() { p, _ := f.h.Pod("a"); p.info.Status = wrapper.Exited })
	f.rec.reload = func(context.Context) ([]contracts.ReloadReport, error) {
		return nil, errors.New("Store resolution failed")
	}
	_, err := f.h.Remove(t.Context(), cliCaller, "a")
	if contracts.CodeOf(err) != string(protocol.CodeInternal) {
		t.Fatalf("err = %v, want internal", err)
	}
}

// A request whose context ended before the loop ran its closure reports the
// context error, not a dead loop.
func TestRemoveWithEndedContextReportsTheContext(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	addPod(t, h, "a")
	h.loop = deadLoop{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.Remove(ctx, cliCaller, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
