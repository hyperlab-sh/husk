package eventbus_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/stem/eventbus"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type capture struct {
	mu   sync.Mutex
	msgs []protocol.Message
	err  error
}

func (c *capture) Notify(m protocol.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.msgs = append(c.msgs, m)
	return nil
}

type delivered struct {
	Name, Source string
	Data         map[string]any
}

func (c *capture) got(t *testing.T) []delivered {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []delivered
	for _, m := range c.msgs {
		if m.Method != protocol.MethodEventDeliver || m.ID != nil {
			t.Fatalf("not an id-less event.deliver: %+v", m)
		}
		var d delivered
		if err := json.Unmarshal(m.Params, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func (c *capture) names(t *testing.T) []string {
	var out []string
	for _, d := range c.got(t) {
		out = append(out, d.Name)
	}
	return out
}

type live map[string]bool

func (l live) Live(id string) bool { return l[id] }

type sinks map[string]contracts.Notifier

func (s sinks) Notifier(id string) (contracts.Notifier, bool) { n, ok := s[id]; return n, ok }

type rig struct {
	bus  *eventbus.Bus
	live live
	sink map[string]*capture
	logs []string
}

func newRig(t *testing.T, subs map[string][]string, noCo ...string) *rig {
	t.Helper()
	r := &rig{live: live{}, sink: map[string]*capture{}}
	s := sinks{}
	for id := range subs {
		r.live[id] = true
		r.sink[id] = &capture{}
		s[id] = r.sink[id]
	}
	r.bus = eventbus.New(eventbus.Options{Liveness: r.live, Sinks: s, Logger: func(f string, a ...any) { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }})
	if err := r.bus.SetTable(eventbus.Table{Subscribe: subs, NoCoalesce: noCo}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDeliverShape(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"notes.saved"}})
	r.bus.Publish(eventbus.Event{Name: "notes.saved", Key: "k", Source: "notes", Data: map[string]any{"path": "/x"}})
	r.bus.Publish(eventbus.Event{Name: "notes.saved", Key: "k2", Source: "notes"})
	r.bus.Flush()
	got := r.sink["a"].got(t)
	if len(got) != 2 || got[0].Name != "notes.saved" || got[0].Source != "notes" || got[0].Data["path"] != "/x" {
		t.Fatalf("got %+v", got)
	}
	if got[1].Data == nil {
		t.Fatal("nil data must be delivered as {}")
	}
	if strings.Contains(string(r.sink["a"].msgs[0].Params), `"key"`) {
		t.Fatal("key must not be on the wire")
	}
}

func TestNothingDeliveredBeforeFlush(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	if len(r.sink["a"].msgs) != 0 {
		t.Fatal("Publish must not deliver")
	}
	r.bus.Flush()
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 1 {
		t.Fatal("a flushed batch must not be delivered twice")
	}
}

func TestWildcardMatching(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"notes.saved", "notes.saved", true},
		{"notes.saved", "notes.saved2", false},
		{"notes.*", "notes.saved", true},
		{"notes.*", "notes.view.opened", true},
		{"notes.*", "notes", false},
		{"notes.*", "notes.", false},
		{"notes.*", "notesx.saved", false},
		{"notes.*", "other.notes.saved", false},
		{"state.changed", "state.changed", true},
	}
	for _, tt := range tests {
		if got := eventbus.Matches(tt.pattern, tt.name); got != tt.want {
			t.Errorf("Matches(%q,%q)=%v", tt.pattern, tt.name, got)
		}
	}
}

