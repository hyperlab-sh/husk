package hooks

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// stubSender wraps a real table and fails CanSend or Send for chosen plugins,
// to reach refusals the table only produces under real pressure.
type stubSender struct {
	*calls.Table
	canErr  map[string]error
	sendErr map[string]error
}

func (s *stubSender) CanSend(plugins ...calls.PluginID) error {
	for _, p := range plugins {
		if err := s.canErr[string(p)]; err != nil {
			return err
		}
	}
	return s.Table.CanSend(plugins...)
}

func (s *stubSender) Send(p calls.PluginID, m string, params json.RawMessage, o calls.Options) (calls.Handle, error) {
	if err := s.sendErr[string(p)]; err != nil {
		return calls.Handle{}, err
	}
	return s.Table.Send(p, m, params, o)
}

// chainRig fires a:t, a transform owned by "a", over live subscribers subs in
// the given priority order, with the owner's data data.
func chainRig(t *testing.T, data map[string]any, subs ...string) (*rig, *collector, *stubSender) {
	t.Helper()
	live := fakeLiveness{}
	for _, s := range subs {
		live[s] = true
	}
	g := newRig(t, (&fakeCatalog{}).declare("a.t", "a", Transform, subs...), live, calls.TableOptions{})
	snd := &stubSender{Table: g.table, canErr: map[string]error{}, sendErr: map[string]error{}}
	g.r.opts.Calls = snd
	return g, &collector{}, snd
}

