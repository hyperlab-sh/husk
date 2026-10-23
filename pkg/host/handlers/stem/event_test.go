package stem

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type noteRecorder struct {
	got []protocol.Message
	err error
}

func (n *noteRecorder) Notify(m protocol.Message) error {
	n.got = append(n.got, m)
	return n.err
}

// event.emit and event.watch are any-caller: Stem and the unattributed
// zero kind are denied before the port.
func TestEventAdmissionDenied(t *testing.T) {
	for name, caller := range map[string]contracts.Caller{"stem": stemSide, "unattributed": nobody} {
		for _, method := range []string{protocol.MethodEventEmit, protocol.MethodEventWatch} {
			t.Run(name+" "+method, func(t *testing.T) {
				rec := fakes.NewRecorder()
				reply := Event{Port: fakes.NewEvents(rec)}.Serve(ctx, caller,
					req(method, `{"name":"file.saved","pattern":"*"}`), nil)
				wantCode(t, reply, protocol.CodeDenied)
				if calls := rec.Methods(); len(calls) != 0 {
					t.Fatalf("denied caller reached the port: %v", calls)
				}
			})
		}
	}
}

func TestEventEmit(t *testing.T) {
	t.Run("happy path passes name and data", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewEvents(rec)
		reply := Event{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodEventEmit, `{"name":"file.saved","data":{"path":"main.go"}}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if string(reply.Result) != "{}" {
			t.Fatalf("result = %s", reply.Result)
		}
		calls := rec.Calls()
		if len(calls) != 1 || calls[0].Method != "Events.Emit" {
			t.Fatalf("calls = %v", calls)
		}
		if calls[0].Args[0] != "file.saved" {
			t.Fatalf("name = %v", calls[0].Args[0])
		}
		if string(calls[0].Args[1].(json.RawMessage)) != `{"path":"main.go"}` {
			t.Fatalf("data = %s", calls[0].Args[1])
		}
	})
	t.Run("absent data decodes to nil", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewEvents(rec)
		if reply := (Event{Port: port}).Serve(ctx, plugin, req(protocol.MethodEventEmit, `{"name":"tick"}`), nil); reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if got, _ := rec.Calls()[0].Args[1].(json.RawMessage); len(got) != 0 {
			t.Fatalf("data = %#v, want empty", got)
		}
	})
	t.Run("non-object data", func(t *testing.T) {
		reply := Event{Port: fakes.NewEvents(nil)}.Serve(ctx, plugin,
			req(protocol.MethodEventEmit, `{"name":"tick","data":"nope"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("missing name", func(t *testing.T) {
		reply := Event{Port: fakes.NewEvents(nil)}.Serve(ctx, plugin, req(protocol.MethodEventEmit, `{}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("port error maps", func(t *testing.T) {
		port := fakes.NewEvents(nil)
		port.Err = contracts.NewError(protocol.CodeBusy, "event queue full", nil)
		reply := Event{Port: port}.Serve(ctx, plugin, req(protocol.MethodEventEmit, `{"name":"tick"}`), nil)
		wantCode(t, reply, protocol.CodeBusy)
	})
}

func TestEventWatch(t *testing.T) {
	t.Run("registers the connection notifier", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewEvents(rec)
		n := &noteRecorder{}
		reply := Event{Port: port}.Serve(ctx, plugin, req(protocol.MethodEventWatch, `{"pattern":"file.*"}`), n)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); len(calls) != 1 || calls[0].Args[0] != "file.*" {
			t.Fatalf("calls = %v", calls)
		}
		if port.Watching() != 1 {
			t.Fatalf("watchers = %d", port.Watching())
		}
		// The notifier registered with the port is the one the adapter got.
		delivered := port.Push(protocol.Message{Method: protocol.MethodEventDeliver, Params: []byte(`{}`)})
		if delivered != 1 || len(n.got) != 1 {
			t.Fatalf("delivered = %d, notified = %d", delivered, len(n.got))
		}
	})
	t.Run("missing pattern", func(t *testing.T) {
		reply := Event{Port: fakes.NewEvents(nil)}.Serve(ctx, plugin, req(protocol.MethodEventWatch, `{}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("port refusal maps", func(t *testing.T) {
		port := fakes.NewEvents(nil)
		port.Err = contracts.NewError(protocol.CodeInvalidParams, "bad pattern", nil)
		reply := Event{Port: port}.Serve(ctx, plugin, req(protocol.MethodEventWatch, `{"pattern":"["}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
}

func TestEventUnknownMethod(t *testing.T) {
	reply := Event{Port: fakes.NewEvents(nil)}.Serve(ctx, plugin, req("event.peek", ""), nil)
	wantCode(t, reply, protocol.CodeUnknownMethod)
}
