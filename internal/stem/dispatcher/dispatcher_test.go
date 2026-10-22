package dispatcher_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

type outbox struct {
	sent []sentMsg
	err  error
}
type sentMsg struct {
	plugin calls.PluginID
	msg    protocol.Message
}

func (o *outbox) Send(p calls.PluginID, m protocol.Message) error {
	if o.err != nil {
		return o.err
	}
	o.sent = append(o.sent, sentMsg{p, m})
	return nil
}

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

type focus struct{ f dispatcher.FocusContext }

func (f *focus) Focus() dispatcher.FocusContext { return f.f }

type rig struct {
	d     *dispatcher.Dispatcher
	table *calls.Table
	out   *outbox
	clk   *clock
	live  live
	foc   *focus
	logs  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{out: &outbox{}, clk: &clock{t: time.Unix(1000, 0)}, live: live{"notes": true, "other": true}, foc: &focus{dispatcher.FocusContext{Surface: "main-stage", Owner: "notes"}}}
	r.table = calls.New(r.out, calls.TableOptions{Clock: r.clk})
	cat := catalog{
		"notes.new":     {ID: "notes.new", Owner: "notes"},
		"notes.pub":     {ID: "notes.pub", Owner: "notes", Public: true},
		"other.thing":   {ID: "other.thing", Owner: "other"},
		"husk.quit":     {ID: "husk.quit", Owner: "husk"},
		"husk.pubbed":   {ID: "husk.pubbed", Owner: "husk", Public: true},
		"husk.nohandle": {ID: "husk.nohandle", Owner: "husk"},
		"gone.act":      {ID: "gone.act", Owner: "gone"},
	}
	r.d = mustNew(t, dispatcher.Options{
		Catalog: cat, Liveness: r.live, Focus: r.foc, Calls: r.table,
		Logger: func(f string, a ...any) { r.logs = append(r.logs, f) },
	})
	return r
}

// invoke runs one request and returns every reply it received.
func (r *rig) invoke(req dispatcher.ActionRequest) *[]dispatcher.ActionReply {
	var got []dispatcher.ActionReply
	req.Reply = func(a dispatcher.ActionReply) { got = append(got, a) }
	r.d.Invoke(req)
	return &got
}

func code(t *testing.T, got *[]dispatcher.ActionReply) string {
	t.Helper()
	if len(*got) != 1 {
		t.Fatalf("replies = %d, want exactly 1", len(*got))
	}
	if (*got)[0].Err == nil {
		t.Fatalf("want error, got result %v", (*got)[0].Result)
	}
	return (*got)[0].Err.Code
}

var user = dispatcher.Caller{Kind: dispatcher.CallerCLI}

func TestImmediateFailures(t *testing.T) {
	r := newRig(t)
	r.d.RegisterHost("husk.quit", func(dispatcher.ActionRequest) dispatcher.ActionReply {
		return dispatcher.ActionReply{Result: map[string]any{"ok": true}}
	})
	r.live["gone"] = false
	tests := []struct {
		name string
		req  dispatcher.ActionRequest
		want string
	}{
		{"empty id", dispatcher.ActionRequest{Caller: user}, "invalid_params"},
		{"no dot", dispatcher.ActionRequest{Action: "notes", Caller: user}, "invalid_params"},
		{"empty segment", dispatcher.ActionRequest{Action: "notes..new", Caller: user}, "invalid_params"},
		{"whitespace", dispatcher.ActionRequest{Action: "notes. new", Caller: user}, "invalid_params"},
		{"too long", dispatcher.ActionRequest{Action: "a." + strings.Repeat("b", 300), Caller: user}, "invalid_params"},
		{"unencodable args", dispatcher.ActionRequest{Action: "notes.new", Args: map[string]any{"x": make(chan int)}, Caller: user}, "invalid_params"},
		{"oversize args", dispatcher.ActionRequest{Action: "notes.new", Args: map[string]any{"x": strings.Repeat("a", dispatcher.MaxArgsBytes)}, Caller: user}, "invalid_params"},
		{"unknown action", dispatcher.ActionRequest{Action: "notes.nope", Caller: user}, "not_found"},
		{"unknown owner", dispatcher.ActionRequest{Action: "zzz.nope", Caller: user}, "not_found"},
		{"husk without handler", dispatcher.ActionRequest{Action: "husk.nohandle", Caller: user}, "not_found"},
		{"foreign private action", dispatcher.ActionRequest{Action: "notes.new", Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "other"}}, "denied"},
		{"plugin on host action", dispatcher.ActionRequest{Action: "husk.quit", Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "other"}}, "denied"},
		{"plugin on public host action without handler", dispatcher.ActionRequest{Action: "husk.pubbed", Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "other"}}, "not_found"},
		{"plugin without id", dispatcher.ActionRequest{Action: "notes.new", Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin}}, "denied"},
		{"unknown caller kind", dispatcher.ActionRequest{Action: "notes.new"}, "denied"},
		{"owner not live", dispatcher.ActionRequest{Action: "gone.act", Caller: user}, "unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(r.out.sent)
			got := r.invoke(tc.req)
			if c := code(t, got); c != tc.want {
				t.Fatalf("code = %s, want %s (%s)", c, tc.want, (*got)[0].Err.Message)
			}
			if len(r.out.sent) != before || r.d.Pending() != 0 {
				t.Fatal("a refused request reached Calls")
			}
		})
	}
}