// legData is the data member of the latest leg sent to plugin.
func (g *rig) legData(plugin string) string {
	g.t.Helper()
	var p struct {
		Hook string          `json:"hook"`
		Kind Kind            `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(g.legTo(plugin).msg.Params, &p); err != nil {
		g.t.Fatal(err)
	}
	if p.Hook != "a.t" || p.Kind != Transform {
		g.t.Fatalf("leg params %s", g.legTo(plugin).msg.Params)
	}
	return string(p.Data)
}

// sentTo lists the plugins that have received a leg, in send order.
func (g *rig) sentTo() []string {
	var out []string
	for _, s := range g.box.sent {
		out = append(out, string(s.plugin))
	}
	return out
}

func TestTransformStagesRunStrictlyOneAtATime(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2", "p3")
	g.fire("a.t", map[string]any{"v": 0}, col)
	for i, p := range []string{"p1", "p2", "p3"} {
		if len(g.box.sent) != i+1 || g.r.Pending() != 1 || g.table.Stats().Pending != 1 || col.n != 0 {
			t.Fatalf("before %s answers: sent=%d pending=%d delivered=%d; want exactly one pending leg", p, len(g.box.sent), g.r.Pending(), col.n)
		}
		if got := string(g.box.sent[i].plugin); got != p {
			t.Fatalf("stage %d went to %s, want %s", i+1, got, p)
		}
		g.answer(p, `{}`)
	}
	if col.n != 1 || len(g.box.sent) != 3 || g.r.Pending() != 0 {
		t.Fatalf("delivered=%d sent=%d pending=%d", col.n, len(g.box.sent), g.r.Pending())
	}
}

func TestTransformOrderIsTheCatalogsPriorityThenID(t *testing.T) {
	cat := (&fakeCatalog{}).declare("a.t", "a", Transform)
	// Sorted the way a real Catalog returns them: priority, then plugin id.
	cat.subs["a.t"] = []Subscription{{"zed", 1}, {"alpha", 5}, {"beta", 5}, {"mid", 7}}
	g := newRig(t, cat, fakeLiveness{"zed": true, "alpha": true, "beta": true, "mid": true}, calls.TableOptions{})
	col := &collector{}
	g.fire("a.t", nil, col)
	for _, p := range []string{"zed", "alpha", "beta", "mid"} {
		g.answer(p, `{}`)
	}
	if want := []string{"zed", "alpha", "beta", "mid"}; !reflect.DeepEqual(g.sentTo(), want) {
		t.Fatalf("send order %v, want %v", g.sentTo(), want)
	}
	var got []string
	for _, e := range col.out.Results {
		got = append(got, e.Plugin)
	}
	if want := []string{"zed", "alpha", "beta", "mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("results %v, want %v", got, want)
	}
}

func TestTransformSkipsSubscribersThatAreNotLive(t *testing.T) {
	g := newRig(t, (&fakeCatalog{}).declare("a.t", "a", Transform, "p1", "dead", "p3"), fakeLiveness{"p1": true, "p3": true}, calls.TableOptions{})
	col := &collector{}
	g.fire("a.t", nil, col)
	g.answer("p1", `{}`)
	g.answer("p3", `{}`)
	if want := []string{"p1", "p3"}; !reflect.DeepEqual(g.sentTo(), want) || len(col.out.Results) != 2 {
		t.Fatalf("sent %v, %d entries", g.sentTo(), len(col.out.Results))
	}
}

func TestTransformReplaceAndStageInputs(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2", "p3")
	g.fire("a.t", map[string]any{"owner": true}, col)
	if got := g.legData("p1"); got != `{"owner":true}` {
		t.Fatalf("stage 1 data %s", got)
	}
	g.answer("p1", `{"data":{"n":1.0,"s":"x"}}`)
	if got := g.legData("p2"); got != `{"n":1.0,"s":"x"}` {
		t.Fatalf("stage 2 must receive stage 1's replacement (number lexeme kept), got %s", got)
	}
	g.answer("p2", `{}`) // pass-through
	if got := g.legData("p3"); got != `{"n":1.0,"s":"x"}` {
		t.Fatalf("stage 3 data %s", got)
	}
	g.answer("p3", `{"data":{}}`) // an empty object is a replacement
	o := col.out
	if o.Kind != Transform || !o.Allowed || o.Vetoes != nil {
		t.Fatalf("outcome %#v", o)
	}
	if o.Data == nil || len(o.Data) != 0 {
		t.Fatalf("outcome data %#v, want the empty replacement", o.Data)
	}
	if want := map[string]any{"n": json.Number("1.0"), "s": "x"}; !reflect.DeepEqual(o.Results[0].Data, want) {
		t.Fatalf("stage 1 entry %#v", o.Results[0].Data)
	}
	if o.Results[1].Data != nil || o.Results[1].Err != nil {
		t.Fatalf("pass-through entry %#v", o.Results[1])
	}
	if e := o.Results[2]; e.Data == nil || len(e.Data) != 0 || e.Err != nil {
		t.Fatalf("{} replacement must carry a non-nil empty Data: %#v", e)
	}
}

func TestTransformPassThroughSpellings(t *testing.T) {
	for _, reply := range []string{`{}`, `null`, `{"data":null}`, `{"other":1}`} {
		t.Run(reply, func(t *testing.T) {
			g, col, _ := chainRig(t, nil, "p1", "p2")
			g.fire("a.t", map[string]any{"v": 1}, col)
			g.answer("p1", reply)
			if got := g.legData("p2"); got != `{"v":1}` {
				t.Fatalf("stage 2 data %s", got)
			}
			g.answer("p2", reply)
			if e := col.out.Results[0]; e.Data != nil || e.Err != nil {
				t.Fatalf("entry %#v", e)
			}
			if !reflect.DeepEqual(col.out.Data, map[string]any{"v": 1}) {
				t.Fatalf("outcome data %#v", col.out.Data)
			}
		})
	}
}

func TestTransformOutcomeDataIsEmptyObjectForNilInput(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1")
	g.fire("a.t", nil, col)
	if got := g.legData("p1"); got != `{}` {
		t.Fatalf("data %s", got)
	}
	g.answer("p1", `{}`)
	if col.out.Data == nil || len(col.out.Data) != 0 {
		t.Fatalf("outcome data %#v, want {}", col.out.Data)
	}
}

func TestTransformFailedStageIsSkippedWithDataUnchanged(t *testing.T) {
	const orig = `{"v":1}`
	for _, tc := range []struct {
		name string
		fail func(g *rig, snd *stubSender)
		code protocol.Code
		data map[string]any // the entry's error data
	}{
		{"error reply", func(g *rig, _ *stubSender) {
			g.fail("p1", &protocol.Error{Code: protocol.CodeFailed, Message: "no", Data: json.RawMessage(`{"k":1}`)})
		}, protocol.CodeFailed, map[string]any{"k": json.Number("1")}},
		{"timeout", func(g *rig, _ *stubSender) {
			g.clock.advance(calls.DefaultTimeout)
			if n := g.r.Complete(g.table.Expire(g.clock.now)...); n != 1 {
				g.t.Fatalf("expiry resolved %d legs", n)
			}
		}, protocol.CodeTimeout, nil},
		{"plugin exit", func(g *rig, _ *stubSender) {
			if n := g.r.Complete(g.table.DropPlugin("p1", "exit status 1")...); n != 1 {
				g.t.Fatalf("drop resolved %d legs", n)
			}
		}, protocol.CodeCancelled, map[string]any{"reason": "plugin_exited"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, col, snd := chainRig(t, nil, "p1", "p2")
			g.fire("a.t", map[string]any{"v": 1}, col)
			tc.fail(g, snd)
			if got := g.legData("p2"); got != orig {
				t.Fatalf("stage 2 received %s, want the unchanged %s", got, orig)
			}
			if len(g.box.sent) != 2 || g.r.Pending() != 1 {
				t.Fatalf("sent=%d pending=%d", len(g.box.sent), g.r.Pending())
			}
			g.answer("p2", `{}`)
			e := col.out.Results[0]
			entryErr(t, e, tc.code)
			if e.Data != nil || !reflect.DeepEqual(e.Err.Data, tc.data) {
				t.Fatalf("entry %#v, error data %v", e, e.Err.Data)
			}
			if !reflect.DeepEqual(col.out.Data, map[string]any{"v": 1}) || col.n != 1 || !col.out.Allowed {
				t.Fatalf("outcome %#v", col.out)
			}
		})
	}
}

func TestTransformSendFailureSkipsTheStageAtOnce(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2", "p3")
	g.box.errFor = map[calls.PluginID]error{"p1": errors.New("writer closed")}
	g.fire("a.t", map[string]any{"v": 1}, col)
	if want := []string{"p2"}; !reflect.DeepEqual(g.sentTo(), want) {
		t.Fatalf("sent %v: the stage after a failed send must go out in the same call", g.sentTo())
	}
	if got := g.legData("p2"); got != `{"v":1}` {
		t.Fatalf("data %s", got)
	}
	g.answer("p2", `{"data":{"v":2}}`)
	g.answer("p3", `{}`)
	e := col.out.Results[0]
	if e.Plugin != "p1" || e.Err == nil || e.Err.Code != CodeUnavailable || e.Err.Data["reason"] != "disconnected" {
		t.Fatalf("send failure entry %#v", e)
	}
	if !reflect.DeepEqual(col.out.Data, map[string]any{"v": json.Number("2")}) {
		t.Fatalf("outcome data %#v", col.out.Data)
	}
}

func TestTransformEveryStageUndeliverableCompletesInsideCall(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2")
	g.box.errFor = map[calls.PluginID]error{"p1": errors.New("x"), "p2": errors.New("y")}
	g.fire("a.t", map[string]any{"v": 1}, col)
	if col.n != 1 || len(col.out.Results) != 2 || !reflect.DeepEqual(col.out.Data, map[string]any{"v": 1}) {
		t.Fatalf("delivered %d: %#v", col.n, col.out)
	}
	if len(g.r.inFlight) != 0 || g.r.Pending() != 0 {
		t.Fatalf("leaked: inFlight=%v pending=%d", g.r.inFlight, g.r.Pending())
	}
}

func TestTransformPerStageCapHitIsThatStagesBusyEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		msg  string
	}{
		{"per-plugin cap", calls.ErrPluginBusy, "too many pending hook calls for one subscriber"},
		{"overall cap", calls.ErrBusy, "too many pending hook calls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, col, snd := chainRig(t, nil, "p1", "p2", "p3")
			g.fire("a.t", map[string]any{"v": 1}, col)
			snd.canErr["p2"] = tc.err
			g.answer("p1", `{"data":{"v":2}}`)
			if want := []string{"p1", "p3"}; !reflect.DeepEqual(g.sentTo(), want) {
				t.Fatalf("sent %v: a busy stage must be skipped and the chain go on", g.sentTo())
			}
			if got := g.legData("p3"); got != `{"v":2}` {
				t.Fatalf("stage 3 data %s", got)
			}
			g.answer("p3", `{}`)
			e := col.out.Results[1]
			if e.Plugin != "p2" || e.Err == nil || e.Err.Code != CodeBusy || e.Err.Message != tc.msg || e.Data != nil {
				t.Fatalf("busy entry %#v", e)
			}
			if col.n != 1 || len(col.out.Results) != 3 {
				t.Fatalf("delivered %d with %d entries", col.n, len(col.out.Results))
			}
		})
	}
}

func TestTransformFirstStageCapHitRefusesTheWholeCall(t *testing.T) {
	g, col, snd := chainRig(t, nil, "p1", "p2")
	snd.canErr["p1"] = calls.ErrPluginBusy
	wantCode(t, g.r.Call(Caller{Kind: CallerPlugin, PluginID: "a"}, "a.t", nil, col.onDone), protocol.CodeBusy)
	if col.n != 0 || len(g.box.sent) != 0 || len(g.r.inFlight) != 0 {
		t.Fatal("refused call leaked work")
	}
	// A later stage's cap does not matter to the preflight.
	delete(snd.canErr, "p1")
	snd.canErr["p2"] = calls.ErrPluginBusy
	g.fire("a.t", nil, col)
}

func TestTransformInvalidReplacementIsThatStagesEntryAndDataPassesThrough(t *testing.T) {
	big := `{"data":{"pad":"` + strings.Repeat("x", MaxPayload) + `"}}`
	for _, tc := range []struct{ name, reply string }{
		{"array", `{"data":[1]}`},
		{"string", `{"data":"x"}`},
		{"number", `{"data":3}`},
		{"boolean", `{"data":true}`},
		{"oversized object", big},
		{"non-object result", `[1]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, col, _ := chainRig(t, nil, "p1", "p2")
			g.fire("a.t", map[string]any{"v": 1}, col)
			g.answer("p1", tc.reply)
			if got := g.legData("p2"); got != `{"v":1}` {
				t.Fatalf("stage 2 received %s", got)
			}
			g.answer("p2", `{}`)
			e := col.out.Results[0]
			entryErr(t, e, protocol.CodeInvalidParams)
			if e.Data != nil || !reflect.DeepEqual(col.out.Data, map[string]any{"v": 1}) {
				t.Fatalf("entry %#v, outcome data %#v", e, col.out.Data)
			}
		})
	}
}