func TestPatternValidation(t *testing.T) {
	for _, ok := range []string{"a.b", "a", "notes.*", "a.b.*", "state.changed"} {
		if err := eventbus.ValidatePattern(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "*", ".*", "*foo", "a.*.b", "a.", ".a", "a..b", "a.**", "a b", "a.b*", "a.*.*", "a\n.b"} {
		err := eventbus.ValidatePattern(bad)
		if err == nil || contracts.CodeOf(err) != string(protocol.CodeInvalidParams) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestSetTableRefusesMalformedAndKeepsOld(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	if err := r.bus.SetTable(eventbus.Table{Subscribe: map[string][]string{"a": {"*foo"}}}); err == nil {
		t.Fatal("malformed pattern accepted")
	}
	if err := r.bus.SetTable(eventbus.Table{NoCoalesce: []string{"a.*"}}); err == nil {
		t.Fatal("wildcard in NoCoalesce accepted")
	}
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 1 {
		t.Fatal("old table lost after refused SetTable")
	}
}

func TestCoalescing(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"state.changed", "notes.*"}})
	pub := func(name, key, v string) {
		r.bus.Publish(eventbus.Event{Name: name, Key: key, Source: "husk", Data: map[string]any{"v": v}})
	}
	pub("state.changed", "s1", "1")
	pub("notes.saved", "", "1")
	pub("state.changed", "s2", "1")
	pub("state.changed", "s1", "2")
	pub("notes.saved", "", "2")
	pub("state.changed", "s1", "3")
	r.bus.Flush()
	got := r.sink["a"].got(t)
	// First-arrival positions, last payloads; different keys never merge.
	want := []string{"state.changed:3", "notes.saved:2", "state.changed:1"}
	var have []string
	for _, d := range got {
		have = append(have, fmt.Sprint(d.Name, ":", d.Data["v"]))
	}
	if !slices.Equal(have, want) {
		t.Fatalf("%v, want %v", have, want)
	}
	if c := r.bus.Stats().Coalesced; c != 3 {
		t.Fatalf("coalesced %d", c)
	}
}

func TestSameNameDifferentKeysAndEmptyKeyDistinct(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	for _, k := range []string{"", "a", "b", ""} {
		r.bus.Publish(eventbus.Event{Name: "x.y", Key: k})
	}
	r.bus.Flush()
	if n := len(r.sink["a"].msgs); n != 3 {
		t.Fatalf("%d deliveries, want 3", n)
	}
}

func TestNoCoalescingAcrossTicks(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	r.bus.Publish(eventbus.Event{Name: "x.y", Data: map[string]any{"v": 1}})
	r.bus.Flush()
	r.bus.Publish(eventbus.Event{Name: "x.y", Data: map[string]any{"v": 2}})
	r.bus.Flush()
	if got := r.sink["a"].got(t); len(got) != 2 || got[0].Data["v"] != 1.0 || got[1].Data["v"] != 2.0 {
		t.Fatalf("%+v", got)
	}
}

func TestCoalescingIsPerSubscriber(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}, "b": {"x.*"}})
	r.bus.Publish(eventbus.Event{Name: "x.y", Key: "k", Data: map[string]any{"v": 1}})
	r.bus.Publish(eventbus.Event{Name: "x.z"})
	r.bus.Publish(eventbus.Event{Name: "x.y", Key: "k", Data: map[string]any{"v": 2}})
	r.bus.Flush()
	if got := r.sink["a"].names(t); !slices.Equal(got, []string{"x.y"}) {
		t.Fatalf("a: %v", got)
	}
	if got := r.sink["b"].names(t); !slices.Equal(got, []string{"x.y", "x.z"}) {
		t.Fatalf("b: %v", got)
	}
}

func TestNonCoalescingQueuedInFullUpToCap(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}, "b": {"x.y"}}, "x.y")
	r.sink["b"].err = errors.New("full")
	n := eventbus.MaxNonCoalescing + 10
	for i := 0; i < n; i++ {
		r.bus.Publish(eventbus.Event{Name: "x.y", Key: "same", Data: map[string]any{"i": i}})
	}
	r.bus.Flush()
	got := r.sink["a"].got(t)
	if len(got) != eventbus.MaxNonCoalescing {
		t.Fatalf("%d delivered", len(got))
	}
	if got[0].Data["i"] != 0.0 || got[len(got)-1].Data["i"] != float64(eventbus.MaxNonCoalescing-1) {
		t.Fatal("the first 256 must be kept, the excess dropped")
	}
	st := r.bus.Stats()
	if st.Overflow != 20 { // 10 per subscriber, two subscribers
		t.Fatalf("overflow %d", st.Overflow)
	}
	if len(r.logs) != 2 { // one line per subscriber for this tick
		t.Fatalf("logs %v", r.logs)
	}
	// The cap is per tick: the next tick starts fresh.
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != eventbus.MaxNonCoalescing+1 {
		t.Fatal("cap carried across ticks")
	}
}

