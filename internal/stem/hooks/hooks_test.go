package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
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
	errFor map[calls.PluginID]error // fails Sends to these plugins
}

func (b *fakeOutbox) Send(p calls.PluginID, m protocol.Message) error {
	if err, ok := b.errFor[p]; ok {
		return err
	}
	b.sent = append(b.sent, sentMsg{p, m})
	return nil
}

// fakeCatalog holds declared hooks and their registrations, already sorted
// the way a real Catalog returns them.
type fakeCatalog struct {
	defs map[string]HookDef
	subs map[string][]Subscription
}

func (c *fakeCatalog) Lookup(hook string) (HookDef, bool) { d, ok := c.defs[hook]; return d, ok }

func (c *fakeCatalog) Subscribers(hook string) []Subscription { return c.subs[hook] }

func (c *fakeCatalog) List() []HookDef {
	var out []HookDef
	for _, d := range c.defs {
		out = append(out, d)
	}
	return out
}

// hookOf declares hook id owned by owner, with a 10s timeout, and registers
// subscribers at ascending priorities in the given order.
func (c *fakeCatalog) declare(id, owner string, kind Kind, subscribers ...string) *fakeCatalog {
	if c.defs == nil {
		c.defs, c.subs = map[string]HookDef{}, map[string][]Subscription{}
	}
	c.defs[id] = HookDef{ID: id, Owner: owner, Kind: kind, Timeout: calls.DefaultTimeout}
	c.subs[id] = nil
	for i, p := range subscribers {
		c.subs[id] = append(c.subs[id], Subscription{Plugin: p, Priority: 10 + i})
	}
	return c
}

func (c *fakeCatalog) timeout(id string, d time.Duration) *fakeCatalog {
	def := c.defs[id]
	def.Timeout = d
	c.defs[id] = def
	return c
}

type fakeLiveness map[string]bool

func (l fakeLiveness) Live(pluginID string) bool { return l[pluginID] }

// rig is a runner over a real call table with a recording outbox.
type rig struct {
	t     *testing.T
	r     *Runner
	table *calls.Table
	box   *fakeOutbox
	clock *fakeClock
	logs  []string
}

func newRig(t *testing.T, cat Catalog, live fakeLiveness, topts calls.TableOptions, mut ...func(*Options)) *rig {
	t.Helper()
	g := &rig{t: t, box: &fakeOutbox{}, clock: &fakeClock{now: time.Unix(1000, 0)}}
	topts.Clock = g.clock
	g.table = calls.New(g.box, topts)
	opts := Options{Catalog: cat, Liveness: live, Calls: g.table, Logger: func(f string, a ...any) { g.logs = append(g.logs, fmt.Sprintf(f, a...)) }}
	for _, m := range mut {
		m(&opts)
	}
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	return g
}

// owner is the caller that owns the declared hook.
func (g *rig) owner(hook string) Caller {
	def, ok := g.r.opts.Catalog.Lookup(hook)
	if !ok {
		g.t.Fatalf("hook %q not declared", hook)
	}
	if def.Owner == HostOwner {
		return Caller{Kind: CallerHost}
	}
	return Caller{Kind: CallerPlugin, PluginID: def.Owner}
}

// fire calls hook as its owner and fails the test on a refusal.
func (g *rig) fire(hook string, data map[string]any, c *collector) {
	g.t.Helper()
	if err := g.r.Call(g.owner(hook), hook, data, c.onDone); err != nil {
		g.t.Fatalf("Call(%q): %v", hook, err)
	}
}

// legTo returns the latest message sent to plugin.
func (g *rig) legTo(plugin string) sentMsg {
	g.t.Helper()
	for i := len(g.box.sent) - 1; i >= 0; i-- {
		if string(g.box.sent[i].plugin) == plugin {
			return g.box.sent[i]
		}
	}
	g.t.Fatalf("no leg sent to %s", plugin)
	return sentMsg{}
}

// answer replies to plugin's latest leg and feeds the runner.
func (g *rig) answer(plugin, result string) {
	g.t.Helper()
	res, ok := g.table.Resolve(calls.PluginID(plugin), protocol.Message{ID: g.legTo(plugin).msg.ID, Result: json.RawMessage(result)})
	if !ok {
		g.t.Fatalf("table refused %s's reply", plugin)
	}
	if n := g.r.Complete(res); n != 1 {
		g.t.Fatalf("%s's reply resolved %d legs, want 1", plugin, n)
	}
}

func (g *rig) fail(plugin string, e *protocol.Error) {
	g.t.Helper()
	res, ok := g.table.Resolve(calls.PluginID(plugin), protocol.Message{ID: g.legTo(plugin).msg.ID, Error: e})
	if !ok {
		g.t.Fatalf("table refused %s's error", plugin)
	}
	if n := g.r.Complete(res); n != 1 {
		g.t.Fatalf("resolved %d legs", n)
	}
}

// collector records outcomes as they are delivered.
type collector struct {
	n   int
	out Outcome
}

