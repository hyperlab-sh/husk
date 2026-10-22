package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// --- fakes -----------------------------------------------------------------

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type sentMsg struct {
	plugin calls.PluginID
	msg    protocol.Message
}

type fakeOutbox struct {
	sent   []sentMsg
	err    error                    // fails every Send when set
	errFor map[calls.PluginID]error // fails Sends to these plugins
	notify chan sentMsg             // optional per-send signal for waiting tests
}

func (b *fakeOutbox) Send(p calls.PluginID, m protocol.Message) error {
	if b.errFor != nil {
		if err, ok := b.errFor[p]; ok {
			return err
		}
	}
	if b.err != nil {
		return b.err
	}
	b.sent = append(b.sent, sentMsg{p, m})
	if b.notify != nil {
		b.notify <- sentMsg{p, m}
	}
	return nil
}

// fakeSubs maps hook id -> subscribers in declaration order. Presence in the
// map distinguishes a declared hook from an unknown one.
type fakeSubs map[string][]string

func (s fakeSubs) Subscribers(hook string) ([]string, bool) { return s[hook], true }

// missingSubs knows no hooks at all.
type missingSubs struct{}

func (missingSubs) Subscribers(string) ([]string, bool) { return nil, false }

type fakeLiveness map[string]bool

func (l fakeLiveness) Live(pluginID string) bool { return l[pluginID] }

func newRunner(t *testing.T, subs Subscriptions, live fakeLiveness, opts calls.TableOptions) (*Runner, *calls.Table, *fakeOutbox, *fakeClock) {
	t.Helper()
	box := &fakeOutbox{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	opts.Clock = clock
	table := calls.New(box, opts)
	r, err := New(Options{Subscriptions: subs, Liveness: live, Calls: table})
	if err != nil {
		t.Fatal(err)
	}
	return r, table, box, clock
}

func mustCall(t *testing.T, r *Runner, hook string, data map[string]any, onDone func([]HookResult)) {
	t.Helper()
	if err := r.Call(Caller{Kind: CallerCLI}, hook, data, onDone); err != nil {
		t.Fatalf("Call(%q): %v", hook, err)
	}
}

// wantCallError asserts Call refuses with the given code.
func wantCallError(t *testing.T, err error, code string) {
	t.Helper()
	var he *HookError
	if !errors.As(err, &he) || he.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// collector records aggregates as they are delivered.
type collector struct {
	calls  int
	result []HookResult
}

func (c *collector) onDone(rs []HookResult) { c.calls++; c.result = rs }

func reply(id *protocol.ID, result json.RawMessage) protocol.Message {
	return protocol.Message{ID: id, Result: result}
}

func errorReply(id *protocol.ID, e *protocol.Error) protocol.Message {
	return protocol.Message{ID: id, Error: e}
}

// complete drives one table result through the runner.
func complete(t *testing.T, r *Runner, res calls.Result, ok bool) {
	t.Helper()
	if !ok {
		t.Fatal("table refused the reply")
	}
	if n := r.Complete(res); n != 1 {
		t.Fatalf("resolved %d legs, want 1", n)
	}
}

// --- New -------------------------------------------------------------------

func TestNewPanicsOnMissingViews(t *testing.T) {
	opts := Options{Subscriptions: fakeSubs{}, Liveness: fakeLiveness{}, Calls: calls.New(nil, calls.TableOptions{})}
	for _, tc := range []struct {
		name string
		mut  func(Options) Options
	}{
		{"no subscriptions", func(o Options) Options { o.Subscriptions = nil; return o }},
		{"no liveness", func(o Options) Options { o.Liveness = nil; return o }},
		{"no calls", func(o Options) Options { o.Calls = nil; return o }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: New did not panic", tc.name)
				}
			}()
			New(tc.mut(opts))
		}()
	}
}

// --- Call ------------------------------------------------------------------

func TestNoSubscribersAnswersAtOnceWithEmptySlice(t *testing.T) {
	r, table, box, _ := newRunner(t, fakeSubs{"a.h": nil}, fakeLiveness{}, calls.TableOptions{})
	c := &collector{}
	mustCall(t, r, "a.h", map[string]any{"x": 1}, c.onDone)
	if c.calls != 1 || len(c.result) != 0 || c.result == nil {
		t.Fatalf("want one call with a non-nil empty slice, got %d calls, %#v", c.calls, c.result)
	}
	if len(box.sent) != 0 || r.Pending() != 0 || table.Stats().Pending != 0 {
		t.Fatalf("no leg expected: sent=%d pending=%d table=%d", len(box.sent), r.Pending(), table.Stats().Pending)
	}
}

func TestUnknownHookRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		subs Subscriptions
		hook string
	}{
		{"undeclared hook", missingSubs{}, "a.h"},
		{"empty hook id", fakeSubs{}, ""},
	} {
		r, _, box, _ := newRunner(t, tc.subs, fakeLiveness{}, calls.TableOptions{})
		var logs int
		r.opts.Logger = func(string, ...any) { logs++ }
		c := &collector{}
		err := r.Call(Caller{Kind: CallerCLI}, tc.hook, nil, c.onDone)
		wantCallError(t, err, CodeNotFound)
		if c.calls != 0 || len(box.sent) != 0 || r.Pending() != 0 {
			t.Fatalf("%s: refused call leaked work", tc.name)
		}
		if tc.hook != "" && logs != 1 {
			t.Fatalf("%s: want one refusal log, got %d", tc.name, logs)
		}
	}
}

func TestCallFansOutToLiveSubscribersOnly(t *testing.T) {
	subs := fakeSubs{"a.h": {"b", "dead", "c"}}
	r, table, box, _ := newRunner(t, subs, fakeLiveness{"b": true, "c": true, "dead": false}, calls.TableOptions{})
	c := &collector{}
	if err := r.Call(Caller{Kind: CallerCLI}, "a.h", map[string]any{"x": 1}, c.onDone); err != nil {
		t.Fatalf("fan-out: %v", err)
	}
	if len(box.sent) != 2 {
		t.Fatalf("want 2 legs, got %d", len(box.sent))
	}
	if box.sent[0].plugin != "b" || box.sent[1].plugin != "c" {
		t.Fatalf("want declaration order b,c, got %v,%v", box.sent[0].plugin, box.sent[1].plugin)
	}
	for _, s := range box.sent {
		if s.msg.Method != "hook.call" {
			t.Fatalf("method %q", s.msg.Method)
		}
		if got := string(s.msg.Params); got != `{"hook":"a.h","data":{"x":1}}` {
			t.Fatalf("params %s", got)
		}
	}
	if box.sent[0].msg.ID.String() == box.sent[1].msg.ID.String() {
		t.Fatal("legs share one request id")
	}
	if r.Pending() != 2 {
		t.Fatalf("pending %d", r.Pending())
	}
	// Complete the call: the dead subscriber never had a leg, so the
	// aggregate holds b and c and no entry for it.
	res, ok := table.Resolve("b", reply(box.sent[0].msg.ID, json.RawMessage(`{"data":{"ok":true}}`)))
	complete(t, r, res, ok)
	if c.calls != 0 {
		t.Fatal("aggregate delivered before the last leg")
	}
	res, ok = table.Resolve("c", reply(box.sent[1].msg.ID, json.RawMessage(`{"data":{"ok":true}}`)))
	complete(t, r, res, ok)
	if c.calls != 1 || len(c.result) != 2 {
		t.Fatalf("delivered %d aggregates, first has %d entries", c.calls, len(c.result))
	}
	saw := map[string]bool{}
	for _, e := range c.result {
		saw[e.Plugin] = true
		if e.Err != nil {
			t.Fatalf("entry %#v", e)
		}
	}
	if !saw["b"] || !saw["c"] || saw["dead"] || len(saw) != 2 {
		t.Fatalf("aggregate plugins %v", saw)
	}
	if r.Pending() != 0 {
		t.Fatal("legs leaked")
	}
	// A nil payload omits the data member, as today's wire does.
	mustCall(t, r, "a.h", nil, nil)
	if got := string(box.sent[len(box.sent)-1].msg.Params); got != `{"hook":"a.h"}` {
		t.Fatalf("params %s", got)
	}
}

func TestCapsRefuseWholeCall(t *testing.T) {
	subs := fakeSubs{"h.b": {"b"}, "h.b2": {"b"}, "h.c": {"c"}}
	live := fakeLiveness{"b": true, "c": true}

	// Overall cap: the second call is refused whole, nothing is sent.
	r, _, box, _ := newRunner(t, subs, live, calls.TableOptions{MaxPending: 1})
	mustCall(t, r, "h.b", nil, nil)
	c := &collector{}
	err := r.Call(Caller{Kind: CallerCLI}, "h.c", nil, c.onDone)
	wantCallError(t, err, CodeBusy)
	if len(box.sent) != 1 || c.calls != 0 || r.Pending() != 1 {
		t.Fatalf("refused call leaked work: sent=%d done=%d pending=%d", len(box.sent), c.calls, r.Pending())
	}

	// Per-subscriber cap: b's second hook is refused, c's hook still runs.
	r, _, box, _ = newRunner(t, subs, live, calls.TableOptions{MaxPendingPerPlugin: 1})
	mustCall(t, r, "h.b", nil, nil)
	err = r.Call(Caller{Kind: CallerCLI}, "h.b2", nil, nil)
	var he *HookError
	if !errors.As(err, &he) || he.Code != CodeBusy || he.Message != "too many pending hook calls for one subscriber" {
		t.Fatalf("per-subscriber cap: %v", err)
	}
	mustCall(t, r, "h.c", nil, nil)
	if len(box.sent) != 2 {
		t.Fatalf("full subscriber bricked another: sent=%d", len(box.sent))
	}
}