func TestNonCoalescingDoesNotAffectOtherNames(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.*"}}, "x.all")
	r.bus.Publish(eventbus.Event{Name: "x.all"})
	r.bus.Publish(eventbus.Event{Name: "x.all"})
	r.bus.Publish(eventbus.Event{Name: "x.some"})
	r.bus.Publish(eventbus.Event{Name: "x.some"})
	r.bus.Flush()
	if got := r.sink["a"].names(t); !slices.Equal(got, []string{"x.all", "x.all", "x.some"}) {
		t.Fatal(got)
	}
}

func TestLastOfBurstNeverDropped(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	for i := 0; i < 1000; i++ {
		r.bus.Publish(eventbus.Event{Name: "x.y", Key: "k", Data: map[string]any{"i": i}})
	}
	r.bus.Flush()
	got := r.sink["a"].got(t)
	if len(got) != 1 || got[0].Data["i"] != 999.0 {
		t.Fatalf("%+v", got)
	}
}

func TestSourceAttributionIsPreserved(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"state.changed", "notes.saved", "user.ping"}})
	r.bus.Publish(eventbus.Event{Name: "state.changed", Source: eventbus.SourceHost})
	r.bus.Publish(eventbus.Event{Name: "notes.saved", Source: "notes"})
	r.bus.Publish(eventbus.Event{Name: "user.ping", Source: eventbus.SourceUser})
	r.bus.Flush()
	var srcs []string
	for _, d := range r.sink["a"].got(t) {
		srcs = append(srcs, d.Source)
	}
	if !slices.Equal(srcs, []string{"husk", "notes", "user"}) {
		t.Fatal(srcs)
	}
}

func TestPublisherReceivesOwnEventsIfSubscribed(t *testing.T) {
	r := newRig(t, map[string][]string{"notes": {"notes.*"}, "other": {"notes.*"}})
	r.bus.Publish(eventbus.Event{Name: "notes.saved", Source: "notes"})
	r.bus.Flush()
	if len(r.sink["notes"].msgs) != 1 || len(r.sink["other"].msgs) != 1 {
		t.Fatal("self-delivery or fan-out missing")
	}
}

func TestOnePluginMatchingTwoPatternsGetsOne(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"notes.*", "notes.saved"}})
	r.bus.Publish(eventbus.Event{Name: "notes.saved"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 1 {
		t.Fatalf("%d deliveries", len(r.sink["a"].msgs))
	}
}

func TestLivenessGateSkipsAndNeverReplays(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}, "b": {"x.y"}})
	r.live["a"] = false
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 0 || len(r.sink["b"].msgs) != 1 {
		t.Fatal("not-live plugin must be skipped, others unaffected")
	}
	r.live["a"] = true
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 0 {
		t.Fatal("event replayed after the plugin became live")
	}
	if r.bus.Stats().NotLive != 1 {
		t.Fatal("skip not counted")
	}
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 1 {
		t.Fatal("live plugin gets events again")
	}
}

func TestMissingSinkDropsQuietly(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	// Table names a live plugin that has no sink.
	r.live["ghost"] = true
	if err := r.bus.SetTable(eventbus.Table{Subscribe: map[string][]string{"ghost": {"x.y"}, "a": {"x.y"}}}); err != nil {
		t.Fatal(err)
	}
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 1 || r.bus.Stats().NoSink != 1 {
		t.Fatalf("%+v", r.bus.Stats())
	}
}

func TestNilLivenessTreatsPluginsAsLive(t *testing.T) {
	c := &capture{}
	b := eventbus.New(eventbus.Options{Sinks: sinks{"a": c}})
	if err := b.SetTable(eventbus.Table{Subscribe: map[string][]string{"a": {"x.y"}}}); err != nil {
		t.Fatal(err)
	}
	b.Publish(eventbus.Event{Name: "x.y"})
	b.Flush()
	if len(c.msgs) != 1 {
		t.Fatal("not delivered")
	}
}

func TestSlowOrDeadSubscriberAffectsOnlyItself(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.*"}, "b": {"x.*"}})
	r.sink["a"].err = errors.New("queue full")
	r.bus.Publish(eventbus.Event{Name: "x.1"})
	r.bus.Publish(eventbus.Event{Name: "x.2"})
	r.bus.Flush()
	if len(r.sink["b"].msgs) != 2 {
		t.Fatal("healthy subscriber starved")
	}
	st := r.bus.Stats()
	if st.NotifyError != 2 || st.Delivered != 2 {
		t.Fatalf("%+v", st)
	}
	// A transient error drops only that message.
	r.sink["a"].err = nil
	r.bus.Publish(eventbus.Event{Name: "x.3"})
	r.bus.Flush()
	if got := r.sink["a"].names(t); !slices.Equal(got, []string{"x.3"}) {
		t.Fatal(got)
	}
}

