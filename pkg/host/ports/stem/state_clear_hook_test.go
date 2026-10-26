package stem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

// order records the sequence of observable steps across fakes.
type order struct {
	mu    sync.Mutex
	steps []string
}

func (o *order) add(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, s)
}

func (o *order) got() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

// orderedCleaner is a SliceCleaner that notes its call in an order.
type orderedCleaner struct{ o *order }

func (c orderedCleaner) DeleteSlice(_ context.Context, ns string) error {
	c.o.add("delete " + ns)
	return nil
}

// fakeFirer is a HookFirer answering from fn and recording every call.
type fakeFirer struct {
	o     *order
	fn    func(ctx context.Context, hook string, data map[string]any) (hooks.Outcome, error)
	hooks []string
	data  []map[string]any
}

func (f *fakeFirer) Fire(ctx context.Context, hook string, data map[string]any) (hooks.Outcome, error) {
	f.hooks = append(f.hooks, hook)
	f.data = append(f.data, data)
	if f.o != nil {
		f.o.add("fire " + hook)
	}
	return f.fn(ctx, hook, data)
}

func answer(o hooks.Outcome, err error) func(context.Context, string, map[string]any) (hooks.Outcome, error) {
	return func(context.Context, string, map[string]any) (hooks.Outcome, error) { return o, err }
}

func hookErr(code protocol.Code, msg string) error {
	return &hooks.HookError{Code: code, Message: msg}
}

func allowed(results ...hooks.HookResult) hooks.Outcome {
	return hooks.Outcome{Kind: hooks.Gate, Allowed: true, Results: results}
}

func vetoed(vetoes ...hooks.Veto) hooks.Outcome {
	var results []hooks.HookResult
	for _, v := range vetoes {
		results = append(results, hooks.HookResult{Plugin: v.Plugin, Veto: true, Reason: v.Reason})
	}
	return hooks.Outcome{Kind: hooks.Gate, Vetoes: vetoes, Results: results}
}

func shutdownLeg(plugin string) hooks.HookResult {
	return hooks.HookResult{Plugin: plugin, Err: &hooks.HookError{
		Code: protocol.CodeCancelled, Message: "husk is shutting down", Data: map[string]any{"reason": "shutdown"},
	}}
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

// logf records "level message".
func (l *logLines) logf(level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, args...))
}

func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// clearRig is a State port over atlas.state (dirty) with a firer and an
// ordered cleaner.
type clearRig struct {
	w   *State
	st  *state.State
	f   *fakeFirer
	ord *order
	log *logLines
}