func TestDeniedBeforeLiveness(t *testing.T) {
	r := newRig(t)
	r.live["notes"] = false
	got := r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "other"}})
	if c := code(t, got); c != "denied" {
		t.Fatalf("code = %s, want denied (not unavailable)", c)
	}
	if len(r.logs) != 1 {
		t.Fatalf("denial not logged: %v", r.logs)
	}
}

func TestAllowedCallers(t *testing.T) {
	plug := func(id string) dispatcher.Caller {
		return dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: id}
	}
	tests := []struct {
		name   string
		action string
		caller dispatcher.Caller
	}{
		{"input", "notes.new", dispatcher.Caller{Kind: dispatcher.CallerInput}},
		{"cli", "notes.new", user},
		{"host", "notes.new", dispatcher.Caller{Kind: dispatcher.CallerHost}},
		{"self", "notes.new", plug("notes")},
		{"foreign public", "notes.pub", plug("other")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			got := r.invoke(dispatcher.ActionRequest{Action: tc.action, Caller: tc.caller})
			if len(*got) != 0 || len(r.out.sent) != 1 || r.d.Pending() != 1 {
				t.Fatalf("replies=%d sent=%d pending=%d", len(*got), len(r.out.sent), r.d.Pending())
			}
			if r.out.sent[0].plugin != "notes" || r.out.sent[0].msg.Method != "action.invoke" {
				t.Fatalf("sent %+v", r.out.sent[0])
			}
		})
	}
}

type wire struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
	Caller map[string]any `json:"caller"`
	Focus  map[string]any `json:"focus"`
}

func sentParams(t *testing.T, r *rig) wire {
	t.Helper()
	var w wire
	if err := json.Unmarshal(r.out.sent[len(r.out.sent)-1].msg.Params, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestFocusSnapshotSource(t *testing.T) {
	mine := dispatcher.FocusContext{Surface: "input-view", Owner: "x"}
	tests := []struct {
		name   string
		caller dispatcher.Caller
		given  dispatcher.FocusContext
		want   map[string]any
	}{
		{"input keeps its own", dispatcher.Caller{Kind: dispatcher.CallerInput}, mine, map[string]any{"surface": "input-view", "owner": "x"}},
		{"cli gets state", user, mine, map[string]any{"surface": "main-stage", "owner": "notes"}},
		{"host gets state", dispatcher.Caller{Kind: dispatcher.CallerHost}, mine, map[string]any{"surface": "main-stage", "owner": "notes"}},
		{"plugin gets state", dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "notes"}, mine, map[string]any{"surface": "main-stage", "owner": "notes"}},
		{"cli with zero focus", user, dispatcher.FocusContext{}, map[string]any{"surface": "main-stage", "owner": "notes"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: tc.caller, Focus: tc.given})
			w := sentParams(t, r)
			if len(w.Focus) != len(tc.want) || w.Focus["surface"] != tc.want["surface"] || w.Focus["owner"] != tc.want["owner"] {
				t.Fatalf("focus = %v, want %v", w.Focus, tc.want)
			}
		})
	}
}