func (c *collector) onDone(o Outcome) { c.n++; c.out = o }

func wantCode(t *testing.T, err error, code protocol.Code) *HookError {
	t.Helper()
	var he *HookError
	if !errors.As(err, &he) || he.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	return he
}

func entryErr(t *testing.T, e HookResult, code protocol.Code) {
	t.Helper()
	if e.Err == nil || e.Err.Code != code {
		t.Fatalf("entry %#v: want error %s", e, code)
	}
}

// --- New -------------------------------------------------------------------

func TestNewPanicsOnMissingViews(t *testing.T) {
	opts := Options{Catalog: &fakeCatalog{}, Liveness: fakeLiveness{}, Calls: calls.New(nil, calls.TableOptions{})}
	for _, tc := range []struct {
		name string
		mut  func(Options) Options
	}{
		{"no catalog", func(o Options) Options { o.Catalog = nil; return o }},
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

func TestNewRejectsNegativeMaxInFlight(t *testing.T) {
	r, err := New(Options{Catalog: &fakeCatalog{}, Liveness: fakeLiveness{}, Calls: &errSender{}, MaxInFlight: -1})
	if err == nil || r != nil {
		t.Fatalf("New() = %v, %v; want nil runner and an error", r, err)
	}
}

// --- resolution: id syntax, ownership --------------------------------------

func TestHookIDSyntax(t *testing.T) {
	long := strings.Repeat("a", 254) + ".b" // 256 bytes
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"a.b", true},
		{"go.reference.before-refresh", true},
		{"husk.before-state-clear", true},
		{long, true},
		{long + "c", false},
		{"", false},
		{"nodot", false},
		{".b", false},
		{"a.", false},
		{"a..b", false},
		{"a b.c", false},
		{"a.b\tc", false},
		{"a;b.c", false},
		{"a=b.c", false},
		{`a\b.c`, false},
		{"a.*", false},
		{"*.b", false},
	} {
		if got := wellFormed(tc.id); got != tc.want {
			t.Errorf("wellFormed(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestSyntaxIsCheckedBeforeOwnershipForEveryCaller(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.h", "a", Gate), fakeLiveness{}, calls.TableOptions{})
	for _, c := range []Caller{{Kind: CallerCLI}, {Kind: CallerPlugin, PluginID: "a"}, {Kind: CallerHost}, {Kind: CallerInput}} {
		for _, id := range []string{"", "nodot", "a.*", "a b.c"} {
			col := &collector{}
			wantCode(t, g.r.Call(c, id, nil, col.onDone), protocol.CodeInvalidParams)
			if col.n != 0 {
				t.Fatalf("%s %q: onDone ran for a refused call", c.label(), id)
			}
		}
	}
}

func TestOwnershipTable(t *testing.T) {
	cat := (&fakeCatalog{}).
		declare("a.h", "a", Gate).
		declare("go.reference.before-refresh", "go.reference", Gate).
		declare("husk.before-state-clear", HostOwner, Gate)
	for _, tc := range []struct {
		name   string
		caller Caller
		hook   string
		want   protocol.Code // "" means accepted
	}{
		{"owner plugin", Caller{Kind: CallerPlugin, PluginID: "a"}, "a.h", ""},
		{"foreign plugin", Caller{Kind: CallerPlugin, PluginID: "b"}, "a.h", protocol.CodeDenied},
		{"plugin go is not go.reference", Caller{Kind: CallerPlugin, PluginID: "go"}, "go.reference.before-refresh", protocol.CodeDenied},
		{"plugin go.reference owns it", Caller{Kind: CallerPlugin, PluginID: "go.reference"}, "go.reference.before-refresh", ""},
		{"plugin on a missing hook", Caller{Kind: CallerPlugin, PluginID: "a"}, "a.missing", protocol.CodeDenied},
		{"plugin without id", Caller{Kind: CallerPlugin}, "a.h", protocol.CodeDenied},
		{"cli on an existing hook", Caller{Kind: CallerCLI}, "a.h", protocol.CodeDenied},
		{"cli on a host hook", Caller{Kind: CallerCLI}, "husk.before-state-clear", protocol.CodeDenied},
		{"cli on a missing hook", Caller{Kind: CallerCLI}, "a.missing", protocol.CodeDenied},
		{"input caller", Caller{Kind: CallerInput}, "a.h", protocol.CodeDenied},
		{"host on a host hook", Caller{Kind: CallerHost}, "husk.before-state-clear", ""},
		{"host on a plugin hook", Caller{Kind: CallerHost}, "a.h", protocol.CodeDenied},
		{"host on a missing hook", Caller{Kind: CallerHost}, "husk.missing", protocol.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t, cat, fakeLiveness{}, calls.TableOptions{})
			col := &collector{}
			err := g.r.Call(tc.caller, tc.hook, nil, col.onDone)
			if tc.want == "" {
				if err != nil || col.n != 1 {
					t.Fatalf("want accepted, got %v (%d outcomes)", err, col.n)
				}
				return
			}
			wantCode(t, err, tc.want)
			if col.n != 0 || len(g.box.sent) != 0 {
				t.Fatal("refused call leaked work")
			}
		})
	}
}