func TestPerSubscriberOrderStableAcrossTicks(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.*"}})
	for tick := 0; tick < 3; tick++ {
		for i := 0; i < 3; i++ {
			r.bus.Publish(eventbus.Event{Name: fmt.Sprintf("x.t%d-%d", tick, i)})
		}
		r.bus.Flush()
	}
	var want []string
	for tick := 0; tick < 3; tick++ {
		for i := 0; i < 3; i++ {
			want = append(want, fmt.Sprintf("x.t%d-%d", tick, i))
		}
	}
	if got := r.sink["a"].names(t); !slices.Equal(got, want) {
		t.Fatal(got)
	}
}

func TestConnectionSubscription(t *testing.T) {
	r := newRig(t, nil)
	c := &capture{}
	cancel, err := r.bus.Subscribe(c, "notes.*")
	if err != nil {
		t.Fatal(err)
	}
	r.bus.Publish(eventbus.Event{Name: "notes.saved", Source: "notes"})
	r.bus.Publish(eventbus.Event{Name: "other.saved"})
	r.bus.Flush()
	if got := c.names(t); !slices.Equal(got, []string{"notes.saved"}) {
		t.Fatal(got)
	}
	cancel()
	r.bus.Publish(eventbus.Event{Name: "notes.saved"})
	r.bus.Flush()
	if len(c.msgs) != 1 {
		t.Fatal("delivery after unsubscribe")
	}
	cancel() // idempotent
}

func TestCancelBetweenPublishAndFlushDropsQueued(t *testing.T) {
	r := newRig(t, nil)
	c := &capture{}
	cancel, _ := r.bus.Subscribe(c, "x.y")
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	cancel()
	r.bus.Flush()
	if len(c.msgs) != 0 {
		t.Fatal("queued event delivered after unsubscribe")
	}
}

func TestSubscribeAfterPublishGetsNoHistory(t *testing.T) {
	r := newRig(t, nil)
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	c := &capture{}
	if _, err := r.bus.Subscribe(c, "x.y"); err != nil {
		t.Fatal(err)
	}
	r.bus.Flush()
	if len(c.msgs) != 0 {
		t.Fatal("history replayed")
	}
}

func TestSubscribeRefusals(t *testing.T) {
	r := newRig(t, nil)
	for _, p := range []string{"", "*foo", "a.*.b"} {
		if _, err := r.bus.Subscribe(&capture{}, p); contracts.CodeOf(err) != "invalid_params" {
			t.Errorf("%q: %v", p, err)
		}
	}
	if _, err := r.bus.Subscribe(nil, "a.b"); err == nil {
		t.Fatal("nil notifier accepted")
	}
	c := &capture{}
	r.bus.Publish(eventbus.Event{Name: "a.b"})
	r.bus.Flush()
	if len(c.msgs) != 0 {
		t.Fatal("refused subscription registered")
	}
}

func TestConnectionSubscriberNotLivenessGated(t *testing.T) {
	r := newRig(t, nil)
	c := &capture{}
	_, _ = r.bus.Subscribe(c, "x.y")
	r.bus.Publish(eventbus.Event{Name: "x.y"})
	r.bus.Flush()
	if len(c.msgs) != 1 {
		t.Fatal("not delivered")
	}
}

func TestMalformedPublishDropped(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.y"}})
	r.bus.Publish(eventbus.Event{Name: ""})
	r.bus.Publish(eventbus.Event{Name: "x..y"})
	r.bus.Flush()
	if len(r.sink["a"].msgs) != 0 || len(r.logs) != 2 {
		t.Fatal(r.logs)
	}
}

func TestUnencodableDataCountsAsFailure(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"x.*"}})
	r.bus.Publish(eventbus.Event{Name: "x.bad", Data: map[string]any{"f": func() {}}})
	r.bus.Publish(eventbus.Event{Name: "x.ok"})
	r.bus.Flush()
	if got := r.sink["a"].names(t); !slices.Equal(got, []string{"x.ok"}) || r.bus.Stats().NotifyError != 1 {
		t.Fatal(got)
	}
}