func TestTransformReplyOfExactlyOneMiBPasses(t *testing.T) {
	const frame = `{"data":{"p":""}}`
	g, col, _ := chainRig(t, nil, "p1")
	g.fire("a.t", nil, col)
	g.answer("p1", `{"data":{"p":"`+strings.Repeat("x", MaxPayload-len(frame))+`"}}`)
	if e := col.out.Results[0]; e.Err != nil || len(e.Data) != 1 {
		t.Fatalf("1 MiB reply refused: %#v", e)
	}
}

func TestTransformCloseMidChainEndsItWithAShutdownEntry(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2", "p3")
	g.fire("a.t", map[string]any{"v": 1}, col)
	g.answer("p1", `{"data":{"v":2}}`)
	if n := g.r.Complete(g.table.Close()...); n != 1 {
		t.Fatalf("close resolved %d legs", n)
	}
	if col.n != 1 || !col.out.Allowed {
		t.Fatalf("delivered %d: %#v", col.n, col.out)
	}
	if want := []string{"p1", "p2"}; !reflect.DeepEqual(g.sentTo(), want) {
		t.Fatalf("sent %v: no stage may follow a shutdown", g.sentTo())
	}
	if len(col.out.Results) != 2 || !isShutdown(col.out.Results[1].Err) || col.out.Results[1].Plugin != "p2" {
		t.Fatalf("results %#v", col.out.Results)
	}
	if !reflect.DeepEqual(col.out.Data, map[string]any{"v": json.Number("2")}) {
		t.Fatalf("outcome data %#v, want the data after stage 1", col.out.Data)
	}
	if len(g.r.inFlight) != 0 || g.r.Pending() != 0 {
		t.Fatal("chain leaked")
	}
}