func TestMissingAndForeignHooksAnswerAPluginTheSameMessage(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.h", "a", Gate), fakeLiveness{}, calls.TableOptions{})
	c := Caller{Kind: CallerPlugin, PluginID: "b"}
	foreign := wantCode(t, g.r.Call(c, "a.h", nil, nil), protocol.CodeDenied)
	missing := wantCode(t, g.r.Call(c, "a.zzz", nil, nil), protocol.CodeDenied)
	// The hook id is the only thing allowed to differ.
	if strings.ReplaceAll(foreign.Message, "a.h", "X") != strings.ReplaceAll(missing.Message, "a.zzz", "X") {
		t.Fatalf("messages differ: %q vs %q", foreign.Message, missing.Message)
	}
}

// --- empty outcomes --------------------------------------------------------

func TestNoLiveSubscriberAnswersAtOnceWithTheKindsEmptyOutcome(t *testing.T) {
	cat := (&fakeCatalog{}).
		declare("a.t", "a", Transform, "dead").
		declare("a.c", "a", Collect, "dead").
		declare("a.g", "a", Gate)
	for _, tc := range []struct {
		hook string
		kind Kind
	}{{"a.t", Transform}, {"a.c", Collect}, {"a.g", Gate}} {
		g := newRig(t, cat, fakeLiveness{"dead": false}, calls.TableOptions{})
		col := &collector{}
		g.fire(tc.hook, map[string]any{"x": 1}, col)
		o := col.out
		if col.n != 1 || o.Kind != tc.kind || !o.Allowed || o.Results == nil || len(o.Results) != 0 {
			t.Fatalf("%s: want one empty outcome, got %d x %#v", tc.hook, col.n, o)
		}
		if tc.kind == Transform && !reflect.DeepEqual(o.Data, map[string]any{"x": 1}) {
			t.Fatalf("transform data %#v, want the input", o.Data)
		}
		if tc.kind == Gate && (o.Vetoes == nil || len(o.Vetoes) != 0) {
			t.Fatalf("gate vetoes %#v, want empty", o.Vetoes)
		}
		if len(g.box.sent) != 0 || g.r.Pending() != 0 || g.r.inFlight[tc.hook] != 0 {
			t.Fatal("no leg and no in-flight slot expected")
		}
	}
}

// --- transform seam --------------------------------------------------------

func TestTransformIsRefusedAtTheSeamWithoutCounting(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.t", "a", Transform, "b"), fakeLiveness{"b": true}, calls.TableOptions{})
	col := &collector{}
	he := wantCode(t, g.r.Call(Caller{Kind: CallerPlugin, PluginID: "a"}, "a.t", nil, col.onDone), protocol.CodeInternal)
	if he.Message != "transform kind not implemented" {
		t.Fatalf("message %q", he.Message)
	}
	if col.n != 0 || len(g.box.sent) != 0 || g.r.Pending() != 0 || g.r.inFlight["a.t"] != 0 {
		t.Fatal("refused transform leaked work")
	}
}

// --- fan-out wire ----------------------------------------------------------

