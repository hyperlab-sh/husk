package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// frozenClock never advances and never fires a timer: a test that passes on
// it did not wait for a deadline.
type frozenClock struct{ now time.Time }

type neverTimer struct{}

func (neverTimer) Stop() bool { return true }

func (c frozenClock) Now() time.Time { return c.now }
func (frozenClock) AfterFunc(time.Duration, func()) protocol.Timer {
	return neverTimer{}
}

// hookPlugin writes a v2 manifest declaring the given hooks.publish entries
// and hooks.subscribe entries (strings or {id, priority} objects).
func hookPlugin(t *testing.T, id string, publish, subscribe []any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"manifest_version": 2, "protocol": 2, "id": id, "name": id, "version": "1",
		"command": []string{"./plugin"},
		"hooks":   map[string]any{"publish": publish, "subscribe": subscribe},
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pubHook(id, kind string) map[string]any {
	return map[string]any{"id": id, "kind": kind, "title": id}
}

func subHook(id string, priority int) map[string]any {
	return map[string]any{"id": id, "priority": priority}
}

// legParams is what a subscriber receives for a hook leg.
type legParams struct {
	Hook string         `json:"hook"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
}

// responders answer hook legs for fake plugins: the returned value is the
// reply's result. A plugin without a responder never replies.
type responders map[string]func(legParams) any

// install makes f answer every hook.call leg through the plugin's own Reply
// sink, as the Pod's stdout reader would.
func (r responders) install(f *fakes) {
	f.onSend = func(p *fakeProc, m protocol.Message) {
		if m.Method != protocol.MethodHookCall || m.ID == nil {
			return
		}
		fn := r[p.id]
		if fn == nil {
			return
		}
		var lp legParams
		if err := json.Unmarshal(m.Params, &lp); err != nil {
			return
		}
		res, _ := json.Marshal(fn(lp))
		p.opts.Replies.Reply(p.id, protocol.Message{ID: m.ID, Result: res})
	}
}

// rpc sends one request over a fresh socket connection and returns the reply.
// A non-empty token authenticates as that plugin; empty is a CLI caller.
func rpc(t *testing.T, h *harness, token, method string, params any) protocol.Message {
	t.Helper()
	c, err := net.Dial("unix", instance.SocketPath(h.dir, h.id))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := protocol.NewStringID("1")
	raw, _ := json.Marshal(params)
	line, _ := json.Marshal(protocol.Message{ID: &id, Method: method, Params: raw, Token: token})
	if _, err := c.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	out, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	return m
}

// hookHost boots a host with the given plugin directories on a frozen clock
// and waits until want plugins have started and every one is live.
func hookHost(t *testing.T, f *fakes, dirs []string, want int) *harness {
	t.Helper()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		pluginSettings(c)["paths"] = dirs
		c.Clock = frozenClock{time.Unix(1000, 0)}
	}, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	var ids []string
	for range want {
		ids = append(ids, <-f.started)
	}
	for _, id := range ids {
		if got := h.podStatus(id); got != "live" {
			t.Fatalf("%s status = %q", id, got)
		}
	}
	return h
}

type outcomeWire struct {
	Kind    string         `json:"kind"`
	Data    map[string]any `json:"data"`
	Allowed *bool          `json:"allowed"`
	Vetoes  []any          `json:"vetoes"`
	Results []struct {
		Plugin string         `json:"plugin_id"`
		Data   map[string]any `json:"data"`
		Items  []any          `json:"items"`
		Error  *protocol.Error
	} `json:"results"`
}

func (o outcomeWire) plugins() []string {
	var out []string
	for _, r := range o.Results {
		out = append(out, r.Plugin)
	}
	return out
}

func decodeOutcome(t *testing.T, m protocol.Message) outcomeWire {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("error reply: %+v", m.Error)
	}
	var o outcomeWire
	if err := json.Unmarshal(m.Result, &o); err != nil {
		t.Fatalf("%s: %v", m.Result, err)
	}
	return o
}

func TestHookOwnerFiresItsGateOverTheSocket(t *testing.T) {
	f := newFakes()
	responders{"sub": func(legParams) any { return map[string]any{} }}.install(f)
	h := hookHost(t, f, []string{
		hookPlugin(t, "notes", []any{pubHook("notes.gate", "gate")}, nil),
		hookPlugin(t, "sub", nil, []any{"notes.gate"}),
		hookPlugin(t, "other", nil, nil),
	}, 3)
	fire := map[string]any{"hook": "notes.gate", "data": map[string]any{"path": "/a"}}

	o := decodeOutcome(t, rpc(t, h, "tok-notes", protocol.MethodHookCall, fire))
	if o.Kind != "gate" || o.Allowed == nil || !*o.Allowed || !slices.Equal(o.plugins(), []string{"sub"}) {
		t.Fatalf("owner outcome = %+v", o)
	}
	for name, token := range map[string]string{"another plugin": "tok-other", "a tokenless CLI request": ""} {
		m := rpc(t, h, token, protocol.MethodHookCall, fire)
		if m.Error == nil || m.Error.Code != protocol.CodeDenied {
			t.Fatalf("%s: reply = %+v, want denied", name, m)
		}
	}
	// Not even a hook the host owns can be fired by a plugin.
	m := rpc(t, h, "tok-notes", protocol.MethodHookCall, map[string]any{"hook": "husk.before-state-clear"})
	if m.Error == nil || m.Error.Code != protocol.CodeDenied {
		t.Fatalf("plugin firing a host hook: %+v", m)
	}
}

func TestHookTransformRunsStagesInPriorityOrder(t *testing.T) {
	f := newFakes()
	stage := func(id string) func(legParams) any {
		return func(lp legParams) any {
			trail, _ := lp.Data["trail"].(string)
			return map[string]any{"data": map[string]any{"trail": trail + id + ","}}
		}
	}
	responders{"alpha": stage("alpha"), "zeta": stage("zeta")}.install(f)
	h := hookHost(t, f, []string{
		hookPlugin(t, "notes", []any{pubHook("notes.tr", "transform")}, nil),
		hookPlugin(t, "alpha", nil, []any{subHook("notes.tr", 10)}),
		hookPlugin(t, "zeta", nil, []any{subHook("notes.tr", 5)}),
	}, 3)
	o := decodeOutcome(t, rpc(t, h, "tok-notes", protocol.MethodHookCall, map[string]any{"hook": "notes.tr", "data": map[string]any{"trail": ""}}))
	if o.Kind != "transform" || o.Data["trail"] != "zeta,alpha," {
		t.Fatalf("outcome = %+v", o)
	}
	if !slices.Equal(o.plugins(), []string{"zeta", "alpha"}) {
		t.Fatalf("result order = %v", o.plugins())
	}
}

func TestHookCollectListsItemsInPriorityOrder(t *testing.T) {
	f := newFakes()
	items := func(id string) func(legParams) any {
		return func(legParams) any { return map[string]any{"items": []any{map[string]any{"from": id}}} }
	}
	responders{"alpha": items("alpha"), "zeta": items("zeta")}.install(f)
	h := hookHost(t, f, []string{
		hookPlugin(t, "notes", []any{pubHook("notes.col", "collect")}, nil),
		hookPlugin(t, "alpha", nil, []any{subHook("notes.col", 10)}),
		hookPlugin(t, "zeta", nil, []any{subHook("notes.col", 5)}),
	}, 3)
	o := decodeOutcome(t, rpc(t, h, "tok-notes", protocol.MethodHookCall, map[string]any{"hook": "notes.col"}))
	if o.Kind != "collect" || !slices.Equal(o.plugins(), []string{"zeta", "alpha"}) {
		t.Fatalf("outcome = %+v", o)
	}
	for i, from := range []string{"zeta", "alpha"} {
		got := o.Results[i].Items
		if len(got) != 1 || got[0].(map[string]any)["from"] != from {
			t.Fatalf("results[%d].items = %v", i, got)
		}
	}
}

type listWire struct {
	Hooks []struct {
		ID          string `json:"id"`
		Owner       string `json:"owner"`
		Kind        string `json:"kind"`
		TimeoutMS   int    `json:"timeout_ms"`
		Title       string `json:"title"`
		Subscribers []struct {
			Plugin   string `json:"plugin_id"`
			Priority int    `json:"priority"`
			Live     bool   `json:"live"`
		} `json:"subscribers"`
	} `json:"hooks"`
}

func decodeList(t *testing.T, m protocol.Message) listWire {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("hook.list: %+v", m.Error)
	}
	var l listWire
	if err := json.Unmarshal(m.Result, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestHookListShowsHostHookAndSubscriberLiveness(t *testing.T) {
	f := newFakes()
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if spec.ID == "down" {
			return nil, errors.New("exec: no such file or directory")
		}
		return f.factory(spec, o)
	}
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		c.Clock = frozenClock{time.Unix(1000, 0)}
		pluginSettings(c)["paths"] = []string{
			hookPlugin(t, "down", nil, []any{subHook("notes.gate", 20)}),
			hookPlugin(t, "notes", []any{pubHook("notes.gate", "gate")}, nil),
			hookPlugin(t, "up", nil, []any{subHook("notes.gate", 5)}),
		}
	}, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	<-f.started // notes and up launch after down failed
	if got := h.podStatus("down"); got != "failed" {
		t.Fatalf("down status = %q", got)
	}

	l := decodeList(t, rpc(t, h, "", protocol.MethodHookList, struct{}{}))
	if len(l.Hooks) != 2 || l.Hooks[0].ID != "husk.before-state-clear" || l.Hooks[1].ID != "notes.gate" {
		t.Fatalf("hooks = %+v", l.Hooks)
	}
	host := l.Hooks[0]
	if host.Owner != "husk" || host.Kind != "gate" || host.TimeoutMS != 2000 || host.Title != "Before state clear" || len(host.Subscribers) != 0 {
		t.Fatalf("host hook = %+v", host)
	}
	gate := l.Hooks[1]
	if gate.Owner != "notes" || gate.Kind != "gate" || gate.TimeoutMS != 10000 || len(gate.Subscribers) != 2 {
		t.Fatalf("notes.gate = %+v", gate)
	}
	if s := gate.Subscribers[0]; s.Plugin != "up" || s.Priority != 5 || !s.Live {
		t.Fatalf("first subscriber = %+v", s)
	}
	if s := gate.Subscribers[1]; s.Plugin != "down" || s.Priority != 20 || s.Live {
		t.Fatalf("second subscriber = %+v", s)
	}
}

// A failed plugin still owns its namespace: its documents stay in Store, so
// the outer plugin's declaration inside it is refused although nothing runs.
func TestHookFailedPluginStillOwnsItsNamespace(t *testing.T) {
	f := newFakes()
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if spec.ID == "go.reference" {
			return nil, errors.New("exec: no such file or directory")
		}
		return f.factory(spec, o)
	}
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		pluginSettings(c)["paths"] = []string{
			hookPlugin(t, "go.reference", nil, nil),
			hookPlugin(t, "go", []any{pubHook("go.reference.x", "gate"), pubHook("go.y", "gate")}, nil),
		}
	}, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	if got := h.podStatus("go.reference"); got != "failed" {
		t.Fatalf("go.reference status = %q", got)
	}
	var ids []string
	for _, hk := range decodeList(t, rpc(t, h, "", protocol.MethodHookList, struct{}{})).Hooks {
		ids = append(ids, hk.ID)
	}
	if !slices.Equal(ids, []string{"go.y", "husk.before-state-clear"}) {
		t.Fatalf("hooks = %v", ids)
	}
	h.loop(func() {
		if p := h.root.catalog.Problems()["go"]; len(p) != 1 {
			t.Errorf("problems = %v", h.root.catalog.Problems())
		}
	})
}

// A subscriber that exits mid-call is answered cancelled/plugin_exited at
// once: the clock is frozen, so no deadline could have produced it.
func TestHookSubscriberExitMidCallIsCancelledAtOnce(t *testing.T) {
	f := newFakes()
	f.onSend = func(p *fakeProc, m protocol.Message) {
		if p.id == "gone" && m.Method == protocol.MethodHookCall {
			p.opts.Replies.Closed(p.id, errors.New("exit status 1"))
		}
	}
	h := hookHost(t, f, []string{
		hookPlugin(t, "notes", []any{pubHook("notes.gate", "gate")}, nil),
		hookPlugin(t, "gone", nil, []any{"notes.gate"}),
	}, 2)
	o := decodeOutcome(t, rpc(t, h, "tok-notes", protocol.MethodHookCall, map[string]any{"hook": "notes.gate"}))
	if len(o.Results) != 1 || o.Results[0].Plugin != "gone" {
		t.Fatalf("results = %+v", o.Results)
	}
	e := o.Results[0].Error
	if e == nil || e.Code != protocol.CodeCancelled {
		t.Fatalf("entry error = %+v", e)
	}
	var data map[string]any
	_ = json.Unmarshal(e.Data, &data)
	if data["reason"] != "plugin_exited" {
		t.Fatalf("error data = %s", e.Data)
	}
	if o.Allowed == nil || !*o.Allowed {
		t.Fatalf("a departed gate subscriber must count as allow: %+v", o)
	}
}

// TestHookReloadReordersTheNextCall changes a subscriber's priority in the
// declared documents, reloads the catalog the way the loop does, and checks
// that the next hook.call asks the subscribers in the new order.
func TestHookReloadReordersTheNextCall(t *testing.T) {
	f := newFakes()
	responders{
		"alpha": func(legParams) any { return map[string]any{} },
		"zeta":  func(legParams) any { return map[string]any{} },
	}.install(f)
	h := hookHost(t, f, []string{
		hookPlugin(t, "notes", []any{pubHook("notes.gate", "gate")}, nil),
		hookPlugin(t, "alpha", nil, []any{subHook("notes.gate", 10)}),
		hookPlugin(t, "zeta", nil, []any{subHook("notes.gate", 5)}),
	}, 3)
	fire := map[string]any{"hook": "notes.gate"}
	o := decodeOutcome(t, rpc(t, h, "tok-notes", protocol.MethodHookCall, fire))
	if !slices.Equal(o.plugins(), []string{"zeta", "alpha"}) {
		t.Fatalf("before reload: %v", o.plugins())
	}

	h.loop(func() {
		docs := h.root.catalog.declared()
		docs["alpha"] = hookDoc(nil, hookReg{"notes.gate", 1})
		h.root.catalog.declared = func() map[string]store.Doc { return docs }
		h.root.refreshCatalog()
	})
	o = decodeOutcome(t, rpc(t, h, "tok-notes", protocol.MethodHookCall, fire))
	if !slices.Equal(o.plugins(), []string{"alpha", "zeta"}) {
		t.Fatalf("after reload: %v", o.plugins())
	}
}