func TestTransformClosedTableBeforeAStageEndsTheChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*stubSender)
	}{
		{"CanSend", func(s *stubSender) { s.canErr["p2"] = calls.ErrClosed }},
		{"Send", func(s *stubSender) { s.sendErr["p2"] = calls.ErrClosed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, col, snd := chainRig(t, nil, "p1", "p2", "p3")
			g.fire("a.t", map[string]any{"v": 1}, col)
			tc.set(snd)
			g.answer("p1", `{"data":{"v":2}}`)
			if want := []string{"p1"}; !reflect.DeepEqual(g.sentTo(), want) {
				t.Fatalf("sent %v", g.sentTo())
			}
			if col.n != 1 || len(col.out.Results) != 2 {
				t.Fatalf("delivered %d: %#v", col.n, col.out.Results)
			}
			e := col.out.Results[1]
			if e.Plugin != "p2" || !isShutdown(e.Err) {
				t.Fatalf("entry %#v, want cancelled/shutdown", e)
			}
			if !reflect.DeepEqual(col.out.Data, map[string]any{"v": json.Number("2")}) || len(g.r.inFlight) != 0 {
				t.Fatalf("outcome %#v inFlight %v", col.out.Data, g.r.inFlight)
			}
		})
	}
}

func TestTransformVetoFieldIsIgnoredAndLoggedOncePerSubscriber(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2")
	for i := 0; i < 3; i++ {
		col = &collector{}
		g.fire("a.t", map[string]any{"v": i}, col)
		g.answer("p1", `{"veto":true,"reason":"no"}`)
		g.answer("p2", `{"veto":true,"data":{"v":9}}`)
		if !col.out.Allowed || len(col.out.Vetoes) != 0 {
			t.Fatalf("a transform cannot veto: %#v", col.out)
		}
		for _, e := range col.out.Results {
			if e.Veto || e.Reason != "" || e.Err != nil {
				t.Fatalf("entry %#v", e)
			}
		}
	}
	if len(g.logs) != 2 { // p1 and p2, once each
		t.Fatalf("want 2 log lines, got %d: %q", len(g.logs), g.logs)
	}
	for _, line := range g.logs {
		if !strings.Contains(line, "veto") {
			t.Fatalf("log line %q does not name the field", line)
		}
	}
}