func TestStateChangedIsAnOrdinaryEvent(t *testing.T) {
	r := newRig(t, map[string][]string{"a": {"state.changed"}})
	r.bus.Publish(eventbus.Event{Name: "state.changed", Key: "husk.focus", Source: "husk", Data: map[string]any{"slice": "husk.focus", "owner": "husk", "revision": 1}})
	r.bus.Publish(eventbus.Event{Name: "state.changed", Key: "husk.focus", Source: "husk", Data: map[string]any{"slice": "husk.focus", "owner": "husk", "revision": 2}})
	r.bus.Flush()
	got := r.sink["a"].got(t)
	if len(got) != 1 || got[0].Data["revision"] != 2.0 {
		t.Fatalf("%+v", got)
	}
}

func TestFakeNotifierIsAccepted(t *testing.T) {
	rec := fakes.NewRecorder()
	n := fakes.NewNotifier(rec)
	b := eventbus.New(eventbus.Options{})
	if _, err := b.Subscribe(n, "x.*"); err != nil {
		t.Fatal(err)
	}
	b.Publish(eventbus.Event{Name: "x.y"})
	b.Flush()
	if m := rec.Methods(); len(m) != 1 {
		t.Fatalf("%v", m)
	}
}

func TestConcurrentSubscribeCancel(t *testing.T) {
	b := eventbus.New(eventbus.Options{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c, _ := b.Subscribe(&capture{}, "x.*")
				c()
			}
		}()
	}
	for j := 0; j < 50; j++ {
		b.Publish(eventbus.Event{Name: "x.y"})
		b.Flush()
	}
	wg.Wait()
}

func TestAuthorize(t *testing.T) {
	P, C, S := contracts.CallerPlugin, contracts.CallerCLI, contracts.CallerStem
	tests := []struct {
		name   string
		kind   contracts.CallerKind
		id     string
		event  string
		source string
		code   string
	}{
		{"plugin own", P, "notes", "notes.saved", "notes", ""},
		{"plugin nested", P, "notes", "notes.view.opened", "notes", ""},
		{"plugin other namespace", P, "notes", "other.saved", "", "denied"},
		{"plugin bare id", P, "notes", "notes", "", "denied"},
		{"plugin id prefix only", P, "notes", "notes.", "", "invalid_params"},
		{"plugin lookalike prefix", P, "notes", "notesx.saved", "", "denied"},
		{"plugin core exact", P, "state", "state.changed", "", "denied"},
		{"plugin core prefix id", P, "husk", "husk.x", "", "denied"},
		{"plugin user prefix", P, "user", "user.x", "", "denied"},
		{"plugin pods", P, "pods", "pods.plugin-exited", "", "denied"},
		{"plugin empty id", P, "", ".x", "", "invalid_params"},
		{"cli user", C, "", "user.ping", "user", ""},
		{"cli plugin ns", C, "", "notes.saved", "", "denied"},
		{"cli core", C, "", "state.changed", "", "denied"},
		{"cli bare user", C, "", "user", "", "denied"},
		{"stem user", S, "", "user.ping", "user", ""},
		{"wildcard", C, "", "user.*", "", "invalid_params"},
		{"empty", C, "", "", "", "invalid_params"},
		{"unknown kind", 0, "", "user.x", "", "denied"},
	}
	for _, tt := range tests {
		src, err := eventbus.Authorize(tt.kind, tt.id, tt.event)
		if got := contracts.CodeOf(err); got != tt.code || src != tt.source {
			t.Errorf("%s: source %q code %q (%v)", tt.name, src, got, err)
		}
	}
}

func TestCoreNames(t *testing.T) {
	for _, n := range []string{"state.changed", "pods.plugin-exited", "services.reloaded", "secrets.read", "surface.resized", "input.key", "husk.anything", "user.x"} {
		if !eventbus.IsCoreName(n) {
			t.Errorf("%s not core", n)
		}
	}
	for _, n := range []string{"notes.saved", "stateful.x", "husky.x"} {
		if eventbus.IsCoreName(n) {
			t.Errorf("%s core", n)
		}
	}
}
