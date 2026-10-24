package shim

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestMissingServerThenNextActionRedials(t *testing.T) {
	r := newRig(t, "", true, "")
	var absent atomic.Bool
	absent.Store(true)
	dial := guardedDial(t, r.o.Socket)
	r.o.Client = upstream.New(upstream.Options{Socket: r.o.Socket, Clock: r.clock, Dial: func(ctx context.Context, network, path string) (net.Conn, error) {
		if absent.Load() {
			return nil, os.ErrNotExist
		}
		return dial(ctx, network, path)
	}})
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	first := r.invoke(t, "demo.go")
	if first.Error == nil || first.Error.Code != protocol.CodeUnavailable || !strings.Contains(first.Error.Message, "start herdr") || string(first.Error.Data) != `{"reason":"herdr_absent"}` {
		t.Fatalf("missing server reply=%+v", first)
	}
	select {
	case <-r.spawns:
		t.Fatal("absent server spawned command")
	default:
	}
	absent.Store(false)
	next := r.invoke(t, "demo.go")
	if next.Error != nil {
		t.Fatalf("next action did not redial: %v", next.Error)
	}
	take(t, r.spawns).process.done <- 0
}

func TestOnlyThreeInterceptionsForwardOtherBytes(t *testing.T) {
	r := newRig(t, "", true, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	spawned := take(t, r.spawns)
	path := envMap(spawned.command.Env)["HERDR_SOCKET_PATH"]
	for _, tc := range []struct{ method, params string }{
		{"notification.show", `{"title":"hello","body":"unchanged"}`},
		{"plugin.pane.focus", `{"pane_id":"upstream-pane"}`},
		{"plugin.pane.close", `{"pane_id":"upstream-pane"}`},
		{"vendor.unknown", `{"opaque":[1,2]}`},
	} {
		line := "{ \"id\" : \"forward\", \"method\" : " + strconvJSON(tc.method) + ", \"params\" : " + tc.params + " }\r\n"
		result := proxyCall(t, path, line)
		if result["error"] != nil {
			t.Fatalf("%s refused: %v", tc.method, result)
		}
		records := r.server.Requests()
		last := records[len(records)-1]
		if last.Method != tc.method || string(last.Raw) != line {
			t.Fatalf("%s request bytes changed: %q", tc.method, last.Raw)
		}
	}
	spawned.process.done <- 0
}
func strconvJSON(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestHooksAndBackgroundAttachmentAreNotRun(t *testing.T) {
	r := newRig(t, "", true, "[[startup]]\ncommand=['./startup']\n[[events]]\non='pane.focused'\ncommand=['./event']\n")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	if records := r.server.Requests(); len(records) != 0 {
		t.Fatalf("background upstream activity: %v", records)
	}
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	spawned := take(t, r.spawns)
	if spawned.command.Argv[0] != "./command" {
		t.Fatalf("hook ran instead of action: %v", spawned.command.Argv)
	}
	for _, req := range r.server.Requests() {
		if req.Method == "events.subscribe" || req.Method == "plugin.list" {
			t.Fatalf("hook setup request %s", req.Method)
		}
	}
	select {
	case extra := <-r.spawns:
		t.Fatalf("unexpected hook: %v", extra.command.Argv)
	default:
	}
	spawned.process.done <- 0
}