func TestLegParamsCarryHookKindAndDataOnly(t *testing.T) {
	cat := (&fakeCatalog{}).declare("a.g", "a", Gate, "b", "dead", "c")
	g := newRig(t, cat, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	col := &collector{}
	g.fire("a.g", map[string]any{"x": 1}, col)
	if len(g.box.sent) != 2 || g.box.sent[0].plugin != "b" || g.box.sent[1].plugin != "c" {
		t.Fatalf("want legs to b then c in priority order, got %+v", g.box.sent)
	}
	for _, s := range g.box.sent {
		if s.msg.Method != "hook.call" {
			t.Fatalf("method %q", s.msg.Method)
		}
		if got := string(s.msg.Params); got != `{"hook":"a.g","kind":"gate","data":{"x":1}}` {
			t.Fatalf("params %s", got)
		}
	}
	if g.box.sent[0].msg.ID.String() == g.box.sent[1].msg.ID.String() {
		t.Fatal("legs share one request id")
	}
	// A nil payload is an empty object, never absent.
	g.fire("a.g", nil, &collector{})
	if got := string(g.box.sent[len(g.box.sent)-1].msg.Params); got != `{"hook":"a.g","kind":"gate","data":{}}` {
		t.Fatalf("params %s", got)
	}
	// Collect legs say so.
	cat.declare("a.c", "a", Collect, "b")
	g.fire("a.c", nil, &collector{})
	if got := string(g.box.sent[len(g.box.sent)-1].msg.Params); got != `{"hook":"a.c","kind":"collect","data":{}}` {
		t.Fatalf("params %s", got)
	}
}

func TestUnencodableDataIsInvalidParams(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.g", "a", Gate, "b"), fakeLiveness{"b": true}, calls.TableOptions{})
	wantCode(t, g.r.Call(Caller{Kind: CallerPlugin, PluginID: "a"}, "a.g", map[string]any{"f": func() {}}, nil), protocol.CodeInvalidParams)
	if len(g.box.sent) != 0 || g.r.inFlight["a.g"] != 0 {
		t.Fatal("refused call leaked work")
	}
}

func TestCapsRefuseWholeCall(t *testing.T) {
	cat := (&fakeCatalog{}).declare("a.b", "a", Gate, "b").declare("a.b2", "a", Gate, "b").declare("a.c", "a", Gate, "c")
	live := fakeLiveness{"b": true, "c": true}
	a := Caller{Kind: CallerPlugin, PluginID: "a"}

	// Overall cap: the second call is refused whole, nothing is sent.
	g := newRig(t, cat, live, calls.TableOptions{MaxPending: 1})
	g.fire("a.b", nil, &collector{})
	col := &collector{}
	wantCode(t, g.r.Call(a, "a.c", nil, col.onDone), protocol.CodeBusy)
	if len(g.box.sent) != 1 || col.n != 0 || g.r.Pending() != 1 || g.r.inFlight["a.c"] != 0 {
		t.Fatalf("refused call leaked work: sent=%d done=%d pending=%d", len(g.box.sent), col.n, g.r.Pending())
	}

	// Per-subscriber cap: b's second hook is refused, c's hook still runs.
	g = newRig(t, cat, live, calls.TableOptions{MaxPendingPerPlugin: 1})
	g.fire("a.b", nil, &collector{})
	he := wantCode(t, g.r.Call(a, "a.b2", nil, nil), protocol.CodeBusy)
	if he.Message != "too many pending hook calls for one subscriber" {
		t.Fatalf("per-subscriber cap: %v", he)
	}
	g.fire("a.c", nil, &collector{})
	if len(g.box.sent) != 2 {
		t.Fatalf("full subscriber bricked another: sent=%d", len(g.box.sent))
	}
}

func TestSendFailureBecomesThatSubscribersEntry(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.g", "a", Gate, "b", "c"), fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
	g.box.errFor = map[calls.PluginID]error{"b": errors.New("writer closed")}
	col := &collector{}
	g.fire("a.g", map[string]any{"x": 1}, col)
	if len(g.box.sent) != 1 || g.r.Pending() != 1 || col.n != 0 {
		t.Fatalf("c's leg missing or early outcome: sent=%d legs=%d done=%d", len(g.box.sent), g.r.Pending(), col.n)
	}
	g.answer("c", `{}`)
	if col.n != 1 || len(col.out.Results) != 2 {
		t.Fatalf("delivered %d outcomes with %d entries", col.n, len(col.out.Results))
	}
	first, second := col.out.Results[0], col.out.Results[1]
	if first.Plugin != "b" || first.Err == nil || first.Err.Code != CodeUnavailable || first.Err.Data["reason"] != "disconnected" {
		t.Fatalf("failed send's entry %#v", first)
	}
	if second.Plugin != "c" || second.Err != nil {
		t.Fatalf("c's entry %#v", second)
	}
	if !col.out.Allowed {
		t.Fatal("an undeliverable subscriber must count as allow")
	}
}

// errSender fails every Send, exercising the classification the real table
// cannot be forced into from a valid configuration.
type errSender struct{ err error }

func (s *errSender) Send(calls.PluginID, string, json.RawMessage, calls.Options) (calls.Handle, error) {
	return calls.Handle{}, s.err
}

func (s *errSender) CanSend(...calls.PluginID) error { return nil }

func TestSendFailureReasonOnlyForUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   protocol.Code
		reason string
	}{
		{"unavailable", fmt.Errorf("send b: %w", calls.ErrUnavailable), CodeUnavailable, "disconnected"},
		{"internal", errors.New("send b: bad options"), CodeInternal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New(Options{Catalog: (&fakeCatalog{}).declare("a.g", "a", Gate, "b"), Liveness: fakeLiveness{"b": true}, Calls: &errSender{err: tc.err}})
			if err != nil {
				t.Fatal(err)
			}
			col := &collector{}
			if err := r.Call(Caller{Kind: CallerPlugin, PluginID: "a"}, "a.g", nil, col.onDone); err != nil {
				t.Fatal(err)
			}
			if col.n != 1 || len(col.out.Results) != 1 {
				t.Fatalf("delivered %d outcomes with %d entries", col.n, len(col.out.Results))
			}
			got := col.out.Results[0]
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
			if r.inFlight["a.g"] != 0 {
				t.Fatal("all-failed call still counts")
			}
		})
	}
}

// --- results order, leg deadlines, exits -----------------------------------

