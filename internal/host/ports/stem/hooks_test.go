package stem_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
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

// kinded is hookSubs with a declared kind per hook (gate when unlisted),
// a title and description on every hook, and a List that reports them all.
type kinded struct {
	hookSubs
	kinds map[string]hooks.Kind
}

func (k kinded) Lookup(hook string) (hooks.HookDef, bool) {
	def, ok := k.hookSubs.Lookup(hook)
	if kind, set := k.kinds[hook]; ok && set {
		def.Kind = kind
	}
	def.Title, def.Description = "Title "+hook, "About "+hook
	return def, ok
}

func (k kinded) List() []hooks.HookDef {
	var ids []string
	for id := range k.hookSubs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []hooks.HookDef
	for _, id := range ids {
		def, _ := k.Lookup(id)
		out = append(out, def)
	}
	return out
}

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

func newHookRig(t *testing.T, subs hooks.Catalog, alive live, mut ...func(*hooks.Options)) *hookRig {
	t.Helper()
	r := &hookRig{
		loop: newLoop(t),
		clk:  &clock{t: time.Unix(1000, 0)},
		sent: make(chan hookSend, 16),
	}
	r.table = calls.New(&hookOutbox{sent: r.sent}, calls.TableOptions{Clock: r.clk})
	opts := hooks.Options{Catalog: subs, Liveness: alive, Calls: r.table}
	for _, m := range mut {
		m(&opts)
	}
	runner, err := hooks.New(opts)
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

// resolveError fails plugin's leg with e, on the loop, and completes the runner.
func (r *hookRig) resolveError(t *testing.T, plugin calls.PluginID, id *protocol.ID, e *protocol.Error) {
	t.Helper()
	var n int
	r.loop.do(func() {
		if res, ok := r.table.Resolve(plugin, protocol.Message{ID: id, Error: e}); ok {
			n = r.runner.Complete(res)
		}
	})
	if n != 1 {
		t.Fatalf("%s's error resolved %d legs", plugin, n)
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
	outcome contracts.HookOutcome
	err     error
}

// hookInvoke runs HookCall on its own goroutine, as the server would.
func hookInvoke(ctx context.Context, p *stemports.Hooks, hook string, data json.RawMessage) chan hookOutcome {
	out := make(chan hookOutcome, 1)
	go func() {
		outcome, err := p.HookCall(ctx, ownerOf(hook), hook, data)
		out <- hookOutcome{outcome: outcome, err: err}
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
		if got.outcome.Kind != "gate" || !got.outcome.Allowed || len(got.outcome.Vetoes) != 0 {
			t.Fatalf("outcome %#v", got.outcome)
		}
		if len(got.outcome.Results) != 2 {
			t.Fatalf("want 2 results, got %d", len(got.outcome.Results))
		}
		first, second := got.outcome.Results[0], got.outcome.Results[1]
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
	o, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("HookCall: %v", err)
	}
	if o.Kind != "gate" || !o.Allowed || o.Results == nil || len(o.Results) != 0 {
		t.Fatalf("want an allowed empty gate outcome, got %#v", o)
	}
}

func TestHookCallUnknownHookIsNotFoundForTheHostAndDeniedForOthers(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	ctx := context.Background()
	_, err := r.port.HookCall(ctx, contracts.Caller{Kind: contracts.CallerHost}, "husk.nope", nil)
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
		if got.outcome.Results != nil {
			t.Fatalf("dropped continuation returned results: %#v", got.outcome.Results)
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
	// CallerHost is a Host caller for the runner, as the Calls port maps it.
	r := newHookRig(t, hookSubs{"husk.h": nil}, live{})
	o, err := r.port.HookCall(context.Background(), contracts.Caller{Kind: contracts.CallerHost}, "husk.h", nil)
	if err != nil || len(o.Results) != 0 {
		t.Fatalf("stem caller refused: %v (%d results)", err, len(o.Results))
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

// --- outcome mapping per kind ---------------------------------------------

func waitOutcome(t *testing.T, out chan hookOutcome) contracts.HookOutcome {
	t.Helper()
	select {
	case got := <-out:
		if got.err != nil {
			t.Fatalf("HookCall: %v", got.err)
		}
		return got.outcome
	case <-time.After(5 * time.Second):
		t.Fatal("HookCall never returned")
	}
	return contracts.HookOutcome{}
}

func TestHookCallGateVetoCarriesVetoReasonAndVetoes(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b", "c"}}, live{"b": true, "c": true})
	out := hookInvoke(context.Background(), r.port, "a.h", nil)
	bID, cID := hookWaitFor(t, r, "b"), hookWaitFor(t, r, "c")
	r.resolve(t, "b", bID, json.RawMessage(`{"veto":true,"reason":"index write in progress"}`))
	r.resolve(t, "c", cID, json.RawMessage(`{}`))
	o := waitOutcome(t, out)
	if o.Kind != "gate" || o.Allowed {
		t.Fatalf("outcome %#v", o)
	}
	if len(o.Vetoes) != 1 || o.Vetoes[0] != (contracts.HookVeto{Plugin: "b", Reason: "index write in progress"}) {
		t.Fatalf("vetoes %#v", o.Vetoes)
	}
	if b := o.Results[0]; !b.Veto || b.Reason != "index write in progress" || b.Err != nil {
		t.Fatalf("vetoing entry %#v", b)
	}
	if c := o.Results[1]; c.Veto || c.Reason != "" {
		t.Fatalf("allowing entry %#v", c)
	}
}

func TestHookCallCollectCarriesItems(t *testing.T) {
	subs := kinded{hookSubs{"a.h": {"b", "c"}}, map[string]hooks.Kind{"a.h": hooks.Collect}}
	r := newHookRig(t, subs, live{"b": true, "c": true})
	out := hookInvoke(context.Background(), r.port, "a.h", nil)
	bID, cID := hookWaitFor(t, r, "b"), hookWaitFor(t, r, "c")
	r.resolve(t, "b", bID, json.RawMessage(`{"items":[{"label":"Preview"}]}`))
	r.resolve(t, "c", cID, json.RawMessage(`{}`))
	o := waitOutcome(t, out)
	if o.Kind != "collect" || o.Data != nil || !o.Allowed || len(o.Vetoes) != 0 {
		t.Fatalf("outcome %#v", o)
	}
	if string(o.Results[0].Items) != `[{"label":"Preview"}]` {
		t.Fatalf("items %s", o.Results[0].Items)
	}
	if o.Results[1].Items != nil {
		t.Fatalf("empty contributor carries items %s", o.Results[1].Items)
	}
}

func TestHookCallLegErrorKeepsCodeMessageAndData(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": {"b"}}, live{"b": true})
	out := hookInvoke(context.Background(), r.port, "a.h", nil)
	bID := hookWaitFor(t, r, "b")
	r.resolveError(t, "b", bID, &protocol.Error{Code: protocol.CodeFailed, Message: "index unreadable", Data: json.RawMessage(`{"file":"x"}`)})
	o := waitOutcome(t, out)
	err := o.Results[0].Err
	if err == nil || err.Code() != "failed" || err.Error() != "index unreadable" {
		t.Fatalf("entry error %v", err)
	}
	var withData contracts.DataError
	if !errors.As(err, &withData) || withData.Data()["file"] != "x" {
		t.Fatalf("entry error lost its data: %#v", err)
	}
}

// --- refusals --------------------------------------------------------------

func TestHookCallRefusalCodesCrossTheBoundary(t *testing.T) {
	t.Run("busy carries its reason", func(t *testing.T) {
		r := newHookRig(t, hookSubs{"a.h": {"b"}}, live{"b": true}, func(o *hooks.Options) { o.MaxInFlight = 1 })
		first := hookInvoke(context.Background(), r.port, "a.h", nil)
		hookWaitFor(t, r, "b")
		_, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", nil)
		if code := hookCodeOf(t, err); code != "busy" {
			t.Fatalf("want busy, got %s", code)
		}
		var withData contracts.DataError
		if !errors.As(err, &withData) || withData.Data()["reason"] != hooks.ReasonInFlight {
			t.Fatalf("busy lost its reason: %#v", err)
		}
		r.sweep()
		<-first
	})
	t.Run("denied, not_found and invalid_params", func(t *testing.T) {
		r := newHookRig(t, hookSubs{"a.h": nil}, live{})
		ctx := context.Background()
		for _, tc := range []struct {
			name   string
			caller contracts.Caller
			hook   string
			code   string
		}{
			{"foreign plugin", contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "other"}, "a.h", "denied"},
			{"host hook unknown", contracts.Caller{Kind: contracts.CallerStem}, "husk.nope", "not_found"},
			{"malformed id", ownerOf("a.h"), "a..h", "invalid_params"},
		} {
			_, err := r.port.HookCall(ctx, tc.caller, tc.hook, nil)
			if code := hookCodeOf(t, err); code != tc.code {
				t.Fatalf("%s: want %s, got %s", tc.name, tc.code, code)
			}
		}
	})
}

func TestHookCallDataSizeLimit(t *testing.T) {
	// object wraps a string value so the compact encoding is exactly n bytes.
	object := func(n int) json.RawMessage {
		const wrap = len(`{"k":""}`)
		return json.RawMessage(`{"k":"` + strings.Repeat("a", n-wrap) + `"}`)
	}
	t.Run("over 1 MiB is invalid_params and posts nothing", func(t *testing.T) {
		posted := 0
		r := newHookRig(t, hookSubs{"a.h": nil}, live{})
		port := stemports.NewHooks(r.runner, func(fn func()) bool { posted++; return false })
		_, err := port.HookCall(context.Background(), ownerOf("a.h"), "a.h", object(hooks.MaxPayload+1))
		if code := hookCodeOf(t, err); code != "invalid_params" {
			t.Fatalf("want invalid_params, got %s", code)
		}
		if posted != 0 {
			t.Fatalf("oversized data posted %d times", posted)
		}
	})
	t.Run("exactly 1 MiB passes", func(t *testing.T) {
		r := newHookRig(t, hookSubs{"a.h": nil}, live{})
		if _, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", object(hooks.MaxPayload)); err != nil {
			t.Fatalf("limit-sized data refused: %v", err)
		}
	})
	t.Run("measured compact, not raw", func(t *testing.T) {
		r := newHookRig(t, hookSubs{"a.h": nil}, live{})
		raw := append(object(hooks.MaxPayload), bytes.Repeat([]byte(" "), 4096)...)
		if _, err := r.port.HookCall(context.Background(), ownerOf("a.h"), "a.h", raw); err != nil {
			t.Fatalf("whitespace counted against the limit: %v", err)
		}
	})
}

// --- HookList --------------------------------------------------------------

func TestHookListReturnsDeclaredHooksWithLiveness(t *testing.T) {
	subs := kinded{hookSubs{"a.h": {"b", "c"}, "x.g": nil}, map[string]hooks.Kind{"a.h": hooks.Transform}}
	r := newHookRig(t, subs, live{"b": true})
	got, err := r.port.HookList(context.Background(), contracts.Caller{Kind: contracts.CallerCLI})
	if err != nil {
		t.Fatal(err)
	}
	want := []contracts.HookInfo{
		{ID: "a.h", Owner: "a", Kind: "transform", TimeoutMS: calls.DefaultTimeout.Milliseconds(), Title: "Title a.h", Description: "About a.h",
			Subscribers: []contracts.HookSubscriber{{Plugin: "b", Priority: 10, Live: true}, {Plugin: "c", Priority: 10}}},
		{ID: "x.g", Owner: "x", Kind: "gate", TimeoutMS: calls.DefaultTimeout.Milliseconds(), Title: "Title x.g", Description: "About x.g",
			Subscribers: []contracts.HookSubscriber{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HookList = %#v\nwant %#v", got, want)
	}
}

func TestHookListCancelledWhilePostIsPending(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	// post queues fn without running it, so only ctx can end the wait.
	port := stemports.NewHooks(r.runner, func(fn func()) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := port.HookList(ctx, contracts.Caller{Kind: contracts.CallerCLI})
	if code := hookCodeOf(t, err); code != "cancelled" {
		t.Fatalf("want cancelled, got %s (%v)", code, err)
	}
}

func TestHookListWhenTheLoopIsDown(t *testing.T) {
	r := newHookRig(t, hookSubs{"a.h": nil}, live{})
	port := stemports.NewHooks(r.runner, func(fn func()) bool { return false })
	_, err := port.HookList(context.Background(), contracts.Caller{Kind: contracts.CallerCLI})
	if code := hookCodeOf(t, err); code != "unavailable" {
		t.Fatalf("want unavailable, got %s", code)
	}
}
