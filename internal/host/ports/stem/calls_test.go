package stem_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// loop is a single goroutine that runs posted functions, like the host loop.
type loop struct {
	q      chan func()
	inLoop atomic.Bool
	closed atomic.Bool
}

func newLoop(t *testing.T) *loop {
	l := &loop{q: make(chan func(), 16)}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for fn := range l.q {
			l.inLoop.Store(true)
			fn()
			l.inLoop.Store(false)
		}
	}()
	t.Cleanup(func() { close(l.q); <-stopped })
	return l
}

func (l *loop) post(fn func()) bool {
	if l.closed.Load() {
		return false
	}
	l.q <- fn
	return true
}

// do runs fn on the loop and waits for it.
func (l *loop) do(fn func()) {
	done := make(chan struct{})
	l.q <- func() { fn(); close(done) }
	<-done
}

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

type outbox struct{ sent chan protocol.Message }

func (o *outbox) Send(_ calls.PluginID, m protocol.Message) error { o.sent <- m; return nil }

type catalog map[string]dispatcher.ActionDef

func (c catalog) Lookup(a string) (dispatcher.ActionDef, bool) { d, ok := c[a]; return d, ok }
func (c catalog) List() []dispatcher.ActionDef {
	var out []dispatcher.ActionDef
	for _, d := range c {
		out = append(out, d)
	}
	return out
}

type live map[string]bool

func (l live) Live(id string) bool { return l[id] }

type focus struct{}

func (focus) Focus() dispatcher.FocusContext {
	return dispatcher.FocusContext{Surface: "s", Owner: "notes"}
}