func TestResultsAreInPriorityOrderWhateverTheReplyOrder(t *testing.T) {
	for _, kind := range []Kind{Collect, Gate} {
		g := newRig(t, (&fakeCatalog{}).declare("a.h", "a", kind, "p1", "p2", "p3"), fakeLiveness{"p1": true, "p2": true, "p3": true}, calls.TableOptions{})
		col := &collector{}
		g.fire("a.h", nil, col)
		g.answer("p3", `{}`)
		g.answer("p2", `{}`)
		if col.n != 0 {
			t.Fatal("outcome before the last leg")
		}
		g.answer("p1", `{}`)
		var got []string
		for _, e := range col.out.Results {
			got = append(got, e.Plugin)
		}
		if col.n != 1 || !reflect.DeepEqual(got, []string{"p1", "p2", "p3"}) {
			t.Fatalf("%s: delivered %d, order %v", kind, col.n, got)
		}
	}
}

func TestLegDeadlineIsTheDeclaredTimeout(t *testing.T) {
	for _, d := range []time.Duration{2 * time.Second, 5 * time.Second} {
		cat := (&fakeCatalog{}).declare("a.g", "a", Gate, "b", "c").timeout("a.g", d)
		g := newRig(t, cat, fakeLiveness{"b": true, "c": true}, calls.TableOptions{})
		col := &collector{}
		g.fire("a.g", nil, col)
		g.answer("b", `{}`)
		g.clock.advance(d - time.Nanosecond)
		if out := g.table.Expire(g.clock.now); len(out) != 0 {
			t.Fatalf("%s: expired before the declared timeout: %+v", d, out)
		}
		g.clock.advance(time.Nanosecond)
		if n := g.r.Complete(g.table.Expire(g.clock.now)...); n != 1 {
			t.Fatalf("%s: expiry resolved %d legs", d, n)
		}
		if col.n != 1 || len(col.out.Results) != 2 {
			t.Fatalf("%s: delivered %d outcomes", d, col.n)
		}
		entryErr(t, col.out.Results[1], protocol.CodeTimeout)
		if !col.out.Allowed {
			t.Fatal("a timed-out subscriber must count as allow")
		}
		if g.r.Pending() != 0 || g.table.Stats().Pending != 0 {
			t.Fatal("expired leg leaked")
		}
	}
}

func TestSubscriberErrorExitAndShutdownEntries(t *testing.T) {
	cat := (&fakeCatalog{}).declare("a.g", "a", Gate, "b", "c", "d")
	g := newRig(t, cat, fakeLiveness{"b": true, "c": true, "d": true}, calls.TableOptions{})
	col := &collector{}
	g.fire("a.g", nil, col)
	g.fail("b", &protocol.Error{Code: protocol.CodeFailed, Message: "no", Data: json.RawMessage(`{"k":1}`)})
	if n := g.r.Complete(g.table.DropPlugin("c", "exit status 1")...); n != 1 {
		t.Fatalf("drop resolved %d legs", n)
	}
	if n := g.r.Complete(g.table.Close()...); n != 1 {
		t.Fatalf("close resolved %d legs", n)
	}
	if col.n != 1 || !col.out.Allowed {
		t.Fatalf("delivered %d, allowed %v", col.n, col.out.Allowed)
	}
	b, c, d := col.out.Results[0], col.out.Results[1], col.out.Results[2]
	if b.Err == nil || b.Err.Code != protocol.CodeFailed || b.Err.Message != "no" || b.Err.Data["k"] != json.Number("1") {
		t.Fatalf("error entry %#v", b)
	}
	if c.Err == nil || c.Err.Code != protocol.CodeCancelled || c.Err.Data["reason"] != "plugin_exited" {
		t.Fatalf("exit entry %#v", c)
	}
	if d.Err == nil || d.Err.Code != protocol.CodeCancelled || d.Err.Data["reason"] != "shutdown" {
		t.Fatalf("shutdown entry %#v", d)
	}
	if g.r.Pending() != 0 {
		t.Fatal("legs leaked")
	}
}

func TestCompleteIgnoresOtherRequestersAndUnknownHandles(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.g", "a", Gate, "b"), fakeLiveness{"b": true}, calls.TableOptions{})
	col := &collector{}
	g.fire("a.g", nil, col)
	leg := g.legTo("b")
	wireID, err := strconv.ParseInt(leg.msg.ID.String(), 10, 64)
	if err != nil {
		t.Fatalf("wire id %s is not numeric: %v", leg.msg.ID, err)
	}
	foreign := calls.Result{Handle: calls.Handle{Plugin: "b", ID: calls.ID(wireID)}, Requester: "dispatcher", Method: Method}
	if n := g.r.Complete(foreign); n != 0 {
		t.Fatalf("foreign requester resolved %d legs", n)
	}
	ghost := calls.Result{Handle: calls.Handle{Plugin: "b", ID: 999}, Requester: Requester, Method: Method}
	if n := g.r.Complete(ghost); n != 0 {
		t.Fatalf("unknown handle resolved %d legs", n)
	}
	if col.n != 0 || g.r.Pending() != 1 {
		t.Fatal("filtered results disturbed the call")
	}
	g.answer("b", `null`)
	if col.n != 1 {
		t.Fatal("real reply lost after filtered ones")
	}
}