func TestFocusForHostHandlers(t *testing.T) {
	r := newRig(t)
	var seen dispatcher.FocusContext
	r.d.RegisterHost("husk.quit", func(req dispatcher.ActionRequest) dispatcher.ActionReply {
		seen = req.Focus
		return dispatcher.ActionReply{}
	})
	r.invoke(dispatcher.ActionRequest{Action: "husk.quit", Caller: user, Focus: dispatcher.FocusContext{Surface: "spoof"}})
	if seen.Surface != "main-stage" {
		t.Fatalf("host handler focus = %+v", seen)
	}
	r.invoke(dispatcher.ActionRequest{Action: "husk.quit", Caller: dispatcher.Caller{Kind: dispatcher.CallerInput}, Focus: dispatcher.FocusContext{Surface: "mine"}})
	if seen.Surface != "mine" {
		t.Fatalf("input focus replaced: %+v", seen)
	}
}

func TestWireParams(t *testing.T) {
	r := newRig(t)
	r.invoke(dispatcher.ActionRequest{Action: "notes.pub", Args: map[string]any{"title": "Todo"}, Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "other"}})
	w := sentParams(t, r)
	if w.Action != "notes.pub" || w.Args["title"] != "Todo" || w.Caller["kind"] != "plugin" || w.Caller["plugin_id"] != "other" {
		t.Fatalf("params = %+v", w)
	}
	r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: dispatcher.Caller{Kind: dispatcher.CallerInput}})
	w = sentParams(t, r)
	if w.Caller["kind"] != "input" || w.Caller["plugin_id"] != nil || w.Args != nil {
		t.Fatalf("params = %+v", w)
	}
}

func TestHostHandler(t *testing.T) {
	r := newRig(t)
	calls := 0
	r.d.RegisterHost("husk.quit", func(req dispatcher.ActionRequest) dispatcher.ActionReply {
		calls++
		return dispatcher.ActionReply{Result: map[string]any{"a": req.Args["a"]}}
	})
	got := r.invoke(dispatcher.ActionRequest{Action: "husk.quit", Args: map[string]any{"a": 1}, Caller: user})
	if calls != 1 || len(*got) != 1 || (*got)[0].Result["a"] != 1 {
		t.Fatalf("calls=%d got=%v", calls, *got)
	}
	if len(r.out.sent) != 0 {
		t.Fatal("host action reached Calls")
	}
	// A host handler runs even if "husk" is not live: liveness is never asked.
	r.d.RegisterHost("husk.quit", nil)
	if c := code(t, r.invoke(dispatcher.ActionRequest{Action: "husk.quit", Caller: user})); c != "not_found" {
		t.Fatalf("after unregister: %s", c)
	}
}

func TestReplyExactlyOnceHostHandlerCannotDouble(t *testing.T) {
	r := newRig(t)
	r.d.RegisterHost("husk.quit", func(dispatcher.ActionRequest) dispatcher.ActionReply { return dispatcher.ActionReply{} })
	got := r.invoke(dispatcher.ActionRequest{Action: "husk.quit", Caller: user})
	if len(*got) != 1 {
		t.Fatalf("replies = %d", len(*got))
	}
}

func resolve(t *testing.T, r *rig, wireID int, reply string) []calls.Result {
	t.Helper()
	var msg protocol.Message
	if err := json.Unmarshal([]byte(`{"id":`+strconv.Itoa(wireID)+`,`+reply[1:]), &msg); err != nil {
		t.Fatal(err)
	}
	res, ok := r.table.Resolve("notes", msg)
	if !ok {
		t.Fatal("reply not matched")
	}
	return []calls.Result{res}
}

func TestOwnerReplies(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		check func(t *testing.T, a dispatcher.ActionReply)
	}{
		{"object result", `{"result":{"path":"/n/todo.md","n":12345678901234567}}`, func(t *testing.T, a dispatcher.ActionReply) {
			if a.Err != nil || a.Result["path"] != "/n/todo.md" || a.Result["n"].(json.Number).String() != "12345678901234567" {
				t.Fatalf("%+v", a)
			}
		}},
		{"null result", `{"result":null}`, func(t *testing.T, a dispatcher.ActionReply) {
			if a.Err != nil || a.Result == nil || len(a.Result) != 0 {
				t.Fatalf("%+v", a)
			}
		}},
		{"non-object result", `{"result":[1]}`, func(t *testing.T, a dispatcher.ActionReply) {
			if a.Err == nil || a.Err.Code != "internal" || a.Result != nil {
				t.Fatalf("%+v", a)
			}
		}},
		{"owner error verbatim", `{"error":{"code":"failed","message":"disk full","data":{"why":"quota"}}}`, func(t *testing.T, a dispatcher.ActionReply) {
			if a.Err == nil || a.Err.Code != "failed" || a.Err.Message != "disk full" || a.Err.Data["why"] != "quota" || a.Result != nil {
				t.Fatalf("%+v", a)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			got := r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
			res := resolve(t, r, 1, tc.reply)
			if n := r.d.Complete(res...); n != 1 {
				t.Fatalf("delivered %d", n)
			}
			if len(*got) != 1 {
				t.Fatalf("replies = %d", len(*got))
			}
			tc.check(t, (*got)[0])
			if r.d.Complete(res...) != 0 || len(*got) != 1 || r.d.Pending() != 0 {
				t.Fatal("duplicate completion replied again")
			}
		})
	}
}