func TestTransformIgnoresItemsAndReasonToo(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1")
	g.fire("a.t", nil, col)
	g.answer("p1", `{"items":[1],"reason":"x"}`)
	if e := col.out.Results[0]; e.Err != nil || e.Items != nil || e.Reason != "" || e.Data != nil {
		t.Fatalf("entry %#v", e)
	}
	if len(g.logs) != 1 {
		t.Fatalf("logs %q", g.logs)
	}
}

func TestTransformInFlightCountFollowsTheChain(t *testing.T) {
	g, col, _ := chainRig(t, nil, "p1", "p2")
	g.fire("a.t", nil, col)
	if g.r.inFlight["a.t"] != 1 {
		t.Fatalf("in flight %d during the chain", g.r.inFlight["a.t"])
	}
	g.answer("p1", `{}`)
	if g.r.inFlight["a.t"] != 1 {
		t.Fatalf("in flight %d between stages", g.r.inFlight["a.t"])
	}
	g.answer("p2", `{}`)
	if _, present := g.r.inFlight["a.t"]; present || col.n != 1 {
		t.Fatalf("in flight %v after the outcome", g.r.inFlight)
	}
}

func TestTransformChainContinuesWhenTheOwnerDroppedItsContinuation(t *testing.T) {
	g, _, _ := chainRig(t, nil, "p1", "p2")
	if err := g.r.Call(Caller{Kind: CallerPlugin, PluginID: "a"}, "a.t", nil, nil); err != nil {
		t.Fatal(err)
	}
	g.answer("p1", `{}`)
	g.answer("p2", `{}`)
	if len(g.r.inFlight) != 0 || len(g.box.sent) != 2 {
		t.Fatalf("inFlight=%v sent=%d", g.r.inFlight, len(g.box.sent))
	}
}

func TestTransformInFlightCapCountsChains(t *testing.T) {
	g, _, _ := chainRig(t, nil, "p1")
	g.r.limit = 1
	a := Caller{Kind: CallerPlugin, PluginID: "a"}
	if err := g.r.Call(a, "a.t", nil, nil); err != nil {
		t.Fatal(err)
	}
	wantCode(t, g.r.Call(a, "a.t", nil, nil), protocol.CodeBusy)
	g.answer("p1", `{}`)
	if err := g.r.Call(a, "a.t", nil, nil); err != nil {
		t.Fatalf("after the chain finished: %v", err)
	}
}
