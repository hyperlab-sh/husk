package stem_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	stemports "github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// --- fixture ---------------------------------------------------------------

type hookClock struct{ now time.Time }

func (c *hookClock) Now() time.Time { return c.now }

func (c *hookClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type hookSend struct {
	plugin calls.PluginID
	msg    protocol.Message
}

type hookOutbox struct{ sent chan hookSend }

func (b *hookOutbox) Send(p calls.PluginID, m protocol.Message) error {
	b.sent <- hookSend{p, m}
	return nil
}

type hookSubs map[string][]string

func (s hookSubs) Subscribers(hook string) ([]string, bool) {
	list, ok := s[hook]
	return list, ok
}

type hookLive map[string]bool

func (l hookLive) Live(pluginID string) bool { return l[pluginID] }

// hookRig wires the port over a real runner on a real call table with fake
// transport views and a post that runs on the calling goroutine.
type hookRig struct {
	port    *stemports.Hooks
	runner  *hooks.Runner
	table   *calls.Table
	clock   *hookClock
	sent    chan hookSend
	started chan struct{}
	post    func(fn func()) bool
}

func newHookRig(t *testing.T, subs hookSubs, live hookLive) *hookRig {
	t.Helper()
	r := &hookRig{
		clock:   &hookClock{now: time.Unix(1000, 0)},
		sent:    make(chan hookSend, 16),
		started: make(chan struct{}, 16),
		table:   nil,
	}
	box := &hookOutbox{sent: r.sent}
	r.table = calls.New(box, calls.TableOptions{Clock: r.clock})
	r.runner = hooks.New(hooks.Options{Subscriptions: subs, Liveness: live, Calls: r.table})
	// started fires once the posted fn has fully returned, giving the test a
	// happens-before edge over the fan-out before it drives the table.
	r.post = func(fn func()) bool {
		fn()
		r.started <- struct{}{}
		return true
	}
	r.port = stemports.NewHooks(r.runner, r.post)
	return r
}

// awaitStarted waits for the last posted fn to finish.
func (r *hookRig) awaitStarted() { <-r.started }

type hookOutcome struct {
	results []contracts.HookResult
	err     error
}

// hookInvoke runs HookCall on its own goroutine, as the server would.
func hookInvoke(ctx context.Context, p *stemports.Hooks, hook string, data json.RawMessage) chan hookOutcome {
	out := make(chan hookOutcome, 1)
	go func() {
		results, err := p.HookCall(ctx, contracts.Caller{Kind: contracts.CallerCLI}, hook, data)
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
	r := newHookRig(t, hookSubs{"notes.before-refresh": {"b", "c"}}, hookLive{"b": true, "c": true})
	out := hookInvoke(context.Background(), r.port, "notes.before-refresh", json.RawMessage(`{"reason":"manual"}`))
	r.awaitStarted()
	bID := hookWaitFor(t, r, "b")
	cID := hookWaitFor(t, r, "c")
	// b answers with a payload; c stays silent until its deadline.
	if res, ok := r.table.Resolve("b", protocol.Message{ID: bID, Result: json.RawMessage(`{"data":{"ready":true}}`)}); !ok {
		t.Fatal("b's reply refused")
	} else if n := r.runner.Complete(res); n != 1 {
		t.Fatalf("b resolved %d legs", n)
	}
	r.clock.advance(calls.DefaultTimeout + time.Nanosecond)
	if n := r.runner.Complete(r.table.Sweep()...); n != 1 {
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
		if first.Plugin != "b" || first.Err != nil || string(first.Data) != `{"ready":true}` {
			t.Fatalf("first result %#v", first)
		}
		if second.Plugin != "c" || second.Err == nil || second.Err.Code() != "timeout" {
			t.Fatalf("second result %#v", second.Err)
		}
		// The entries marshal with the wire's plugin_id tag.
		blob, err := json.Marshal(first)
		if err != nil || string(blob) != `{"plugin_id":"b","data":{"ready":true}}` {
			t.Fatalf("entry JSON %s (%v)", blob, err)
		}
		_ = cID
	case <-time.After(5 * time.Second):
		t.Fatal("HookCall never returned")
	}
	if n := r.runner.Pending(); n != 0 {
		t.Fatalf("%d legs leaked through the port", n)
	}
}

func TestHookCallWithoutSubscribersReturnsEmptyReply(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, hookLive{})
	results, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "a.h", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("HookCall: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("want a non-nil empty reply, got %#v", results)
	}
}

func TestHookCallUnknownHookIsNotFound(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, hookLive{})
	_, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "nope.h", nil)
	if code := hookCodeOf(t, err); code != "not_found" {
		t.Fatalf("want not_found, got %s", code)
	}
}

func TestHookCallWhenTheLoopIsDown(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, hookLive{"b": true})
	r.port = stemports.NewHooks(r.runner, func(fn func()) bool { return false })
	_, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "a.h", nil)
	if code := hookCodeOf(t, err); code != "unavailable" {
		t.Fatalf("want unavailable, got %s", code)
	}
	if n := r.runner.Pending(); n != 0 {
		t.Fatalf("refused call left %d legs", n)
	}
}

func TestHookCallValidatesData(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, hookLive{})
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
		_, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "a.h", tc.data)
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
	r := newHookRig(t, hookSubs{"a.h": nil}, hookLive{})
	if _, err := r.port.HookCall(context.Background(), contracts.Caller{}, "a.h", nil); err == nil {
		t.Fatal("empty caller accepted")
	}
	if _, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerPlugin}, "a.h", nil); err == nil {
		t.Fatal("plugin caller without id accepted")
	}
	if n := r.runner.Pending(); n != 0 {
		t.Fatalf("refused calls left %d legs", n)
	}
}

func TestHookCallCallerGoneDropsContinuationButConsumesReplies(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, hookLive{"b": true})
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
	if n := r.runner.Pending(); n != 1 {
		t.Fatalf("leg vanished before its reply: %d pending", n)
	}
	// The loop still consumes the leg: the aggregate is discarded, not lost.
	res, ok := r.table.Resolve("b", protocol.Message{ID: bID, Result: json.RawMessage(`{"data":{"late":true}}`)})
	if !ok {
		t.Fatal("b's late reply refused")
	}
	if n := r.runner.Complete(res); n != 1 {
		t.Fatalf("late reply resolved %d legs", n)
	}
	if n := r.runner.Pending(); n != 0 {
		t.Fatalf("dropped continuation leaked %d legs", n)
	}
}

func TestHookCallCallerDeadlineIsTimeout(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, hookLive{"b": true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := r.port.HookCall(ctx, contracts.Caller{Kind: contracts.CallerCLI}, "a.h", nil)
	if code := hookCodeOf(t, err); code != "timeout" {
		t.Fatalf("want timeout, got %s (%v)", code, err)
	}
	// Abandoned, not leaked: sweep the leg once its deadline passes.
	r.clock.advance(calls.DefaultTimeout + time.Nanosecond)
	r.runner.Complete(r.table.Sweep()...)
	if n := r.runner.Pending(); n != 0 {
		t.Fatalf("%d legs leaked after caller deadline", n)
	}
}

// --- caller mapping --------------------------------------------------------

func TestHookCallerMapping(t *testing.T) {
	// CallerStem is a Host caller for the runner, as the Calls port maps it.
	r := newHookRig(t, hookSubs{"a.h": nil}, hookLive{})
	results, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerStem}, "a.h", nil)
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
