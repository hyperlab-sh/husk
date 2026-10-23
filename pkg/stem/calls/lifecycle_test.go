package calls

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestResolvePreservesSuccessNullAndPluginError(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		failure     *Error
	}{
		{"object", `{ "kept": [1,2] }`, nil}, {"null", `null`, nil},
		{"error", "", &Error{Code: protocol.CodeFailed, Message: "application declined", Data: json.RawMessage(`{"detail":1}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table, clock, _ := newTestTable(TableOptions{})
			h := mustSend(t, table, "p", Options{Requester: "opaque-requester"})
			clock.advance(2 * time.Second)
			reply := replyFor(h, tc.value)
			if tc.failure != nil {
				reply.Result = nil
				reply.Error = tc.failure
			}
			r, ok := table.Resolve("p", reply)
			if !ok || r.Handle != h || r.Requester != "opaque-requester" || r.Method != "sample.call" || r.Elapsed != 2*time.Second {
				t.Fatalf("result=%+v ok=%v", r, ok)
			}
			if string(r.Value) != tc.value || r.Err != tc.failure {
				t.Fatalf("payload changed: %+v", r)
			}
			if (r.Value == nil) == (r.Err == nil) {
				t.Fatal("result does not have exactly one outcome")
			}
			if table.Stats().Pending != 0 {
				t.Fatal("matched entry retained")
			}
			if _, ok := table.Resolve("p", reply); ok {
				t.Fatal("duplicate produced a result")
			}
			assertCounts(t, table)
		})
	}
}

func TestRepliesCanArriveOutOfOrderAndOnlyMatchTheirPlugin(t *testing.T) {
	table, _, _ := newTestTable(TableOptions{})
	a := mustSend(t, table, "a")
	b := mustSend(t, table, "b")
	c := mustSend(t, table, "a")
	if a.ID != 1 || b.ID != 2 || c.ID != 3 {
		t.Fatal("counter was not shared")
	}
	if _, ok := table.Resolve("b", replyFor(a, `{}`)); ok {
		t.Fatal("wrong plugin matched")
	}
	if table.Stats().Pending != 3 {
		t.Fatal("orphan removed a real call")
	}
	for _, h := range []Handle{c, a, b} {
		r, ok := table.Resolve(h.Plugin, replyFor(h, `{}`))
		if !ok || r.Handle != h {
			t.Fatalf("%+v %v", r, ok)
		}
		assertCounts(t, table)
	}
}

func TestInvalidAndNonNumericReplyIDsAreOrphans(t *testing.T) {
	table, _, _ := newTestTable(TableOptions{})
	h := mustSend(t, table, "p")
	stringID := protocol.NewStringID(fmt.Sprint(h.ID))
	zero, _ := protocol.NewIntID(0)
	negative, _ := protocol.NewIntID(-1)
	unknown, _ := protocol.NewIntID(999)
	ids := []*protocol.ID{nil, {}, &stringID, &zero, &negative, &unknown}
	// Fractions and out-of-range values cannot become valid protocol.ID values.
	// Even an unset ID left by rejected decoding must remain an orphan here.
	for _, raw := range []string{`1.5`, `9007199254740992`, `18446744073709551616`} {
		var id protocol.ID
		if err := json.Unmarshal([]byte(raw), &id); err == nil {
			t.Fatalf("codec accepted %s", raw)
		}
		ids = append(ids, &id)
	}
	for _, id := range ids {
		if _, ok := table.Resolve("p", protocol.Message{ID: id, Result: json.RawMessage(`{}`)}); ok {
			t.Fatal("invalid id matched")
		}
		if table.Stats().Pending != 1 {
			t.Fatal("orphan removed call")
		}
	}
	if table.Stats().Orphans != uint64(len(ids)) {
		t.Fatal(table.Stats())
	}
	if _, ok := table.Resolve("p", replyFor(h, `{}`)); !ok {
		t.Fatal("real id lost")
	}
	assertCounts(t, table)
}

func TestMalformedMatchedRepliesFailWithInternal(t *testing.T) {
	for _, shape := range []string{"both", "neither", "method", "params", "token", "invalid result", "invalid error data"} {
		t.Run(shape, func(t *testing.T) {
			var logs []string
			table, _, _ := newTestTable(TableOptions{Logger: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
			h := mustSend(t, table, "p")
			reply := replyFor(h, `{}`)
			switch shape {
			case "both":
				reply.Error = &Error{Code: protocol.CodeFailed, Message: "x"}
			case "neither":
				reply.Result = nil
			case "method":
				reply.Method = "sample.call"
			case "params":
				reply.Params = json.RawMessage(`{}`)
			case "token":
				reply.Token = "not-a-reply-field"
			case "invalid result":
				reply.Result = json.RawMessage(`{`)
			case "invalid error data":
				reply.Result = nil
				reply.Error = &Error{Code: protocol.CodeFailed, Data: json.RawMessage(`[]`)}
			}
			r, ok := table.Resolve("p", reply)
			if !ok || r.Value != nil || r.Err == nil || r.Err.Code != protocol.CodeInternal || string(r.Err.Data) != `{"reason":"invalid_reply"}` {
				t.Fatalf("%+v %v", r, ok)
			}
			if len(logs) != 1 || !strings.Contains(logs[0], "p") || !strings.Contains(logs[0], "sample.call") {
				t.Fatal(logs)
			}
			if len(table.Close()) != 0 || table.Stats().Orphans != 0 {
				t.Fatal("invalid matched reply remained pending or counted as orphan")
			}
			assertCounts(t, table)
		})
	}
}

func TestReplyHandledBeforeSweepWinsEvenAfterDeadline(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	h := mustSend(t, table, "p")
	clock.advance(DefaultTimeout + SweepInterval)
	r, ok := table.Resolve("p", replyFor(h, `{}`))
	if !ok || r.Err != nil || r.Elapsed != DefaultTimeout+SweepInterval {
		t.Fatalf("%+v %v", r, ok)
	}
	if len(table.Expire(clock.Now())) != 0 || table.Stats().Timeouts != 0 {
		t.Fatal("reply lost to deadline")
	}
}

func TestExpireBoundaryOrderingAndLateReplies(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	late := mustSend(t, table, "p", Options{Timeout: 5 * time.Second, Requester: "r"})
	q1 := mustSend(t, table, "q", Options{Timeout: time.Second, Requester: "r"})
	p := mustSend(t, table, "p", Options{Timeout: time.Second, Requester: "r"})
	q2 := mustSend(t, table, "q", Options{Timeout: time.Second, Requester: "r"})
	clock.advance(time.Second - time.Nanosecond)
	if len(table.Expire(clock.Now())) != 0 {
		t.Fatal("expired before deadline")
	}
	clock.advance(time.Nanosecond)
	results := table.Expire(clock.Now())
	want := []Handle{p, q1, q2}
	if len(results) != len(want) {
		t.Fatal(results)
	}
	for i, r := range results {
		if r.Handle != want[i] || r.Err.Code != protocol.CodeTimeout || r.Err.Data != nil || r.Value != nil || r.Elapsed != time.Second {
			t.Fatalf("result %d=%+v", i, r)
		}
		if r.Err.Message != fmt.Sprintf("plugin %s did not reply to sample.call within 1s", r.Handle.Plugin) {
			t.Fatal(r.Err.Message)
		}
		if _, ok := table.Resolve(r.Handle.Plugin, replyFor(r.Handle, `{}`)); ok {
			t.Fatal("late reply matched")
		}
	}
	if table.Stats().Timeouts != 3 || table.Stats().Pending != 1 {
		t.Fatal(table.Stats())
	}
	clock.advance(4 * time.Second)
	more := table.Expire(clock.Now())
	if len(more) != 1 || more[0].Handle != late || table.Stats().Timeouts != 4 {
		t.Fatal(more)
	}
	assertCounts(t, table)
}

// The Pod's final reason reaches a pending call verbatim, in Message, while
// Data stays the stable plugin_exited code (pods.md wrapper invariants 4 and 6).
func TestDropPluginCarriesThePodReasonVerbatim(t *testing.T) {
	for _, reason := range []string{
		"exited",
		"crashed (exit status 7): exit status 7",
		"crashed (signal: killed): signal: killed",
		"stopped reading: wrapper: stdout closed while process is running",
		"stopped reading: read stdout: connection reset by peer",
		"stopped reading: protocol: send queue full (1024 messages)",
		"stopped reading: protocol: write timed out after 5s",
	} {
		t.Run(reason, func(t *testing.T) {
			table, _, _ := newTestTable(TableOptions{})
			mustSend(t, table, "p")
			results := table.DropPlugin("p", reason)
			if len(results) != 1 {
				t.Fatal(results)
			}
			if e := results[0].Err; e.Code != protocol.CodeCancelled || string(e.Data) != `{"reason":"plugin_exited"}` || e.Message != "plugin p exited: "+reason {
				t.Fatalf("error = %+v", e)
			}
		})
	}
}

func TestDropPluginOrdersByIDAndPreservesOthers(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	p1 := mustSend(t, table, "p", Options{Timeout: MaxTimeout, Requester: "first"})
	other := mustSend(t, table, "other")
	p2 := mustSend(t, table, "p", Options{Timeout: MinTimeout, Requester: "second"})
	clock.advance(500 * time.Millisecond)
	results := table.DropPlugin("p", "stdin queue full")
	if len(results) != 2 || results[0].Handle != p1 || results[1].Handle != p2 {
		t.Fatal(results)
	}
	for _, r := range results {
		if r.Err.Code != protocol.CodeCancelled || string(r.Err.Data) != `{"reason":"plugin_exited"}` || r.Err.Message != "plugin p exited: stdin queue full" || r.Elapsed != 500*time.Millisecond {
			t.Fatal(r)
		}
	}
	if results[0].Requester != "first" || results[1].Requester != "second" {
		t.Fatal("requester changed")
	}
	if len(table.DropPlugin("p", "again")) != 0 {
		t.Fatal("duplicate exit produced results")
	}
	if table.Stats().Pending != 1 || table.Stats().PerPlugin[other.Plugin] != 1 {
		t.Fatal(table.Stats())
	}
	if _, ok := table.Resolve(other.Plugin, replyFor(other, `{}`)); !ok {
		t.Fatal("other plugin lost")
	}
	for _, h := range []Handle{p1, p2} {
		if _, ok := table.Resolve("p", replyFor(h, `{}`)); ok {
			t.Fatal("exited call survived")
		}
	}
	assertCounts(t, table)
}

func TestCancelIsLocalAndProducesNoOutcome(t *testing.T) {
	table, clock, box := newTestTable(TableOptions{})
	h := mustSend(t, table, "p")
	if table.Cancel(Handle{Plugin: "other", ID: h.ID}) {
		t.Fatal("wrong plugin cancelled")
	}
	if !table.Cancel(h) || table.Cancel(h) {
		t.Fatal("cancel not exactly once")
	}
	if len(box.messages) != 1 {
		t.Fatal("cancel wrote a notification")
	}
	clock.advance(time.Hour)
	if len(table.Expire(clock.Now())) != 0 || len(table.DropPlugin("p", "gone")) != 0 || len(table.Close()) != 0 {
		t.Fatal("cancel produced a result")
	}
	if _, ok := table.Resolve("p", replyFor(h, `{}`)); ok {
		t.Fatal("cancelled reply matched")
	}
	assertCounts(t, table)
}

func TestCloseOrdersAllResultsAndPermanentlyRefusesSend(t *testing.T) {
	table, clock, box := newTestTable(TableOptions{})
	late := mustSend(t, table, "z")
	q := mustSend(t, table, "q", Options{Timeout: MinTimeout, Requester: "r"})
	p := mustSend(t, table, "p", Options{Timeout: MinTimeout, Requester: "r"})
	clock.advance(500 * time.Millisecond)
	results := table.Close()
	want := []Handle{p, q, late}
	if len(results) != 3 {
		t.Fatal(results)
	}
	for i, r := range results {
		if r.Handle != want[i] || r.Err.Code != protocol.CodeCancelled || string(r.Err.Data) != `{"reason":"shutdown"}` || r.Err.Message != "husk is shutting down" || r.Elapsed != 500*time.Millisecond {
			t.Fatalf("result %d=%+v", i, r)
		}
	}
	if h, err := table.Send("p", "sample.call", nil, Options{Requester: "r"}); h != (Handle{}) || err != ErrClosed {
		t.Fatalf("%v %v", h, err)
	}
	if table.CanSend("p") != ErrClosed || len(table.Close()) != 0 || len(box.messages) != 3 {
		t.Fatal("close was not permanent/idempotent")
	}
	if _, ok := table.Resolve("p", replyFor(p, `{}`)); ok {
		t.Fatal("shutdown reply matched")
	}
	if table.Stats().Timeouts != 0 {
		t.Fatal("shutdown counted as timeout")
	}
	assertCounts(t, table)
}

func TestOnlyFirstCompletionProducesAResult(t *testing.T) {
	for _, first := range []string{"reply", "expire", "exit", "close", "cancel"} {
		t.Run(first, func(t *testing.T) {
			table, clock, _ := newTestTable(TableOptions{})
			h := mustSend(t, table, "p")
			clock.advance(DefaultTimeout)
			var got []Result
			resolve := func() {
				if r, ok := table.Resolve("p", replyFor(h, `{}`)); ok {
					got = append(got, r)
				}
			}
			switch first {
			case "reply":
				resolve()
			case "expire":
				got = append(got, table.Expire(clock.Now())...)
			case "exit":
				got = append(got, table.DropPlugin("p", "gone")...)
			case "close":
				got = append(got, table.Close()...)
			case "cancel":
				table.Cancel(h)
			}
			resolve()
			got = append(got, table.Expire(clock.Now())...)
			got = append(got, table.DropPlugin("p", "again")...)
			got = append(got, table.Close()...)
			want := 1
			if first == "cancel" {
				want = 0
			}
			if len(got) != want {
				t.Fatalf("outcomes=%v", got)
			}
			assertCounts(t, table)
		})
	}
}

func TestBulkResultsAreDeterministic(t *testing.T) {
	var expected []Handle
	for run := 0; run < 20; run++ {
		table, clock, _ := newTestTable(TableOptions{})
		for _, plugin := range []PluginID{"c", "a", "b", "a", "c"} {
			mustSend(t, table, plugin)
		}
		clock.advance(DefaultTimeout)
		results := table.Expire(clock.Now())
		var handles []Handle
		for _, r := range results {
			handles = append(handles, r.Handle)
		}
		if run == 0 {
			expected = handles
		} else if !reflect.DeepEqual(handles, expected) {
			t.Fatalf("run %d=%v want %v", run, handles, expected)
		}
	}
}

// Plugin ids may nest ("a" and "a.b"); DropPlugin matches the id exactly, so a
// parent's exit leaves the child's pending calls to resolve, and the child's own
// exit then cancels exactly those.
func TestDropPluginIsExactForDottedParentAndChild(t *testing.T) {
	table, _, _ := newTestTable(TableOptions{})
	parent := mustSend(t, table, "a")
	child := mustSend(t, table, "a.b")
	if results := table.DropPlugin("a", "exit"); len(results) != 1 || results[0].Handle != parent {
		t.Fatalf("parent drop = %v", results)
	}
	if table.Stats().PerPlugin[child.Plugin] != 1 {
		t.Fatal(table.Stats())
	}
	if _, ok := table.Resolve(child.Plugin, replyFor(child, `{}`)); !ok {
		t.Fatal("child call lost when the parent was dropped")
	}
	child2 := mustSend(t, table, "a.b")
	mustSend(t, table, "a")
	if results := table.DropPlugin("a.b", "exit"); len(results) != 1 || results[0].Handle != child2 {
		t.Fatalf("child drop = %v", results)
	}
}
