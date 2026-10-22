package calls

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type sentMessage struct {
	plugin  PluginID
	message protocol.Message
}
type fakeOutbox struct {
	messages []sentMessage
	err      error
	inspect  func(PluginID, protocol.Message)
}

func (b *fakeOutbox) Send(plugin PluginID, m protocol.Message) error {
	b.messages = append(b.messages, sentMessage{plugin, m})
	if b.inspect != nil {
		b.inspect(plugin, m)
	}
	return b.err
}

func newTestTable(opts TableOptions) (*Table, *fakeClock, *fakeOutbox) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	box := &fakeOutbox{}
	opts.Clock = clock
	return New(box, opts), clock, box
}

func mustSend(t *testing.T, table *Table, plugin PluginID, opts ...Options) Handle {
	t.Helper()
	options := Options{Requester: "test"}
	if len(opts) > 0 {
		options = opts[0]
	}
	h, err := table.Send(plugin, "sample.call", json.RawMessage(`{"value":1}`), options)
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, table)
	return h
}

func replyFor(h Handle, value string) protocol.Message {
	id, _ := protocol.NewIntID(int64(h.ID))
	return protocol.Message{ID: &id, Result: json.RawMessage(value)}
}

func assertCounts(t *testing.T, table *Table) {
	t.Helper()
	counts := make(map[PluginID]int)
	for h, p := range table.pending {
		if h != p.handle || h.ID == 0 || h.ID > table.lastID || h.ID > maxID {
			t.Fatalf("invalid pending entry %+v", p)
		}
		counts[h.Plugin]++
	}
	if len(table.pending) > table.maxPending || !reflect.DeepEqual(counts, table.perPlugin) {
		t.Fatalf("counts=%v perPlugin=%v", counts, table.perPlugin)
	}
	for plugin, n := range counts {
		if n <= 0 || n > table.maxPendingPerPlugin {
			t.Fatalf("plugin %s count %d", plugin, n)
		}
	}
	stats := table.Stats()
	if stats.Pending != len(table.pending) || !reflect.DeepEqual(stats.PerPlugin, counts) {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestSendRecordsBeforeOutboxAndEchoesRequest(t *testing.T) {
	table, clock, box := newTestTable(TableOptions{})
	box.inspect = func(plugin PluginID, m protocol.Message) {
		if table.Stats().Pending != 1 || table.Stats().PerPlugin[plugin] != 1 {
			t.Fatal("request not recorded before enqueue")
		}
		if m.ID.String() != "1" {
			t.Fatal(m.ID.String())
		}
		clock.advance(time.Second) // enqueue time, not write completion, starts the deadline
	}
	params := json.RawMessage(`{"opaque": {"n":2}}`)
	h, err := table.Send("p", "anything.opaque", params, Options{Requester: "caller"})
	if err != nil || h != (Handle{Plugin: "p", ID: 1}) {
		t.Fatalf("handle=%+v err=%v", h, err)
	}
	m := box.messages[0]
	if m.plugin != "p" || m.message.Method != "anything.opaque" || string(m.message.Params) != string(params) {
		t.Fatalf("wire=%+v", m)
	}
	kind, err := m.message.Kind()
	if err != nil || kind != protocol.KindRequest {
		t.Fatalf("kind=%v err=%v", kind, err)
	}
	p := table.pending[h]
	if p.requester != "caller" || !p.deadline.Equal(p.started.Add(DefaultTimeout)) || p.started.Equal(clock.Now()) {
		t.Fatalf("entry=%+v", p)
	}
	assertCounts(t, table)
}

func TestOptionsTimeoutMatrix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timeout   time.Duration
		requester Requester
		valid     bool
	}{
		{"default", 0, "r", true}, {"minimum", MinTimeout, "r", true}, {"maximum", MaxTimeout, "r", true},
		{"too short", MinTimeout - time.Nanosecond, "r", false}, {"too long", MaxTimeout + time.Nanosecond, "r", false},
		{"negative", -time.Second, "r", false}, {"missing requester", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table, _, box := newTestTable(TableOptions{})
			h, err := table.Send("p", "sample.call", nil, Options{Timeout: tc.timeout, Requester: tc.requester})
			if tc.valid {
				if err != nil || h.ID != 1 {
					t.Fatalf("handle=%v err=%v", h, err)
				}
				want := tc.timeout
				if want == 0 {
					want = DefaultTimeout
				}
				if table.pending[h].timeout != want {
					t.Fatal("wrong effective timeout")
				}
			} else if !errors.Is(err, ErrBadOptions) || h != (Handle{}) || len(box.messages) != 0 || table.lastID != 0 || table.Stats().Pending != 0 {
				t.Fatalf("invalid options changed state: handle=%v err=%v", h, err)
			}
			assertCounts(t, table)
		})
	}
}

