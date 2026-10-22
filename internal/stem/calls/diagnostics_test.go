package calls

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestOrphanLogsAreRateLimitedPerPluginWithSuppressedCount(t *testing.T) {
	var logs []string
	table, clock, _ := newTestTable(TableOptions{Logger: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	clock.now = time.Time{} // the zero time is a valid first log timestamp
	orphans := uint64(0)
	orphan := func(plugin PluginID) {
		orphans++
		if _, ok := table.Resolve(plugin, protocol.Message{}); ok {
			t.Fatal("orphan matched")
		}
	}
	orphan("p")
	orphan("p")
	orphan("p")
	orphan("q")
	if len(logs) != 2 {
		t.Fatal(logs)
	}
	clock.advance(time.Second - time.Nanosecond)
	orphan("p")
	if len(logs) != 2 {
		t.Fatal("logged inside cooldown")
	}
	clock.advance(time.Nanosecond)
	orphan("p")
	if len(logs) != 3 || !strings.Contains(logs[2], "3 suppressed") {
		t.Fatal(logs)
	}
	clock.advance(time.Second)
	orphan("p")
	if len(logs) != 4 || !strings.Contains(logs[3], "0 suppressed") {
		t.Fatal(logs)
	}
	if table.Stats().Orphans != orphans {
		t.Fatal(table.Stats())
	}
	if _, ok := table.NextSweep(); ok {
		t.Fatal("orphan diagnostics started a sweep")
	}
}

func TestOrphanLogTruncatesUntrustedIDs(t *testing.T) {
	var logs []string
	table, _, _ := newTestTable(TableOptions{Logger: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	id := protocol.NewStringID(strings.Repeat("x", 1000) + "\n")
	_, _ = table.Resolve("p", protocol.Message{ID: &id})
	if len(logs) != 1 || len(logs[0]) > 256 || strings.Contains(logs[0], "\n") || !strings.Contains(logs[0], "...") {
		t.Fatal(logs)
	}
}

func TestNilLoggerStillCountsOrphans(t *testing.T) {
	table, _, _ := newTestTable(TableOptions{})
	for i := 0; i < 5; i++ {
		_, _ = table.Resolve("p", protocol.Message{})
	}
	if table.Stats().Orphans != 5 || len(table.orphanLogs) != 0 {
		t.Fatal(table.Stats())
	}
}

func TestFuturePluginErrorIsForwardedAfterProtocolNormalization(t *testing.T) {
	table, _, _ := newTestTable(TableOptions{})
	h := mustSend(t, table, "p")
	var reply protocol.Message
	raw := fmt.Sprintf(`{"id":%d,"error":{"code":"future_minor_code","message":"future failure","data":{"future":1}}}`, h.ID)
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatal(err)
	}
	r, ok := table.Resolve("p", reply)
	if !ok || r.Err != reply.Error || r.Err.Code != protocol.CodeInternal || string(r.Err.Data) != `{"future":1}` || r.Err.Message != "future failure" {
		t.Fatalf("%+v %v", r, ok)
	}
}