func TestNilOnDoneDiscardsOutcome(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.g", "a", Gate, "b"), fakeLiveness{"b": true}, calls.TableOptions{})
	if err := g.r.Call(Caller{Kind: CallerPlugin, PluginID: "a"}, "a.g", nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := g.r.Complete(g.table.Close()...); n != 1 {
		t.Fatalf("leg vanished: resolved %d", n)
	}
	if g.r.inFlight["a.g"] != 0 {
		t.Fatal("discarded call still counts")
	}
}

// --- in-flight cap ---------------------------------------------------------

func TestInFlightCap(t *testing.T) {
	cat := (&fakeCatalog{}).declare("a.g", "a", Gate, "b").declare("a.other", "a", Gate, "b")
	a := Caller{Kind: CallerPlugin, PluginID: "a"}
	fill := func(g *rig, hook string, n int) {
		for i := 0; i < n; i++ {
			if err := g.r.Call(a, hook, nil, nil); err != nil {
				t.Fatalf("call %d: %v", i+1, err)
			}
		}
	}

	t.Run("sixteen pass, the seventeenth is busy", func(t *testing.T) {
		g := newRig(t, cat, fakeLiveness{"b": true}, calls.TableOptions{})
		fill(g, "a.g", 16)
		col := &collector{}
		he := wantCode(t, g.r.Call(a, "a.g", nil, col.onDone), protocol.CodeBusy)
		if he.Data["reason"] != ReasonInFlight || ReasonInFlight != "hook_in_flight" {
			t.Fatalf("data %v", he.Data)
		}
		if col.n != 0 || len(g.box.sent) != 16 {
			t.Fatalf("refused call leaked: done=%d sent=%d", col.n, len(g.box.sent))
		}
		// One outcome frees a slot.
		g.answer("b", `{}`)
		if err := g.r.Call(a, "a.g", nil, nil); err != nil {
			t.Fatalf("after one outcome: %v", err)
		}
	})

	t.Run("different hook ids do not share the count", func(t *testing.T) {
		g := newRig(t, cat, fakeLiveness{"b": true}, calls.TableOptions{})
		fill(g, "a.g", 16)
		fill(g, "a.other", 1)
	})

	t.Run("MaxInFlight 2 makes the third busy", func(t *testing.T) {
		g := newRig(t, cat, fakeLiveness{"b": true}, calls.TableOptions{}, func(o *Options) { o.MaxInFlight = 2 })
		fill(g, "a.g", 2)
		wantCode(t, g.r.Call(a, "a.g", nil, nil), protocol.CodeBusy)
	})

	t.Run("refused calls never count", func(t *testing.T) {
		g := newRig(t, cat, fakeLiveness{"b": true}, calls.TableOptions{MaxPending: 1}, func(o *Options) { o.MaxInFlight = 2 })
		fill(g, "a.g", 1)
		// Refused: ownership, syntax, data, Calls cap.
		wantCode(t, g.r.Call(Caller{Kind: CallerCLI}, "a.g", nil, nil), protocol.CodeDenied)
		wantCode(t, g.r.Call(a, "a.*", nil, nil), protocol.CodeInvalidParams)
		wantCode(t, g.r.Call(a, "a.g", map[string]any{"f": func() {}}, nil), protocol.CodeInvalidParams)
		wantCode(t, g.r.Call(a, "a.g", nil, nil), protocol.CodeBusy) // Calls cap, not the hook cap
		if g.r.inFlight["a.g"] != 1 {
			t.Fatalf("in flight %d, want 1", g.r.inFlight["a.g"])
		}
	})

	t.Run("a dropped continuation still counts until onDone", func(t *testing.T) {
		g := newRig(t, cat, fakeLiveness{"b": true}, calls.TableOptions{}, func(o *Options) { o.MaxInFlight = 1 })
		dropped := 0
		if err := g.r.Call(a, "a.g", nil, func(Outcome) { dropped++ }); err != nil {
			t.Fatal(err)
		}
		// The owner is gone, so nothing consumes the outcome, but the leg drains.
		wantCode(t, g.r.Call(a, "a.g", nil, nil), protocol.CodeBusy)
		g.answer("b", `{}`)
		if dropped != 1 {
			t.Fatalf("onDone ran %d times", dropped)
		}
		if err := g.r.Call(a, "a.g", nil, nil); err != nil {
			t.Fatalf("after the drain: %v", err)
		}
	})

	t.Run("Close empties the count", func(t *testing.T) {
		g := newRig(t, cat, fakeLiveness{"b": true}, calls.TableOptions{})
		fill(g, "a.g", 16)
		if n := g.r.Complete(g.table.Close()...); n != 16 {
			t.Fatalf("close resolved %d legs", n)
		}
		if g.r.inFlight["a.g"] != 0 {
			t.Fatalf("in flight %d after close", g.r.inFlight["a.g"])
		}
	})

	t.Run("an immediate outcome counts while onDone runs", func(t *testing.T) {
		g := newRig(t, (&fakeCatalog{}).declare("a.n", "a", Gate), fakeLiveness{}, calls.TableOptions{}, func(o *Options) { o.MaxInFlight = 1 })
		var nested error
		if err := g.r.Call(a, "a.n", nil, func(Outcome) { nested = g.r.Call(a, "a.n", nil, nil) }); err != nil {
			t.Fatal(err)
		}
		wantCode(t, nested, protocol.CodeBusy)
		if g.r.inFlight["a.n"] != 0 {
			t.Fatal("slot not released")
		}
	})
}

// --- gate ------------------------------------------------------------------

func gateRig(t *testing.T, subs ...string) (*rig, *collector) {
	live := fakeLiveness{}
	for _, s := range subs {
		live[s] = true
	}
	g := newRig(t, (&fakeCatalog{}).declare("a.g", "a", Gate, subs...), live, calls.TableOptions{})
	col := &collector{}
	g.fire("a.g", nil, col)
	return g, col
}

func TestGateAllowAndVetoInPriorityOrder(t *testing.T) {
	g, col := gateRig(t, "p1", "p2", "p3", "p4")
	g.answer("p4", `{"veto":true,"reason":"last"}`)
	g.answer("p3", `{}`)
	g.answer("p2", `{"veto":true}`)
	g.answer("p1", `{"veto":false,"reason":"ignored"}`)
	o := col.out
	if o.Kind != Gate || o.Allowed {
		t.Fatalf("outcome %#v", o)
	}
	want := []Veto{{Plugin: "p2"}, {Plugin: "p4", Reason: "last"}}
	if !reflect.DeepEqual(o.Vetoes, want) {
		t.Fatalf("vetoes %#v, want %#v", o.Vetoes, want)
	}
	p1 := o.Results[0]
	if p1.Veto || p1.Reason != "" || p1.Err != nil {
		t.Fatalf("a reason with veto false must be ignored: %#v", p1)
	}
	if r := o.Results[3]; !r.Veto || r.Reason != "last" {
		t.Fatalf("veto entry %#v", r)
	}
	if g.r.inFlight["a.g"] != 0 {
		t.Fatal("slot not released")
	}
}

func TestGateAllowedWhenNoVeto(t *testing.T) {
	g, col := gateRig(t, "p1", "p2")
	g.answer("p1", `{}`)
	g.answer("p2", `null`)
	if !col.out.Allowed || col.out.Vetoes == nil || len(col.out.Vetoes) != 0 {
		t.Fatalf("outcome %#v", col.out)
	}
}

func TestGateWrongTypesAreInvalidParamsAndCountAsAllow(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"veto string", `{"veto":"yes"}`},
		{"veto number", `{"veto":1}`},
		{"veto null", `{"veto":null}`},
		{"reason number", `{"veto":true,"reason":5}`},
		{"reason null", `{"veto":true,"reason":null}`},
		{"reason number without veto", `{"reason":5}`},
		{"non-object result", `[1]`},
		{"string result", `"veto"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, col := gateRig(t, "p1")
			g.answer("p1", tc.reply)
			e := col.out.Results[0]
			entryErr(t, e, protocol.CodeInvalidParams)
			if e.Veto || !col.out.Allowed || len(col.out.Vetoes) != 0 {
				t.Fatalf("a malformed reply must count as allow: %#v", col.out)
			}
		})
	}
}

func TestGateIgnoresForeignFieldsAndLogsOncePerSubscriberAndHook(t *testing.T) {
	cat := (&fakeCatalog{}).declare("a.g", "a", Gate, "p1", "p2").declare("a.g2", "a", Gate, "p1")
	g := newRig(t, cat, fakeLiveness{"p1": true, "p2": true}, calls.TableOptions{})
	for i := 0; i < 3; i++ {
		col := &collector{}
		g.fire("a.g", nil, col)
		g.answer("p1", `{"data":{"x":1},"items":[1]}`)
		g.answer("p2", `{"data":{}}`)
		for _, e := range col.out.Results {
			if e.Err != nil || e.Data != nil || e.Items != nil || e.Veto {
				t.Fatalf("foreign fields leaked into the entry: %#v", e)
			}
		}
		if !col.out.Allowed {
			t.Fatal("foreign fields must not veto")
		}
	}
	if len(g.logs) != 2 { // p1 and p2 on a.g
		t.Fatalf("want 2 log lines, got %d: %q", len(g.logs), g.logs)
	}
	g.fire("a.g2", nil, &collector{})
	g.answer("p1", `{"items":[]}`)
	if len(g.logs) != 3 {
		t.Fatalf("a second hook for the same subscriber logs again: %q", g.logs)
	}
	g.fire("a.g2", nil, &collector{})
	g.answer("p1", `{"items":[]}`)
	if len(g.logs) != 3 {
		t.Fatalf("repeat logged: %q", g.logs)
	}
}

// --- collect ---------------------------------------------------------------

func collectRig(t *testing.T, subs ...string) (*rig, *collector) {
	live := fakeLiveness{}
	for _, s := range subs {
		live[s] = true
	}
	g := newRig(t, (&fakeCatalog{}).declare("a.c", "a", Collect, subs...), live, calls.TableOptions{})
	col := &collector{}
	g.fire("a.c", nil, col)
	return g, col
}

func TestCollectItemsInPriorityOrder(t *testing.T) {
	g, col := collectRig(t, "p1", "p2", "p3")
	g.answer("p3", `{"items":[{"label":"Three"}]}`)
	g.answer("p2", `{}`)
	g.answer("p1", `{"items":[1,"two",null,[3],{"n":4.0}]}`)
	o := col.out
	if o.Kind != Collect || !o.Allowed || o.Data != nil {
		t.Fatalf("outcome %#v", o)
	}
	wantP1 := []any{json.Number("1"), "two", nil, []any{json.Number("3")}, map[string]any{"n": json.Number("4.0")}}
	if !reflect.DeepEqual(o.Results[0].Items, wantP1) {
		t.Fatalf("p1 items %#v", o.Results[0].Items)
	}
	if o.Results[1].Items != nil || o.Results[1].Err != nil {
		t.Fatalf("{} must contribute nothing: %#v", o.Results[1])
	}
	if !reflect.DeepEqual(o.Results[2].Items, []any{map[string]any{"label": "Three"}}) {
		t.Fatalf("p3 items %#v", o.Results[2].Items)
	}
}

func TestCollectWrongTypesAreInvalidParamsAndContributeNothing(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"object", `{"items":{"a":1}}`},
		{"string", `{"items":"x"}`},
		{"number", `{"items":3}`},
		{"null", `{"items":null}`},
		{"non-object result", `[1]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, col := collectRig(t, "p1")
			g.answer("p1", tc.reply)
			e := col.out.Results[0]
			entryErr(t, e, protocol.CodeInvalidParams)
			if e.Items != nil {
				t.Fatalf("items %#v", e.Items)
			}
		})
	}
}