func TestSendFailureBecomesThatSubscribersEntry(t *testing.T) {
	r, table, box, _ := newRunner(t, fakeSubs{"a.h": {"b", "c"}}, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	box.errFor = map[calls.PluginID]error{"c": errors.New("writer closed")}
	c := &collector{}
	if err := r.Call(Caller{Kind: CallerCLI}, "a.h", map[string]any{"x": 1}, c.onDone); err != nil {
		t.Fatalf("per-subscriber fan-out refused the call: %v", err)
	}
	if len(box.sent) != 1 || r.Pending() != 1 || table.Stats().Pending != 1 {
		t.Fatalf("b's leg missing: sent=%d legs=%d table=%d", len(box.sent), r.Pending(), table.Stats().Pending)
	}
	if c.calls != 0 {
		t.Fatal("aggregate delivered before the last leg")
	}
	// b still answers; the aggregate carries c's send failure and b's
	// payload — the rest of the call proceeded.
	res, ok := table.Resolve("b", reply(box.sent[0].msg.ID, json.RawMessage(`{"data":{"ready":true}}`)))
	complete(t, r, res, ok)
	if c.calls != 1 || len(c.result) != 2 {
		t.Fatalf("delivered %d aggregates, first has %d entries", c.calls, len(c.result))
	}
	first, second := c.result[0], c.result[1]
	if first.Plugin != "c" || first.Err == nil || first.Err.Code != CodeUnavailable || first.Err.Data["reason"] != "disconnected" || first.Data != nil {
		t.Fatalf("failed send's entry %#v", first)
	}
	if second.Plugin != "b" || second.Err != nil || second.Data["ready"] != true {
		t.Fatalf("b's entry %#v", second)
	}
	if r.Pending() != 0 {
		t.Fatal("legs leaked")
	}
}

// errSender fails every Send, exercising the send-failure classification the
// real table cannot be forced into from a valid Runner configuration.
type errSender struct{ err error }

func (s *errSender) Send(calls.PluginID, string, json.RawMessage, calls.Options) (calls.Handle, error) {
	return calls.Handle{}, s.err
}

func (s *errSender) CanSend(...calls.PluginID) error { return nil }

func TestNewRejectsNegativeTimeout(t *testing.T) {
	r, err := New(Options{Subscriptions: fakeSubs{}, Liveness: fakeLiveness{}, Calls: &errSender{}, Timeout: -time.Second})
	if err == nil || r != nil {
		t.Fatalf("New() = %v, %v; want nil runner and an error", r, err)
	}
}

func TestSendFailureReasonOnlyForUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   string
		reason string
	}{
		{"unavailable", fmt.Errorf("send b: %w", calls.ErrUnavailable), CodeUnavailable, "disconnected"},
		{"internal", errors.New("send b: bad options"), CodeInternal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New(Options{Subscriptions: fakeSubs{"a.h": {"b"}}, Liveness: fakeLiveness{"b": true}, Calls: &errSender{err: tc.err}})
			if err != nil {
				t.Fatal(err)
			}
			c := &collector{}
			if err := r.Call(Caller{Kind: CallerCLI}, "a.h", nil, c.onDone); err != nil {
				t.Fatal(err)
			}
			if c.calls != 1 || len(c.result) != 1 {
				t.Fatalf("delivered %d aggregates, first has %d entries", c.calls, len(c.result))
			}
			got := c.result[0]
			if got.Plugin != "b" || got.Err == nil || got.Err.Code != tc.code {
				t.Fatalf("entry %#v", got)
			}
			if tc.reason == "" {
				if got.Err.Data != nil {
					t.Fatalf("data = %v, want none", got.Err.Data)
				}
			} else if got.Err.Data["reason"] != tc.reason {
				t.Fatalf("data = %v, want reason %q", got.Err.Data, tc.reason)
			}
		})
	}
}

