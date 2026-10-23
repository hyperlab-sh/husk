package stem_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	stemports "github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// --- fixture ---------------------------------------------------------------

// hookSend records one leg on the wire: which plugin, which message.
type hookSend struct {
	plugin calls.PluginID
	msg    protocol.Message
}

// hookOutbox is the transport the rig's call table sends through.
type hookOutbox struct{ sent chan hookSend }

func (b *hookOutbox) Send(p calls.PluginID, m protocol.Message) error {
	b.sent <- hookSend{p, m}
	return nil
}

// hookSubs maps a declared hook id to its subscribers. Each hook is a gate
// owned by the first segment of its id, with the Calls default timeout.
type hookSubs map[string][]string

func (s hookSubs) Lookup(hook string) (hooks.HookDef, bool) {
	if _, ok := s[hook]; !ok {
		return hooks.HookDef{}, false
	}
	return hooks.HookDef{ID: hook, Owner: strings.SplitN(hook, ".", 2)[0], Kind: hooks.Gate, Timeout: calls.DefaultTimeout}, true
}

func (s hookSubs) Subscribers(hook string) []hooks.Subscription {
	var out []hooks.Subscription
	for _, p := range s[hook] {
		out = append(out, hooks.Subscription{Plugin: p, Priority: 10})
	}
	return out
}

func (s hookSubs) List() []hooks.HookDef { return nil }

// ownerOf is the caller that owns hook under hookSubs' rule.
func ownerOf(hook string) contracts.Caller {
	return contracts.Caller{Kind: contracts.CallerPlugin, PluginID: strings.SplitN(hook, ".", 2)[0]}
}

// hookRig wires the port over a real runner on a real call table, driven by
// the same loop helper the Calls port tests use: every runner and table
// touch runs on the loop goroutine, exactly as the host would touch them.
type hookRig struct {
	loop   *loop
	port   *stemports.Hooks
	runner *hooks.Runner
	table  *calls.Table
	clk    *clock
	sent   chan hookSend
}

func newHookRig(t *testing.T, subs hookSubs, alive live) *hookRig {
	t.Helper()
	r := &hookRig{
		loop: newLoop(t),
		clk:  &clock{t: time.Unix(1000, 0)},
		sent: make(chan hookSend, 16),
	}
	r.table = calls.New(&hookOutbox{sent: r.sent}, calls.TableOptions{Clock: r.clk})
	runner, err := hooks.New(hooks.Options{Catalog: subs, Liveness: alive, Calls: r.table})
	if err != nil {
		t.Fatal(err)
	}
	r.runner = runner
	r.port = stemports.NewHooks(r.runner, r.loop.post)
	return r
}

// resolve replies for plugin's leg, on the loop, and completes the runner.
func (r *hookRig) resolve(t *testing.T, plugin calls.PluginID, id *protocol.ID, result json.RawMessage) {
	t.Helper()
	var n int
	r.loop.do(func() {
		if res, ok := r.table.Resolve(plugin, protocol.Message{ID: id, Result: result}); ok {
			n = r.runner.Complete(res)
		}
	})
	if n != 1 {
		t.Fatalf("%s's reply resolved %d legs", plugin, n)
	}
}

// sweep advances the clock past every deadline — on the loop, so every
// earlier fan-out has finished reading it — and completes what expired.
func (r *hookRig) sweep() int {
	var n int
	r.loop.do(func() {
		r.clk.t = r.clk.t.Add(calls.DefaultTimeout + time.Nanosecond)
		n = r.runner.Complete(r.table.Sweep()...)
	})
	return n
}

// pending reports the runner's pending legs, read on the loop.
func (r *hookRig) pending() int {
	var n int
	r.loop.do(func() { n = r.runner.Pending() })
	return n
}

type hookOutcome struct {
	results []contracts.HookResult
	err     error
}

// hookInvoke runs HookCall on its own goroutine, as the server would.
func hookInvoke(ctx context.Context, p *stemports.Hooks, hook string, data json.RawMessage) chan hookOutcome {
	out := make(chan hookOutcome, 1)
	go func() {
		results, err := p.HookCall(ctx, ownerOf(hook), hook, data)
		out <- hookOutcome{results: results, err: err}
	}()
	return out
}

// hookWaitFor blocks until the plugin's leg is on the wire.
func hookWaitFor(t *testing.T, r *hookRig, plugin calls.PluginID) *protocol.ID {
	t.Helper()
	for {
		select {
		case s := <-r.sent:
			if s.plugin == plugin {
				return s.msg.ID
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no send to %s", plugin)
		}
	}
}

func hookCodeOf(t *testing.T, err error) string {
	t.Helper()
	var coded contracts.CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("uncoded error %v", err)
	}
	return coded.Code()
}

// --- HookCall --------------------------------------------------------------