func TestFailedSendsBurnIDsAndNeverProduceResults(t *testing.T) {
	table, clock, box := newTestTable(TableOptions{MaxPending: 1})
	first := mustSend(t, table, "a")
	if h, err := table.Send("b", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrBusy) || h != (Handle{}) {
		t.Fatalf("%v %v", h, err)
	}
	table.Cancel(first)
	third := mustSend(t, table, "b")
	if third.ID != 3 {
		t.Fatal(third)
	}
	table.Cancel(third)
	cause := errors.New("writer queue full")
	box.err = cause
	if h, err := table.Send("a", "sample.call", nil, Options{Requester: "r"}); h != (Handle{}) || !errors.Is(err, ErrUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("%v %v", h, err)
	}
	assertCounts(t, table)
	failed := box.messages[len(box.messages)-1]
	if failed.message.ID.String() != "4" {
		t.Fatal("outbox failure reused an id")
	}
	clock.advance(time.Hour)
	if results := table.Expire(clock.Now()); len(results) != 0 {
		t.Fatal(results)
	}
	if _, ok := table.Resolve("a", protocol.Message{ID: failed.message.ID, Result: json.RawMessage(`{}`)}); ok {
		t.Fatal("failed Send resolved")
	}
	box.err = nil
	fifth := mustSend(t, table, "a")
	if fifth.ID != 5 {
		t.Fatal(fifth)
	}
	table.Cancel(fifth)
	if len(table.Close()) != 0 {
		t.Fatal("cancelled/failed sends produced a result")
	}
}

func TestDefaultCapsAndOtherPluginsUnaffected(t *testing.T) {
	table, _, _ := newTestTable(TableOptions{})
	for i := 0; i < MaxPendingPerPlugin; i++ {
		mustSend(t, table, "p")
	}
	if _, err := table.Send("p", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrPluginBusy) {
		t.Fatal(err)
	}
	mustSend(t, table, "other")
	for n := 1; n < MaxPending/MaxPendingPerPlugin; n++ {
		plugin := PluginID(fmt.Sprintf("p%d", n))
		for i := 0; i < MaxPendingPerPlugin; i++ {
			if table.Stats().Pending == MaxPending {
				break
			}
			mustSend(t, table, plugin)
		}
	}
	if table.Stats().Pending != MaxPending {
		t.Fatal(table.Stats().Pending)
	}
	if _, err := table.Send("fresh", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	assertCounts(t, table)
}

func TestCanSendPreflightsWholeFanoutAndDuplicates(t *testing.T) {
	table, _, box := newTestTable(TableOptions{MaxPending: 3, MaxPendingPerPlugin: 2})
	mustSend(t, table, "p")
	before := table.Stats()
	lastID, writes := table.lastID, len(box.messages)
	if err := table.CanSend("p", "p"); !errors.Is(err, ErrPluginBusy) {
		t.Fatal(err)
	}
	if err := table.CanSend("p", "q", "r"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, table.Stats()) || table.lastID != lastID || len(box.messages) != writes {
		t.Fatal("preflight mutated table or wrote")
	}
	if err := table.CanSend("p", "q"); err != nil {
		t.Fatal(err)
	}
	mustSend(t, table, "p")
	mustSend(t, table, "q")
	if err := table.CanSend(); err != nil {
		t.Fatal(err)
	}
	if err := table.CanSend("other"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	assertCounts(t, table)
}

func TestStatsIsDetachedAndCountersSurviveEmptyTable(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	h := mustSend(t, table, "p")
	stats := table.Stats()
	stats.PerPlugin["p"] = 500
	stats.PerPlugin["other"] = 10
	clock.advance(DefaultTimeout)
	if len(table.Expire(clock.Now())) != 1 {
		t.Fatal("not expired")
	}
	_, _ = table.Resolve(h.Plugin, replyFor(h, `{}`))
	got := table.Stats()
	if got.Pending != 0 || len(got.PerPlugin) != 0 || got.Timeouts != 1 || got.Orphans != 1 {
		t.Fatal(got)
	}
	mustSend(t, table, "p")
	assertCounts(t, table)
}

func TestIDExhaustionNeverWrapsAndLogsOnce(t *testing.T) {
	var logs []string
	table, _, box := newTestTable(TableOptions{Logger: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	table.lastID = maxID - 1
	h := mustSend(t, table, "p")
	if h.ID != maxID || box.messages[0].message.ID.String() != strconv.FormatUint(uint64(maxID), 10) {
		t.Fatal(h)
	}
	for i := 0; i < 3; i++ {
		if h, err := table.Send("p", "sample.call", nil, Options{Requester: "r"}); h != (Handle{}) || !errors.Is(err, ErrBusy) {
			t.Fatalf("%v %v", h, err)
		}
	}
	if len(logs) != 1 || len(box.messages) != 1 {
		t.Fatalf("logs=%v writes=%d", logs, len(box.messages))
	}
	table.Cancel(h)
	if _, err := table.Send("p", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if len(logs) != 1 || table.lastID != maxID {
		t.Fatal("exhaustion reset")
	}
	assertCounts(t, table)
}

func TestConstructorDefaultsOverridesAndNilOutbox(t *testing.T) {
	table := New(nil, TableOptions{})
	if _, ok := table.clock.(realClock); !ok || table.maxPending != MaxPending || table.maxPendingPerPlugin != MaxPendingPerPlugin {
		t.Fatal("wrong defaults")
	}
	if _, err := table.Send("p", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	assertCounts(t, table)
	for _, opts := range []TableOptions{{MaxPending: -1}, {MaxPendingPerPlugin: -1}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("negative cap did not panic")
				}
			}()
			New(nil, opts)
		}()
	}
}
