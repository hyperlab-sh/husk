package stem

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestStateGet(t *testing.T) {
	t.Run("happy path passes slice and path", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		port.Read = contracts.StateReadResult{Slice: "atlas.state", Owner: "atlas", Revision: 4, Value: []byte(`{"a":1}`)}
		reply := State{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodStateGet, `{"slice":"atlas.state","path":"/a"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		want := `{"slice":"atlas.state","owner":"atlas","revision":4,"value":{"a":1}}`
		if string(reply.Result) != want {
			t.Fatalf("result = %s", reply.Result)
		}
		if calls := rec.Calls(); calls[0].Args[0] != "atlas.state" || calls[0].Args[1] != "/a" {
			t.Fatalf("args = %v", calls[0].Args)
		}
	})
	t.Run("missing slice", func(t *testing.T) {
		reply := State{Port: fakes.NewState(nil)}.Serve(ctx, plugin, req(protocol.MethodStateGet, `{}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
}

func TestStateSet(t *testing.T) {
	t.Run("happy path returns the new revision", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		port.Revision = 9
		reply := State{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodStateSet, `{"slice":"atlas.state","value":{"done":true}}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if string(reply.Result) != `{"slice":"atlas.state","revision":9}` {
			t.Fatalf("result = %s", reply.Result)
		}
		if calls := rec.Calls(); calls[0].Args[0] != "atlas.state" || string(calls[0].Args[1].(json.RawMessage)) != `{"done":true}` {
			t.Fatalf("args = %v", calls[0].Args)
		}
	})
	t.Run("cli caller denied before the port", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		reply := State{Port: port}.Serve(ctx, cli,
			req(protocol.MethodStateSet, `{"slice":"x.state","value":{}}`), nil)
		wantCode(t, reply, protocol.CodeDenied)
		if len(port.Calls()) != 0 {
			t.Fatal("port must not be called for a denied caller")
		}
	})
	t.Run("missing value", func(t *testing.T) {
		reply := State{Port: fakes.NewState(nil)}.Serve(ctx, plugin,
			req(protocol.MethodStateSet, `{"slice":"atlas.state"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("non-object value", func(t *testing.T) {
		reply := State{Port: fakes.NewState(nil)}.Serve(ctx, plugin,
			req(protocol.MethodStateSet, `{"slice":"atlas.state","value":[1]}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("port conflict maps with data", func(t *testing.T) {
		port := fakes.NewState(nil)
		port.Err = contracts.NewError(protocol.CodeConflict, "stale revision", map[string]any{"revision": 3})
		reply := State{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodStatePatch, `{"slice":"atlas.state","patch":{},"if_revision":3}`), nil)
		wantCode(t, reply, protocol.CodeConflict)
		if string(reply.Error.Data) != `{"revision":3}` {
			t.Fatalf("data = %s", reply.Error.Data)
		}
	})
}

func TestStatePatch(t *testing.T) {
	t.Run("if_revision decodes and defaults to zero", func(t *testing.T) {
		cases := []struct {
			name   string
			params string
			want   uint64
		}{
			{"absent", `{"slice":"atlas.state","patch":{"a":1}}`, 0},
			{"present", `{"slice":"atlas.state","patch":{"a":1},"if_revision":7}`, 7},
			{"zero", `{"slice":"atlas.state","patch":{"a":1},"if_revision":0}`, 0},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := fakes.NewRecorder()
				port := fakes.NewState(rec)
				if reply := (State{Port: port}).Serve(ctx, plugin, req(protocol.MethodStatePatch, tc.params), nil); reply.Error != nil {
					t.Fatalf("error = %+v", reply.Error)
				}
				if got := rec.Calls()[0].Args[1]; got != tc.want {
					t.Fatalf("if_revision = %v, want %v", got, tc.want)
				}
			})
		}
	})
	t.Run("missing patch", func(t *testing.T) {
		reply := State{Port: fakes.NewState(nil)}.Serve(ctx, plugin,
			req(protocol.MethodStatePatch, `{"slice":"atlas.state"}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
}

func TestStateSnapshot(t *testing.T) {
	t.Run("values flag passes through", func(t *testing.T) {
		cases := []struct {
			name   string
			params string
			want   bool
		}{
			{"absent", `{}`, false},
			{"false", `{"values":false}`, false},
			{"true", `{"values":true}`, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := fakes.NewRecorder()
				port := fakes.NewState(rec)
				if reply := (State{Port: port}).Serve(ctx, cli, req(protocol.MethodStateSnapshot, tc.params), nil); reply.Error != nil {
					t.Fatalf("error = %+v", reply.Error)
				}
				if got := rec.Calls()[0].Args[0]; got != tc.want {
					t.Fatalf("values = %v, want %v", got, tc.want)
				}
			})
		}
	})
	t.Run("result encodes", func(t *testing.T) {
		port := fakes.NewState(nil)
		port.Snap = contracts.StateSnapshotResult{Seq: 5, Slices: []contracts.StateSlice{{Slice: "husk.focus"}}}
		reply := State{Port: port}.Serve(ctx, cli, req(protocol.MethodStateSnapshot, `{}`), nil)
		want := `{"seq":5,"slices":[{"slice":"husk.focus","owner":"","scope":"","persistent":false,"revision":0,"bytes":0,"hydrated":false}]}`
		if string(reply.Result) != want {
			t.Fatalf("result = %s", reply.Result)
		}
	})
}

func TestStateWatch(t *testing.T) {
	t.Run("passes slices and notifier", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		port.Snap = contracts.StateSnapshotResult{Seq: 1, Slices: []contracts.StateSlice{}}
		n := &noteRecorder{}
		reply := State{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodStateWatch, `{"slices":["husk.focus","atlas.state"]}`), n)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if string(reply.Result) != `{"seq":1,"slices":[]}` {
			t.Fatalf("result = %s", reply.Result)
		}
		if calls := rec.Calls(); len(calls[0].Args) != 1 {
			t.Fatalf("slice args = %v", calls[0].Args)
		}
	})
	t.Run("absent slices decode to nil", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		if reply := (State{Port: port}).Serve(ctx, plugin, req(protocol.MethodStateWatch, `{}`), nil); reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if got, _ := rec.Calls()[0].Args[0].([]string); len(got) != 0 {
			t.Fatalf("slices = %#v, want empty", got)
		}
	})
}

func TestStateClear(t *testing.T) {
	t.Run("user caller reaches the port", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		reply := State{Port: port}.Serve(ctx, cli, req(protocol.MethodStateClear, `{"namespace":"atlas.state"}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); calls[0].Args[0] != "atlas.state" || calls[0].Args[1] != false {
			t.Fatalf("args = %v", calls[0].Args)
		}
	})
	t.Run("force reaches the port", func(t *testing.T) {
		rec := fakes.NewRecorder()
		reply := State{Port: fakes.NewState(rec)}.Serve(ctx, cli, req(protocol.MethodStateClear, `{"namespace":"atlas.state","force":true}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		if calls := rec.Calls(); calls[0].Args[1] != true {
			t.Fatalf("args = %v", calls[0].Args)
		}
	})
	t.Run("plugin caller denied", func(t *testing.T) {
		rec := fakes.NewRecorder()
		port := fakes.NewState(rec)
		reply := State{Port: port}.Serve(ctx, plugin, req(protocol.MethodStateClear, `{"namespace":"atlas.state"}`), nil)
		wantCode(t, reply, protocol.CodeDenied)
		if len(port.Calls()) != 0 {
			t.Fatal("port must not be called for a denied caller")
		}
	})
	t.Run("missing namespace", func(t *testing.T) {
		reply := State{Port: fakes.NewState(nil)}.Serve(ctx, cli, req(protocol.MethodStateClear, `{}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("stem and unattributed callers denied on the cli-only clear", func(t *testing.T) {
		for name, caller := range map[string]contracts.Caller{"stem": stemSide, "unattributed": nobody} {
			rec := fakes.NewRecorder()
			reply := State{Port: fakes.NewState(rec)}.Serve(ctx, caller, req(protocol.MethodStateClear, `{"namespace":"atlas.state"}`), nil)
			wantCode(t, reply, protocol.CodeDenied)
			if calls := rec.Methods(); len(calls) != 0 {
				t.Fatalf("%s: denied caller reached the port: %v", name, calls)
			}
		}
	})
}

// state.get is any-caller: the unattributed zero kind is denied (D36).
func TestStateGetUnattributedDenied(t *testing.T) {
	rec := fakes.NewRecorder()
	reply := State{Port: fakes.NewState(rec)}.Serve(ctx, nobody, req(protocol.MethodStateGet, `{"slice":"atlas.state"}`), nil)
	wantCode(t, reply, protocol.CodeDenied)
	if calls := rec.Methods(); len(calls) != 0 {
		t.Fatalf("denied caller reached the port: %v", calls)
	}
}

func TestStateUnknownMethod(t *testing.T) {
	reply := State{Port: fakes.NewState(nil)}.Serve(ctx, cli, req("state.compact", ""), nil)
	wantCode(t, reply, protocol.CodeUnknownMethod)
}