func newClearRig(t *testing.T, fn func(context.Context, string, map[string]any) (hooks.Outcome, error), specs ...state.Spec) *clearRig {
	t.Helper()
	if len(specs) == 0 {
		specs = []state.Spec{specFresh}
	}
	r := &clearRig{ord: &order{}, log: &logLines{}}
	r.f = &fakeFirer{o: r.ord, fn: fn}
	r.w, r.st, _ = newPort(t, StatePortOptions{Cleaner: orderedCleaner{r.ord}, Hooks: r.f, Logf: r.log.logf}, specs...)
	if _, err := r.w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"dirty":1}`)); err != nil {
		t.Fatal(err)
	}
	return r
}

// untouched asserts the slice kept its dirty value and nothing was deleted.
func (r *clearRig) untouched(t *testing.T) {
	t.Helper()
	for _, s := range r.ord.got() {
		if strings.HasPrefix(s, "delete") {
			t.Fatalf("a delete ran: %v", r.ord.got())
		}
	}
	res, err := r.w.Get(ctxBG, callerCLI, "atlas.state", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Value) != `{"dirty":1}` {
		t.Fatalf("State.Clear ran: value = %s", res.Value)
	}
}

func (r *clearRig) cleared(t *testing.T) {
	t.Helper()
	res, err := r.w.Get(ctxBG, callerCLI, "atlas.state", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Value) != `{"fresh":true}` {
		t.Fatalf("slice not cleared: value = %s", res.Value)
	}
}

func dataOf(t *testing.T, err error) map[string]any {
	t.Helper()
	de, ok := err.(contracts.DataError)
	if !ok {
		t.Fatalf("%v carries no data", err)
	}
	return de.Data()
}

func TestClearVetoIsDenied(t *testing.T) {
	t.Run("one veto", func(t *testing.T) {
		r := newClearRig(t, answer(vetoed(hooks.Veto{Plugin: "atlas", Reason: "unsaved edits"}), nil))
		err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false)
		wantPortCode(t, err, protocol.CodeDenied)
		want := map[string]any{
			"reason": "vetoed",
			"vetoes": []map[string]any{{"plugin_id": "atlas", "reason": "unsaved edits"}},
		}
		if got := dataOf(t, err); !reflect.DeepEqual(got, want) {
			t.Fatalf("data = %#v, want %#v", got, want)
		}
		r.untouched(t)
	})
	t.Run("two vetoes are both listed, in order", func(t *testing.T) {
		r := newClearRig(t, answer(vetoed(hooks.Veto{Plugin: "atlas", Reason: "a"}, hooks.Veto{Plugin: "zed", Reason: "z"}), nil))
		err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false)
		wantPortCode(t, err, protocol.CodeDenied)
		want := []map[string]any{{"plugin_id": "atlas", "reason": "a"}, {"plugin_id": "zed", "reason": "z"}}
		if got := dataOf(t, err)["vetoes"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("vetoes = %#v, want %#v", got, want)
		}
		r.untouched(t)
	})
}

func TestClearAllowedFiresThenDeletesThenClears(t *testing.T) {
	r := newClearRig(t, answer(allowed(hooks.HookResult{Plugin: "atlas"}), nil))
	if err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false); err != nil {
		t.Fatal(err)
	}
	if got, want := r.ord.got(), []string{"fire husk.before-state-clear", "delete atlas.state"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	r.cleared(t)
}

func TestClearLogsASubscriberThatDidNotAnswer(t *testing.T) {
	late := hooks.HookResult{Plugin: "slow", Err: &hooks.HookError{Code: protocol.CodeTimeout, Message: "no reply within 2s"}}
	r := newClearRig(t, answer(allowed(hooks.HookResult{Plugin: "atlas"}, late), nil))
	if err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false); err != nil {
		t.Fatal(err)
	}
	r.cleared(t)
	lines := r.log.all()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "info ") || !strings.Contains(lines[0], "slow") || !strings.Contains(lines[0], "timeout") {
		t.Fatalf("log = %q, want one line naming the silent subscriber and its timeout", lines)
	}
}

func TestClearHookData(t *testing.T) {
	t.Run("a plugin slice names its plugin and carries no value", func(t *testing.T) {
		r := newClearRig(t, answer(allowed(), nil))
		if err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"slice": "atlas.state", "owner": "atlas"}
		if len(r.f.data) != 1 || !reflect.DeepEqual(r.f.data[0], want) {
			t.Fatalf("hook data = %#v, want exactly %#v", r.f.data, want)
		}
	})
	t.Run("a host slice names husk", func(t *testing.T) {
		husk := state.Spec{Namespace: "husk.notes", Owner: state.OwnerHusk, Scope: state.ScopeInstance}
		r := newClearRig(t, answer(allowed(), nil), specFresh, husk)
		if err := r.w.Clear(ctxBG, callerCLI, "husk.notes", false); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"slice": "husk.notes", "owner": "husk"}
		if len(r.f.data) != 1 || !reflect.DeepEqual(r.f.data[0], want) {
			t.Fatalf("hook data = %#v, want exactly %#v", r.f.data, want)
		}
	})
}

func TestClearForceSkipsTheHook(t *testing.T) {
	r := newClearRig(t, answer(vetoed(hooks.Veto{Plugin: "atlas"}), nil))
	if err := r.w.Clear(ctxBG, callerCLI, "atlas.state", true); err != nil {
		t.Fatal(err)
	}
	if len(r.f.hooks) != 0 {
		t.Fatalf("firer called with force: %v", r.f.hooks)
	}
	r.cleared(t)
}

func TestClearBusyFailsOpen(t *testing.T) {
	for _, msg := range []string{"too many pending hook calls for one subscriber", "too many pending hook calls", "16 calls already in flight"} {
		t.Run(msg, func(t *testing.T) {
			r := newClearRig(t, answer(hooks.Outcome{}, hookErr(protocol.CodeBusy, msg)))
			if err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false); err != nil {
				t.Fatal(err)
			}
			r.cleared(t)
			lines := r.log.all()
			if len(lines) != 1 || !strings.HasPrefix(lines[0], "warn ") || !strings.Contains(lines[0], msg) {
				t.Fatalf("log = %q, want one warn line holding the refusal", lines)
			}
		})
	}
}

func TestClearHostBugRefusalsAreInternal(t *testing.T) {
	for _, code := range []protocol.Code{protocol.CodeDenied, protocol.CodeNotFound, protocol.CodeInvalidParams} {
		t.Run(string(code), func(t *testing.T) {
			r := newClearRig(t, answer(hooks.Outcome{}, hookErr(code, "the list is wrong")))
			wantPortCode(t, r.w.Clear(ctxBG, callerCLI, "atlas.state", false), protocol.CodeInternal)
			r.untouched(t)
			lines := r.log.all()
			if len(lines) != 1 || !strings.HasPrefix(lines[0], "error ") || !strings.Contains(lines[0], "the list is wrong") {
				t.Fatalf("log = %q, want the refusal logged once at error", lines)
			}
		})
	}
	t.Run("an uncoded error", func(t *testing.T) {
		r := newClearRig(t, answer(hooks.Outcome{}, errors.New("boom")))
		wantPortCode(t, r.w.Clear(ctxBG, callerCLI, "atlas.state", false), protocol.CodeInternal)
		r.untouched(t)
		if lines := r.log.all(); len(lines) != 1 || !strings.HasPrefix(lines[0], "error ") || !strings.Contains(lines[0], "boom") {
			t.Fatalf("log = %q, want one error line", lines)
		}
	})
}

func TestClearUnavailableAndShutdown(t *testing.T) {
	cases := []struct {
		name string
		out  hooks.Outcome
		err  error
	}{
		{"loop gone", hooks.Outcome{}, hookErr(protocol.CodeUnavailable, "the host loop no longer runs")},
		{"allow plus a shutdown leg", allowed(hooks.HookResult{Plugin: "a"}, shutdownLeg("b")), nil},
		{"veto plus a shutdown leg: shutdown wins", func() hooks.Outcome {
			o := vetoed(hooks.Veto{Plugin: "a", Reason: "no"})
			o.Results = append(o.Results, shutdownLeg("b"))
			return o
		}(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newClearRig(t, answer(tc.out, tc.err))
			wantPortCode(t, r.w.Clear(ctxBG, callerCLI, "atlas.state", false), protocol.CodeUnavailable)
			r.untouched(t)
		})
	}
	t.Run("a cancelled leg with another reason does not block", func(t *testing.T) {
		leg := shutdownLeg("b")
		leg.Err.Data = map[string]any{"reason": "plugin_exited"}
		r := newClearRig(t, answer(allowed(leg), nil))
		if err := r.w.Clear(ctxBG, callerCLI, "atlas.state", false); err != nil {
			t.Fatal(err)
		}
		r.cleared(t)
	})
}

func TestClearContextEndingMidHookIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(ctxBG)
	r := newClearRig(t, func(ctx context.Context, _ string, _ map[string]any) (hooks.Outcome, error) {
		cancel()
		<-ctx.Done()
		return hooks.Outcome{}, &hooks.HookError{Code: protocol.CodeCancelled, Message: "ended", Cause: ctx.Err()}
	})
	wantPortCode(t, r.w.Clear(ctx, callerCLI, "atlas.state", false), protocol.CodeCancelled)
	r.untouched(t)
}

func TestClearContextEndedByShutdownIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancelCause(ctxBG)
	r := newClearRig(t, func(ctx context.Context, _ string, _ map[string]any) (hooks.Outcome, error) {
		cancel(contracts.ErrShutdown)
		<-ctx.Done()
		return hooks.Outcome{}, &hooks.HookError{Code: protocol.CodeCancelled, Message: "ended", Cause: ctx.Err()}
	})
	wantPortCode(t, r.w.Clear(ctx, callerCLI, "atlas.state", false), protocol.CodeUnavailable)
	r.untouched(t)
}

func TestClearWithoutAFirerOrForAnUnknownSlice(t *testing.T) {
	t.Run("no firer wired", func(t *testing.T) {
		cl := &cleaner{}
		w, _, _ := newPort(t, StatePortOptions{Cleaner: cl}, specFresh)
		if _, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"dirty":1}`)); err != nil {
			t.Fatal(err)
		}
		if err := w.Clear(ctxBG, callerCLI, "atlas.state", false); err != nil {
			t.Fatal(err)
		}
		if got := cl.deletedNS(); !reflect.DeepEqual(got, []string{"atlas.state"}) {
			t.Fatalf("deleted = %v", got)
		}
		res, err := w.Get(ctxBG, callerCLI, "atlas.state", "")
		if err != nil || string(res.Value) != `{"fresh":true}` {
			t.Fatalf("reset = %s, %v", res.Value, err)
		}
	})
	t.Run("an unregistered slice asks nobody and is not_found", func(t *testing.T) {
		r := newClearRig(t, answer(allowed(), nil))
		wantPortCode(t, r.w.Clear(ctxBG, callerCLI, "nope.state", false), protocol.CodeNotFound)
		if len(r.f.hooks) != 0 {
			t.Fatalf("firer called for an unregistered slice: %v", r.f.hooks)
		}
		for _, s := range r.ord.got() {
			if strings.HasPrefix(s, "delete") {
				t.Fatalf("persisted copies deleted for an unregistered slice: %v", r.ord.got())
			}
		}
	})
	t.Run("denied callers never reach the hook", func(t *testing.T) {
		r := newClearRig(t, answer(allowed(), nil))
		wantPortCode(t, r.w.Clear(ctxBG, callerAtlas, "atlas.state", false), protocol.CodeDenied)
		if len(r.f.hooks) != 0 {
			t.Fatalf("firer called for a denied caller: %v", r.f.hooks)
		}
	})
}