func TestTimeoutExitAndShutdown(t *testing.T) {
	r := newRig(t)
	a := r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
	r.clk.t = r.clk.t.Add(calls.DefaultTimeout + time.Second)
	if n := r.d.Complete(r.table.Expire(r.clk.t)...); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if c := code(t, a); c != "timeout" {
		t.Fatalf("code = %s", c)
	}

	b := r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
	r.d.Complete(r.table.DropPlugin("notes", "plugin_exited")...)
	if c := code(t, b); c != "cancelled" || (*b)[0].Err.Data["reason"] != "plugin_exited" {
		t.Fatalf("exit: %+v", (*b)[0].Err)
	}

	c1 := r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
	c2 := r.invoke(dispatcher.ActionRequest{Action: "other.thing", Caller: user})
	r.d.Complete(r.table.Close()...)
	if code(t, c1) != "cancelled" || code(t, c2) != "cancelled" || r.d.Pending() != 0 {
		t.Fatal("shutdown left requests unanswered")
	}
}

func TestForeignResultsIgnored(t *testing.T) {
	r := newRig(t)
	got := r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
	h := calls.Handle{Plugin: "notes", ID: 1}
	if r.d.Complete(calls.Result{Handle: h, Requester: "hooks", Value: json.RawMessage(`{}`)}) != 0 {
		t.Fatal("another requester's result consumed")
	}
	if r.d.Complete(calls.Result{Handle: calls.Handle{Plugin: "notes", ID: 99}, Requester: dispatcher.Requester}) != 0 {
		t.Fatal("unknown handle consumed")
	}
	if len(*got) != 0 || r.d.Pending() != 1 {
		t.Fatal("pending request disturbed")
	}
}

func TestCallsFailuresBecomeCodes(t *testing.T) {
	t.Run("outbox failure", func(t *testing.T) {
		r := newRig(t)
		r.out.err = errors.New("pipe closed")
		if c := code(t, r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})); c != "unavailable" {
			t.Fatal(c)
		}
		if r.d.Pending() != 0 {
			t.Fatal("pending after failed send")
		}
	})
	t.Run("per-plugin cap", func(t *testing.T) {
		r := newRig(t)
		r.table = calls.New(r.out, calls.TableOptions{Clock: r.clk, MaxPendingPerPlugin: 1})
		r.d = mustNew(t, dispatcher.Options{Catalog: catalog{"notes.new": {ID: "notes.new", Owner: "notes"}}, Liveness: r.live, Focus: r.foc, Calls: r.table})
		r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
		if c := code(t, r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})); c != "busy" {
			t.Fatal(c)
		}
	})
	t.Run("overall cap", func(t *testing.T) {
		r := newRig(t)
		r.table = calls.New(r.out, calls.TableOptions{Clock: r.clk, MaxPending: 1})
		r.d = mustNew(t, dispatcher.Options{Catalog: catalog{"notes.new": {ID: "notes.new", Owner: "notes"}}, Liveness: r.live, Focus: r.foc, Calls: r.table})
		r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
		if c := code(t, r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})); c != "busy" {
			t.Fatal(c)
		}
	})
	t.Run("closed table", func(t *testing.T) {
		r := newRig(t)
		r.table.Close()
		if c := code(t, r.invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})); c != "unavailable" {
			t.Fatal(c)
		}
	})
}

func TestOrderPerOwner(t *testing.T) {
	r := newRig(t)
	for _, a := range []string{"notes.new", "notes.pub", "notes.new"} {
		r.invoke(dispatcher.ActionRequest{Action: a, Caller: user})
	}
	var ids []string
	var acts []string
	for _, s := range r.out.sent {
		ids = append(ids, s.msg.ID.String())
		var w wire
		_ = json.Unmarshal(s.msg.Params, &w)
		acts = append(acts, w.Action)
	}
	if strings.Join(ids, ",") != "1,2,3" || strings.Join(acts, ",") != "notes.new,notes.pub,notes.new" {
		t.Fatalf("ids=%v acts=%v", ids, acts)
	}
}