type rig struct {
	loop  *loop
	port  *stem.Calls
	table *calls.Table
	out   *outbox
	clk   *clock
	disp  *dispatcher.Dispatcher
	// replyOnLoop records whether every host Reply ran on the loop.
	hostRan atomic.Bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{loop: newLoop(t), out: &outbox{sent: make(chan protocol.Message, 8)}, clk: &clock{t: time.Unix(1000, 0)}}
	r.table = calls.New(r.out, calls.TableOptions{Clock: r.clk})
	var err error
	r.disp, err = dispatcher.New(dispatcher.Options{
		Catalog: catalog{
			"notes.new":   {ID: "notes.new", Owner: "notes"},
			"notes.pub":   {ID: "notes.pub", Owner: "notes", Public: true},
			"gone.act":    {ID: "gone.act", Owner: "gone", Public: true},
			"husk.quit":   {ID: "husk.quit", Owner: "husk"},
			"husk.shared": {ID: "husk.shared", Owner: "husk", Public: true},
		},
		Liveness: live{"notes": true},
		Focus:    focus{},
		Calls:    r.table,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.disp.RegisterHost("husk.quit", func(req dispatcher.ActionRequest) dispatcher.ActionReply {
		r.hostRan.Store(r.loop.inLoop.Load())
		return dispatcher.ActionReply{Result: map[string]any{"args": req.Args}}
	})
	r.port = stem.NewCalls(r.disp, r.loop.post)
	return r
}

var (
	cli    = contracts.Caller{Kind: contracts.CallerCLI}
	plugin = func(id string) contracts.Caller { return contracts.Caller{Kind: contracts.CallerPlugin, PluginID: id} }
)

func TestArgsMustBeAnObject(t *testing.T) {
	r := newRig(t)
	for _, args := range []string{`[1]`, `"x"`, `5`, `true`, `{"a":1} {"b":2}`, `{`, `{"a":1}x`} {
		_, err := r.port.ActionInvoke(context.Background(), cli, "notes.new", json.RawMessage(args))
		if got := contracts.CodeOf(err); got != "invalid_params" {
			t.Errorf("args %s: code %q", args, got)
		}
	}
	if len(r.out.sent) != 0 {
		t.Fatal("bad args reached the owner")
	}
}

func TestRefusalsKeepWireCodes(t *testing.T) {
	r := newRig(t)
	tests := []struct {
		name   string
		caller contracts.Caller
		action string
		want   string
	}{
		{"malformed id", cli, "nodot", "invalid_params"},
		{"unknown", cli, "notes.nope", "not_found"},
		{"plugin foreign private", plugin("other"), "notes.new", "denied"},
		{"plugin on host action", plugin("other"), "husk.quit", "denied"},
		{"owner not live", cli, "gone.act", "unavailable"},
		{"unknown caller kind", contracts.Caller{}, "notes.new", "denied"},
		{"plugin caller without id", contracts.Caller{Kind: contracts.CallerPlugin}, "notes.new", "denied"},
		{"public host action without handler", plugin("other"), "husk.shared", "not_found"},
	}
	for _, tc := range tests {
		_, err := r.port.ActionInvoke(context.Background(), tc.caller, tc.action, nil)
		if got := contracts.CodeOf(err); got != tc.want {
			t.Errorf("%s: code %q, want %s", tc.name, got, tc.want)
		}
	}
}

func TestHostActionRunsOnLoopAndKeepsNumbers(t *testing.T) {
	r := newRig(t)
	out, err := r.port.ActionInvoke(context.Background(), cli, "husk.quit", json.RawMessage(`{"n":12345678901234567}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"args":{"n":12345678901234567}}` {
		t.Fatalf("result = %s", out)
	}
	if !r.hostRan.Load() {
		t.Fatal("host handler ran off the loop")
	}
	out, err = r.port.ActionInvoke(context.Background(), cli, "husk.quit", nil)
	if err != nil || string(out) != `{"args":null}` {
		t.Fatalf("nil args: %s, %v", out, err)
	}
}

// serve answers the next request the owner receives.
func (r *rig) serve(t *testing.T, reply string) protocol.Message {
	t.Helper()
	req := <-r.out.sent
	var msg protocol.Message
	if err := json.Unmarshal([]byte(`{"id":`+req.ID.String()+`,`+reply[1:]), &msg); err != nil {
		t.Fatal(err)
	}
	r.loop.do(func() {
		res, ok := r.table.Resolve("notes", msg)
		if !ok {
			t.Error("reply not matched")
			return
		}
		r.disp.Complete(res)
	})
	return req
}

func invoke(r *rig, c contracts.Caller, action string, args string) (json.RawMessage, error) {
	return r.port.ActionInvoke(context.Background(), c, action, json.RawMessage(args))
}

// A Stem caller is a Host caller: it gets State's focus, not Input's.
func TestOwnerRoundTrip(t *testing.T) {
	r := newRig(t)
	type res struct {
		out json.RawMessage
		err error
	}
	ch := make(chan res, 1)
	go func() {
		o, e := invoke(r, contracts.Caller{Kind: contracts.CallerStem}, "notes.new", `{"title":"Todo"}`)
		ch <- res{o, e}
	}()
	req := r.serve(t, `{"result":{"path":"/notes/todo.md"}}`)
	got := <-ch
	if got.err != nil || string(got.out) != `{"path":"/notes/todo.md"}` {
		t.Fatalf("%s, %v", got.out, got.err)
	}
	var p struct {
		Action string
		Args   map[string]any
		Caller map[string]any
		Focus  map[string]any
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatal(err)
	}
	if req.Method != "action.invoke" || p.Action != "notes.new" || p.Args["title"] != "Todo" || p.Caller["kind"] != "host" || p.Focus["surface"] != "s" {
		t.Fatalf("owner saw %s %+v", req.Method, p)
	}
}

func TestOwnerErrorPassesThrough(t *testing.T) {
	r := newRig(t)
	ch := make(chan error, 1)
	go func() { _, e := invoke(r, plugin("notes"), "notes.new", ""); ch <- e }()
	r.serve(t, `{"error":{"code":"failed","message":"disk full","data":{"why":"quota"}}}`)
	err := <-ch
	if contracts.CodeOf(err) != "failed" || err.Error() != "disk full" {
		t.Fatalf("%v", err)
	}
	if de, ok := err.(contracts.DataError); !ok || de.Data()["why"] != "quota" {
		t.Fatalf("data lost: %v", err)
	}
}

func TestOwnerExitCarriesReason(t *testing.T) {
	r := newRig(t)
	ch := make(chan error, 1)
	go func() { _, e := invoke(r, cli, "notes.new", ""); ch <- e }()
	<-r.out.sent
	r.loop.do(func() { r.disp.Complete(r.table.DropPlugin("notes", "plugin_exited")...) })
	err := <-ch
	de, ok := err.(contracts.DataError)
	if contracts.CodeOf(err) != "cancelled" || !ok || de.Data()["reason"] != "plugin_exited" {
		t.Fatalf("%v", err)
	}
}

func TestTimeoutWithInjectedClock(t *testing.T) {
	r := newRig(t)
	ch := make(chan error, 1)
	go func() { _, e := invoke(r, cli, "notes.new", ""); ch <- e }()
	<-r.out.sent
	r.loop.do(func() {
		r.clk.t = r.clk.t.Add(calls.DefaultTimeout + time.Second)
		r.disp.Complete(r.table.Expire(r.clk.t)...)
	})
	if got := contracts.CodeOf(<-ch); got != "timeout" {
		t.Fatalf("code %s", got)
	}
}

func TestContextEndsTheWaitOnly(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { _, e := r.port.ActionInvoke(ctx, cli, "notes.new", nil); ch <- e }()
	<-r.out.sent
	cancel()
	if got := contracts.CodeOf(<-ch); got != "cancelled" {
		t.Fatalf("code %s", got)
	}
	// The abandoned request is still pending; its late reply must not panic
	// or block the loop.
	r.loop.do(func() {
		id, _ := protocol.NewIntID(1)
		res, ok := r.table.Resolve("notes", protocol.Message{ID: &id, Result: json.RawMessage(`{}`)})
		if !ok {
			t.Error("late reply refused")
			return
		}
		r.disp.Complete(res)
	})

	dctx, dcancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer dcancel()
	_, err := r.port.ActionInvoke(dctx, cli, "notes.new", nil)
	if got := contracts.CodeOf(err); got != "timeout" {
		t.Fatalf("expired ctx: %s", got)
	}
}

func TestLoopStopped(t *testing.T) {
	r := newRig(t)
	r.loop.closed.Store(true)
	if _, err := invoke(r, cli, "notes.new", ""); contracts.CodeOf(err) != "unavailable" {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := r.port.ActionList(context.Background(), cli); contracts.CodeOf(err) != "unavailable" {
		t.Fatalf("list: %v", err)
	}
}

func TestActionListFiltersPerCaller(t *testing.T) {
	r := newRig(t)
	ids := func(c contracts.Caller) []contracts.ActionInfo {
		got, err := r.port.ActionList(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	all := []contracts.ActionInfo{
		{ID: "gone.act", Owner: "gone", Public: true, Live: false},
		{ID: "husk.quit", Owner: "husk", Live: true},
		{ID: "husk.shared", Owner: "husk", Public: true, Live: true},
		{ID: "notes.new", Owner: "notes", Live: true},
		{ID: "notes.pub", Owner: "notes", Public: true, Live: true},
	}
	for _, c := range []contracts.Caller{cli, {Kind: contracts.CallerStem}} {
		if got := ids(c); !reflect.DeepEqual(got, all) {
			t.Errorf("kind %d: %+v", c.Kind, got)
		}
	}
	wantOther := []contracts.ActionInfo{all[0], all[2], all[4]}
	if got := ids(plugin("other")); !reflect.DeepEqual(got, wantOther) {
		t.Errorf("foreign plugin: %+v", got)
	}
	wantOwner := []contracts.ActionInfo{all[0], all[2], all[3], all[4]}
	if got := ids(plugin("notes")); !reflect.DeepEqual(got, wantOwner) {
		t.Errorf("owner plugin: %+v", got)
	}
	if _, err := r.port.ActionList(context.Background(), contracts.Caller{}); contracts.CodeOf(err) != "denied" {
		t.Fatalf("unknown caller: %v", err)
	}
}

func TestListWaitHonoursContext(t *testing.T) {
	r := newRig(t)
	block := make(chan struct{})
	r.loop.q <- func() { <-block }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.port.ActionList(ctx, cli)
	if contracts.CodeOf(err) != "cancelled" {
		t.Fatalf("%v", err)
	}
	dctx, dcancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer dcancel()
	_, err = r.port.ActionList(dctx, cli)
	close(block)
	if contracts.CodeOf(err) != "timeout" {
		t.Fatalf("list deadline: %v", err)
	}
}

func TestNewCallsRequiresDependencies(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	stem.NewCalls(nil, nil)
}