func TestHookCallAggregatesThroughThePort(t *testing.T) {
	r := newHookRig(t, hookSubs{"notes.before-refresh": {"b", "c"}}, live{"b": true, "c": true})
	out := hookInvoke(context.Background(), r.port, "notes.before-refresh", json.RawMessage(`{"reason":"manual"}`))
	bID := hookWaitFor(t, r, "b")
	cID := hookWaitFor(t, r, "c")
	if bID.String() == cID.String() {
		t.Fatal("legs share one request id")
	}
	// b allows; c stays silent until its deadline.
	r.resolve(t, "b", bID, json.RawMessage(`{}`))
	if n := r.sweep(); n != 1 {
		t.Fatalf("expiry resolved %d legs", n)
	}
	select {
	case got := <-out:
		if got.err != nil {
			t.Fatalf("HookCall: %v", got.err)
		}
		if len(got.results) != 2 {
			t.Fatalf("want 2 results, got %d", len(got.results))
		}
		first, second := got.results[0], got.results[1]
		if first.Plugin != "b" || first.Err != nil {
			t.Fatalf("first result %#v", first)
		}
		if second.Plugin != "c" || second.Err == nil || second.Err.Code() != "timeout" {
			t.Fatalf("second result %#v", second.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HookCall never returned")
	}
	if n := r.pending(); n != 0 {
		t.Fatalf("%d legs leaked through the port", n)
	}
}

func TestHookCallWithoutSubscribersReturnsEmptyReply(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	results, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("HookCall: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("want a non-nil empty reply, got %#v", results)
	}
}

func TestHookCallUnknownHookIsNotFoundForTheHostAndDeniedForOthers(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	ctx := context.Background()
	_, err := r.port.HookCall(ctx, contracts.Caller{Kind: contracts.CallerStem}, "husk.nope", nil)
	if code := hookCodeOf(t, err); code != "not_found" {
		t.Fatalf("host: want not_found, got %s", code)
	}
	_, err = r.port.HookCall(ctx, ownerOf("nope.h"), "nope.h", nil)
	if code := hookCodeOf(t, err); code != "denied" {
		t.Fatalf("plugin: want denied, got %s", code)
	}
	_, err = r.port.HookCall(ctx, contracts.Caller{Kind: contracts.CallerCLI}, "a.h", nil)
	if code := hookCodeOf(t, err); code != "denied" {
		t.Fatalf("cli: want denied, got %s", code)
	}
}

func TestHookCallWhenTheLoopIsDown(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, live{"b": true})
	r.port = stemports.NewHooks(r.runner, func(fn func()) bool { return false })
	_, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", nil)
	if code := hookCodeOf(t, err); code != "unavailable" {
		t.Fatalf("want unavailable, got %s", code)
	}
	if n := r.pending(); n != 0 {
		t.Fatalf("refused call left %d legs", n)
	}
}

func TestHookCallValidatesData(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	for _, tc := range []struct {
		name string
		data json.RawMessage
		code string // "" means accepted
	}{
		{"array", json.RawMessage(`[1]`), "invalid_params"},
		{"string", json.RawMessage(`"x"`), "invalid_params"},
		{"number", json.RawMessage(`5`), "invalid_params"},
		{"trailing", json.RawMessage(`{"x":1}{"y":2}`), "invalid_params"},
		{"absent", nil, ""},
		{"empty", json.RawMessage(``), ""},
		{"null", json.RawMessage(`null`), ""},
		{"object", json.RawMessage(`{"why":"manual"}`), ""},
	} {
		_, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", tc.data)
		if tc.code == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if code := hookCodeOf(t, err); code != tc.code {
			t.Fatalf("%s: want %s, got %s", tc.name, tc.code, code)
		}
	}
}

func TestHookCallRefusesUnknownCallers(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	if _, err := r.port.HookCall(context.Background(), contracts.Caller{}, "a.h", nil); err == nil {
		t.Fatal("empty caller accepted")
	}
	if _, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerPlugin}, "a.h", nil); err == nil {
		t.Fatal("plugin caller without id accepted")
	}
	if n := r.pending(); n != 0 {
		t.Fatalf("refused calls left %d legs", n)
	}
}

func TestHookCallCallerGoneDropsContinuationButConsumesReplies(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, live{"b": true})
	ctx, cancel := context.WithCancel(context.Background())
	out := hookInvoke(ctx, r.port, "a.h", nil)
	bID := hookWaitFor(t, r, "b")
	cancel()
	select {
	case got := <-out:
		if code := hookCodeOf(t, got.err); code != "cancelled" {
			t.Fatalf("want cancelled, got %s (%v)", code, got.err)
		}
		if got.results != nil {
			t.Fatalf("dropped continuation returned results: %#v", got.results)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HookCall never returned")
	}
	if n := r.pending(); n != 1 {
		t.Fatalf("leg vanished before its reply: %d pending", n)
	}
	// The loop still consumes the leg: the aggregate is discarded, not lost.
	r.resolve(t, "b", bID, json.RawMessage(`{"data":{"late":true}}`))
	if n := r.pending(); n != 0 {
		t.Fatalf("dropped continuation leaked %d legs", n)
	}
}

func TestHookCallCallerDeadlineIsTimeout(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, live{"b": true})
	// A deadline already in the past expires the context at once: the branch
	// is exercised without a real timer.
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, err := r.port.HookCall(ctx, ownerOf("a.h"), "a.h", nil)
	if code := hookCodeOf(t, err); code != "timeout" {
		t.Fatalf("want timeout, got %s (%v)", code, err)
	}
	// Abandoned, not leaked: sweep the leg once its deadline passes.
	if n := r.sweep(); n != 1 {
		t.Fatalf("sweep resolved %d legs", n)
	}
	if n := r.pending(); n != 0 {
		t.Fatalf("%d legs leaked after caller deadline", n)
	}
}

// --- caller mapping --------------------------------------------------------

func TestHookCallerMapping(t *testing.T) {
	// CallerStem is a Host caller for the runner, as the Calls port maps it.
	r := newHookRig(t, hookSubs{"husk.h": nil}, live{})
	results, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerStem}, "husk.h", nil)
	if err != nil || len(results) != 0 {
		t.Fatalf("stem caller refused: %v (%d results)", err, len(results))
	}
}

func TestNewHooksRequiresRunnerAndPost(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHooks accepted a nil argument")
		}
	}()
	stemports.NewHooks(nil, nil)
}