func TestNilReplyIsHarmless(t *testing.T) {
	r := newRig(t)
	r.d.Invoke(dispatcher.ActionRequest{Action: "nope.x", Caller: user})
	r.d.Invoke(dispatcher.ActionRequest{Action: "notes.new", Caller: user})
	r.d.Complete(r.table.Close()...)
}

func mustNew(t *testing.T, o dispatcher.Options) *dispatcher.Dispatcher {
	t.Helper()
	d, err := dispatcher.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNewValidatesOptions(t *testing.T) {
	r := newRig(t)
	ok := dispatcher.Options{Catalog: catalog{}, Liveness: r.live, Focus: r.foc, Calls: r.table}
	if _, err := dispatcher.New(dispatcher.Options{}); err == nil {
		t.Fatal("missing views accepted")
	}
	for _, tc := range []struct {
		timeout time.Duration
		valid   bool
	}{
		{0, true}, {calls.MinTimeout, true}, {calls.MaxTimeout, true}, {30 * time.Second, true},
		{-time.Second, false}, {time.Millisecond, false}, {calls.MinTimeout - 1, false}, {calls.MaxTimeout + 1, false},
	} {
		o := ok
		o.Timeout = tc.timeout
		if _, err := dispatcher.New(o); (err == nil) != tc.valid {
			t.Errorf("timeout %s: err = %v", tc.timeout, err)
		}
	}
}

func TestHostHandlerPanicRepliesOnce(t *testing.T) {
	r := newRig(t)
	r.d.RegisterHost("husk.quit", func(dispatcher.ActionRequest) dispatcher.ActionReply { panic("boom") })
	got := r.invoke(dispatcher.ActionRequest{Action: "husk.quit", Caller: user})
	if c := code(t, got); c != "internal" {
		t.Fatalf("code = %s", c)
	}
	if len(r.logs) != 1 {
		t.Fatalf("panic not logged: %v", r.logs)
	}
}

func TestCallerKindString(t *testing.T) {
	for k, want := range map[dispatcher.CallerKind]string{dispatcher.CallerInput: "input", dispatcher.CallerPlugin: "plugin", dispatcher.CallerCLI: "cli", dispatcher.CallerHost: "host", 0: "unknown"} {
		if k.String() != want {
			t.Errorf("%d = %s", k, k.String())
		}
	}
}

func TestStateViews(t *testing.T) {
	s := state.New(state.Options{})
	reg := func(ns, owner string) *state.Handle {
		h, err := s.Register(state.Spec{Namespace: ns, Owner: owner, Scope: state.ScopeInstance})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	lv, fv := dispatcher.StateLiveness(s), dispatcher.StateFocus(s)
	if lv.Live("notes") || fv.Focus() != (dispatcher.FocusContext{}) {
		t.Fatal("empty state reports live or focused")
	}
	pods, focusH, surf := reg(state.PodsNamespace, "husk"), reg(state.FocusNamespace, "husk"), reg(state.SurfacesNamespace, "husk")
	if lv.Live("notes") || fv.Focus() != (dispatcher.FocusContext{}) {
		t.Fatal("empty slices report live or focused")
	}
	set := func(h *state.Handle, v string) {
		if _, err := h.Set(json.RawMessage(v)); err != nil {
			t.Fatal(err)
		}
	}
	set(pods, `{"plugins":{"notes":{"status":"live"},"starting":{"status":"starting"},"dead":{"status":"exited"}}}`)
	for id, want := range map[string]bool{"notes": true, "starting": false, "dead": false, "absent": false} {
		if lv.Live(id) != want {
			t.Errorf("Live(%s) = %v", id, !want)
		}
	}
	set(focusH, `{"surface":"main-stage","fallback":null,"terminal_focused":true}`)
	set(surf, `{"occupants":{"main-stage":"notes"}}`)
	if f := fv.Focus(); f.Surface != "main-stage" || f.Owner != "notes" {
		t.Fatalf("focus = %+v", f)
	}
	set(focusH, `{"surface":"activity-rail"}`)
	if f := fv.Focus(); f.Surface != "activity-rail" || f.Owner != "" {
		t.Fatalf("host-occupied focus = %+v", f)
	}
	set(focusH, `{"surface":null}`)
	if fv.Focus() != (dispatcher.FocusContext{}) {
		t.Fatal("null surface")
	}
}
