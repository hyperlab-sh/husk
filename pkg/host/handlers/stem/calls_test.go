package stem

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// action.invoke accepts CLI, plugin and Stem callers; the unattributed zero
// kind is denied. action.list is any-caller, so Stem is wrong there too.
func TestCallsAdmissionDenied(t *testing.T) {
	t.Run("unattributed invoke", func(t *testing.T) {
		rec := fakes.NewRecorder()
		reply := Calls{Port: fakes.NewCalls(rec)}.Serve(ctx, nobody,
			req(protocol.MethodActionInvoke, `{"action":"go"}`), nil)
		wantCode(t, reply, protocol.CodeDenied)
		if calls := rec.Methods(); len(calls) != 0 {
			t.Fatalf("denied caller reached the port: %v", calls)
		}
	})
	for name, caller := range map[string]contracts.Caller{"stem": stemSide, "unattributed": nobody} {
		t.Run(name+" list", func(t *testing.T) {
			rec := fakes.NewRecorder()
			reply := Calls{Port: fakes.NewCalls(rec)}.Serve(ctx, caller, req(protocol.MethodActionList, ""), nil)
			wantCode(t, reply, protocol.CodeDenied)
			if calls := rec.Methods(); len(calls) != 0 {
				t.Fatalf("denied caller reached the port: %v", calls)
			}
		})
	}
}

func TestCallsInvoke(t *testing.T) {
	t.Run("happy path passes action and args", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewCalls(rec)
		port.Result = []byte(`{"done":true}`)
		reply := Calls{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodActionInvoke, `{"action":"refactor","args":{"scope":"file"}}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if string(reply.Result) != `{"done":true}` {
			t.Fatalf("result = %s", reply.Result)
		}
		calls := rec.Calls()
		if len(calls) != 1 || calls[0].Method != "Calls.ActionInvoke" || calls[0].PluginID != "atlas" {
			t.Fatalf("calls = %v", calls)
		}
		if got := calls[0].Args[0]; got != "refactor" {
			t.Fatalf("action arg = %v", got)
		}
	})
	t.Run("absent args decode to nil", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewCalls(rec)
		if reply := (Calls{Port: port}).Serve(ctx, plugin, req(protocol.MethodActionInvoke, `{"action":"go"}`), nil); reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		calls := rec.Calls()
		if got, _ := calls[0].Args[1].(json.RawMessage); len(got) != 0 {
			t.Fatalf("args = %#v, want empty", calls[0].Args[1])
		}
	})
	t.Run("null args decode to nil", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewCalls(rec)
		if reply := (Calls{Port: port}).Serve(ctx, plugin, req(protocol.MethodActionInvoke, `{"action":"go","args":null}`), nil); reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if got, _ := rec.Calls()[0].Args[1].(json.RawMessage); len(got) != 0 {
			t.Fatalf("args = %#v, want empty", rec.Calls()[0].Args[1])
		}
	})
	t.Run("empty result encodes empty object", func(t *testing.T) {
		port := fakes.NewCalls(nil)
		reply := Calls{Port: port}.Serve(ctx, plugin, req(protocol.MethodActionInvoke, `{"action":"go"}`), nil)
		if string(reply.Result) != "{}" {
			t.Fatalf("result = %s", reply.Result)
		}
	})
	t.Run("missing action", func(t *testing.T) {
		reply := Calls{Port: fakes.NewCalls(nil)}.Serve(ctx, plugin,
			req(protocol.MethodActionInvoke, `{"args":{}}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("non-object args", func(t *testing.T) {
		reply := Calls{Port: fakes.NewCalls(nil)}.Serve(ctx, plugin,
			req(protocol.MethodActionInvoke, `{"action":"go","args":[1,2]}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("mistyped action", func(t *testing.T) {
		reply := Calls{Port: fakes.NewCalls(nil)}.Serve(ctx, plugin,
			req(protocol.MethodActionInvoke, `{"action":42}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("params not an object", func(t *testing.T) {
		reply := Calls{Port: fakes.NewCalls(nil)}.Serve(ctx, plugin,
			req(protocol.MethodActionInvoke, `[1]`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("stem caller may invoke", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewCalls(rec)
		reply := Calls{Port: port}.Serve(ctx, stemSide, req(protocol.MethodActionInvoke, `{"action":"go"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); calls[0].Kind != contracts.CallerHost {
			t.Fatalf("kind = %s", calls[0].Kind)
		}
	})
}

func TestCallsList(t *testing.T) {
	t.Run("encodes entries", func(t *testing.T) {
		port := fakes.NewCalls(nil)
		port.Actions = []contracts.ActionInfo{{ID: "go", Owner: "atlas", Public: true, Live: true}}
		reply := Calls{Port: port}.Serve(ctx, cli, req(protocol.MethodActionList, ""), nil)
		if string(reply.Result) != `[{"id":"go","owner":"atlas","public":true,"live":true}]` {
			t.Fatalf("result = %s", reply.Result)
		}
	})
	t.Run("nil entries encode as empty array", func(t *testing.T) {
		reply := Calls{Port: fakes.NewCalls(nil)}.Serve(ctx, cli, req(protocol.MethodActionList, ""), nil)
		if string(reply.Result) != `[]` {
			t.Fatalf("result = %s", reply.Result)
		}
	})
	t.Run("port error maps", func(t *testing.T) {
		port := fakes.NewCalls(nil)
		port.Err = contracts.NewError(protocol.CodeFailed, "catalog unavailable", nil)
		reply := Calls{Port: port}.Serve(ctx, cli, req(protocol.MethodActionList, ""), nil)
		wantCode(t, reply, protocol.CodeFailed)
	})
}

func TestCallsUnknownMethod(t *testing.T) {
	reply := Calls{Port: fakes.NewCalls(nil)}.Serve(ctx, cli, req("action.drop", ""), nil)
	wantCode(t, reply, protocol.CodeUnknownMethod)
}