func TestAllSendsFailAnswersAtOnceWithFailureEntries(t *testing.T) {
	r, table, box, _ := newRunner(t, fakeSubs{"a.h": {"b", "c"}}, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	box.errFor = map[calls.PluginID]error{"b": errors.New("writer closed"), "c": errors.New("writer closed")}
	c := &collector{}
	if err := r.Call(Caller{Kind: CallerCLI}, "a.h", nil, c.onDone); err != nil {
		t.Fatalf("all-failed fan-out refused the call: %v", err)
	}
	if c.calls != 1 || len(c.result) != 2 {
		t.Fatalf("delivered %d aggregates, first has %d entries", c.calls, len(c.result))
	}
	for _, e := range c.result {
		if e.Err == nil || e.Err.Code != CodeUnavailable || e.Err.Data["reason"] != "disconnected" {
			t.Fatalf("entry %#v", e)
		}
	}
	if r.Pending() != 0 || table.Stats().Pending != 0 || len(box.sent) != 0 {
		t.Fatalf("nothing was delivered but work leaked: legs=%d table=%d sent=%d", r.Pending(), table.Stats().Pending, len(box.sent))
	}
}

// --- Complete --------------------------------------------------------------

func TestAggregateInResolutionOrderWithPayloadAndErrorPassthrough(t *testing.T) {
	r, table, box, _ := newRunner(t, fakeSubs{"a.h": {"b", "c"}}, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	c := &collector{}
	mustCall(t, r, "a.h", nil, c.onDone)
	// c answers first with an error, then b with a payload.
	res, ok := table.Resolve("c", errorReply(box.sent[1].msg.ID, &protocol.Error{Code: protocol.CodeFailed, Message: "no"}))
	complete(t, r, res, ok)
	if c.calls != 0 {
		t.Fatal("aggregate delivered before the last leg")
	}
	res, ok = table.Resolve("b", reply(box.sent[0].msg.ID, json.RawMessage(`{"data":{"ready":true}}`)))
	complete(t, r, res, ok)
	if c.calls != 1 || len(c.result) != 2 {
		t.Fatalf("delivered %d aggregates, first has %d entries", c.calls, len(c.result))
	}
	if got := c.result[0]; got.Plugin != "c" || got.Hook != "a.h" || got.Err == nil || got.Err.Code != "failed" || got.Err.Message != "no" || got.Data != nil {
		t.Fatalf("first entry %#v", got)
	}
	if got := c.result[1]; got.Plugin != "b" || got.Err != nil || got.Data["ready"] != true {
		t.Fatalf("second entry %#v", got)
	}
	if r.Pending() != 0 {
		t.Fatal("legs leaked")
	}
}

func TestTimeoutRecordedOthersStillAggregated(t *testing.T) {
	r, table, box, clock := newRunner(t, fakeSubs{"a.h": {"b", "c"}}, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	c := &collector{}
	mustCall(t, r, "a.h", nil, c.onDone)
	// b answers in time; c stays silent.
	res, ok := table.Resolve("b", reply(box.sent[0].msg.ID, json.RawMessage(`{"data":{"ready":true}}`)))
	complete(t, r, res, ok)
	clock.advance(calls.DefaultTimeout - time.Nanosecond)
	if out := table.Expire(clock.now); len(out) != 0 {
		t.Fatalf("expired before the deadline: %+v", out)
	}
	clock.advance(time.Nanosecond)
	if n := r.Complete(table.Expire(clock.now)...); n != 1 {
		t.Fatalf("expiry resolved %d legs", n)
	}
	if c.calls != 1 || len(c.result) != 2 {
		t.Fatalf("want b's payload and c's timeout, got %d calls, %d entries", c.calls, len(c.result))
	}
	if got := c.result[1]; got.Plugin != "c" || got.Err == nil || got.Err.Code != "timeout" || got.Err.Message == "" {
		t.Fatalf("timeout entry %#v", got)
	}
	if r.Pending() != 0 || table.Stats().Pending != 0 {
		t.Fatalf("expired leg leaked: runner=%d table=%d", r.Pending(), table.Stats().Pending)
	}
	// The late reply is an orphan the runner never sees again.
	if _, ok := table.Resolve("c", reply(box.sent[1].msg.ID, json.RawMessage(`{}`))); ok {
		t.Fatal("late reply accepted after timeout")
	}
	if again := table.Expire(clock.now.Add(time.Hour)); len(again) != 0 {
		t.Fatal("timeout delivered twice")
	}
}

func TestSubscriberExitCancelsWithReason(t *testing.T) {
	r, table, box, _ := newRunner(t, fakeSubs{"a.h": {"b", "c"}}, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	c := &collector{}
	mustCall(t, r, "a.h", nil, c.onDone)
	res, ok := table.Resolve("b", reply(box.sent[0].msg.ID, json.RawMessage(`{"data":{}}`)))
	complete(t, r, res, ok)
	if n := r.Complete(table.DropPlugin("c", "exit status 1")...); n != 1 {
		t.Fatalf("drop resolved %d legs", n)
	}
	if c.calls != 1 || len(c.result) != 2 {
		t.Fatalf("aggregate %d calls, %d entries", c.calls, len(c.result))
	}
	got := c.result[1]
	if got.Plugin != "c" || got.Err == nil || got.Err.Code != "cancelled" {
		t.Fatalf("exit entry %#v", got)
	}
	if got.Err.Data["reason"] != "plugin_exited" {
		t.Fatalf("exit reason %#v", got.Err.Data)
	}
	if r.Pending() != 0 {
		t.Fatal("dropped leg leaked")
	}
}

func TestShutdownCancelsRemainingLegs(t *testing.T) {
	r, table, _, _ := newRunner(t, fakeSubs{"a.h": {"b"}}, fakeLiveness{"b": true}, calls.TableOptions{})
	c := &collector{}
	mustCall(t, r, "a.h", nil, c.onDone)
	if n := r.Complete(table.Close()...); n != 1 {
		t.Fatalf("close resolved %d legs", n)
	}
	if c.calls != 1 || len(c.result) != 1 {
		t.Fatalf("aggregate %d calls, %d entries", c.calls, len(c.result))
	}
	got := c.result[0]
	if got.Err == nil || got.Err.Code != "cancelled" || got.Err.Data["reason"] != "shutdown" {
		t.Fatalf("shutdown entry %#v", got)
	}
	if r.Pending() != 0 {
		t.Fatal("shutdown leg leaked")
	}
}

func TestCompleteIgnoresOtherRequestersAndUnknownHandles(t *testing.T) {
	r, table, box, _ := newRunner(t, fakeSubs{"a.h": {"b"}}, fakeLiveness{"b": true}, calls.TableOptions{})
	c := &collector{}
	mustCall(t, r, "a.h", nil, c.onDone)
	leg := box.sent[0]
	wireID, err := strconv.ParseInt(leg.msg.ID.String(), 10, 64)
	if err != nil {
		t.Fatalf("wire id %s is not numeric: %v", leg.msg.ID, err)
	}
	foreign := calls.Result{Handle: calls.Handle{Plugin: "b", ID: calls.ID(wireID)}, Requester: "dispatcher", Method: Method}
	if n := r.Complete(foreign); n != 0 {
		t.Fatalf("foreign requester resolved %d legs", n)
	}
	ghost := calls.Result{Handle: calls.Handle{Plugin: "b", ID: 999}, Requester: Requester, Method: Method}
	if n := r.Complete(ghost); n != 0 {
		t.Fatalf("unknown handle resolved %d legs", n)
	}
	if c.calls != 0 || r.Pending() != 1 {
		t.Fatal("filtered results disturbed the fan-out")
	}
	res, ok := table.Resolve("b", reply(leg.msg.ID, json.RawMessage(`null`)))
	complete(t, r, res, ok)
	if c.calls != 1 {
		t.Fatal("real reply lost after filtered ones")
	}
}

func TestNilOnDoneDiscardsAggregate(t *testing.T) {
	r, table, _, _ := newRunner(t, fakeSubs{"a.h": {"b"}}, fakeLiveness{"b": true}, calls.TableOptions{})
	mustCall(t, r, "a.h", nil, nil) // must not panic
	if out := table.Close(); len(out) != 1 {
		t.Fatalf("leg vanished: %+v", out)
	}
}

// --- payload extraction ----------------------------------------------------

func TestPayloadDataEdgeShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value json.RawMessage
		want  map[string]any
	}{
		{"empty result", nil, nil},
		{"null result", json.RawMessage(`null`), nil},
		{"non-object result", json.RawMessage(`"x"`), nil},
		{"no data member", json.RawMessage(`{}`), nil},
		{"null data", json.RawMessage(`{"data":null}`), nil},
		{"non-object data", json.RawMessage(`{"data":5}`), nil},
		{"object data", json.RawMessage(`{"data":{"n":1,"s":"a"}}`), map[string]any{"n": json.Number("1"), "s": "a"}},
		{"extra members ignored", json.RawMessage(`{"data":{"x":1},"meta":2}`), map[string]any{"x": json.Number("1")}},
	} {
		if got := payloadData(tc.value); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %#v want %#v", tc.name, got, tc.want)
		}
	}
}