func TestCollectIgnoresForeignFieldsAndLogsOnce(t *testing.T) {
	g, col := collectRig(t, "p1")
	g.answer("p1", `{"veto":true,"reason":"no","data":{"x":1},"items":[1]}`)
	e := col.out.Results[0]
	if e.Err != nil || e.Veto || e.Reason != "" || e.Data != nil || len(e.Items) != 1 || !col.out.Allowed {
		t.Fatalf("entry %#v", e)
	}
	if len(g.logs) != 1 {
		t.Fatalf("logs %q", g.logs)
	}
	g.fire("a.c", nil, &collector{})
	g.answer("p1", `{"veto":true}`)
	if len(g.logs) != 1 {
		t.Fatalf("repeat logged: %q", g.logs)
	}
}

// --- reply size ------------------------------------------------------------

// itemsReply returns a compact collect reply of exactly n bytes.
func itemsReply(n int) string {
	const frame = `{"items":["` + `"]}`
	return `{"items":["` + strings.Repeat("x", n-len(frame)) + `"]}`
}

func TestReplySizeLimitIsMeasuredOnCompactJSONForEveryKind(t *testing.T) {
	// Collect: exactly 1 MiB passes, one byte more is that subscriber's entry.
	g, col := collectRig(t, "p1", "p2")
	g.answer("p1", itemsReply(MaxPayload))
	g.answer("p2", itemsReply(MaxPayload+1))
	if e := col.out.Results[0]; e.Err != nil || len(e.Items) != 1 {
		t.Fatalf("1 MiB reply refused: %#v", e.Err)
	}
	if e := col.out.Results[1]; e.Items != nil {
		t.Fatal("oversized reply contributed items")
	} else {
		entryErr(t, e, protocol.CodeInvalidParams)
	}

	// Whitespace does not count: the compact form is what is measured.
	g, col = collectRig(t, "p1")
	g.answer("p1", itemsReply(MaxPayload)[:1]+strings.Repeat(" ", 64)+itemsReply(MaxPayload)[1:])
	if e := col.out.Results[0]; e.Err != nil || len(e.Items) != 1 {
		t.Fatalf("padded reply refused: %#v", e.Err)
	}

	// Gate: oversized whatever it contains, even a plain allow with padding.
	g, col = gateRig(t, "p1")
	g.answer("p1", `{"veto":true,"reason":"`+strings.Repeat("x", MaxPayload)+`"}`)
	entryErr(t, col.out.Results[0], protocol.CodeInvalidParams)
	if !col.out.Allowed {
		t.Fatal("an oversized veto must count as allow")
	}
}
