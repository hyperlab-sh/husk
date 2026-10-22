package stem

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestHookCall(t *testing.T) {
	t.Run("encodes every subscriber outcome in one reply", func(t *testing.T) {
		port := fakes.NewHooks(nil)
		port.Results = []contracts.HookResult{
			{Plugin: "b", Data: []byte(`{"ready":true}`)},
			{Plugin: "c", Err: contracts.NewError(protocol.CodeTimeout, "no reply in 10s", nil)},
		}
		reply := Hook{Port: port}.Serve(ctx, plugin,
			req(protocol.MethodHookCall, `{"hook":"app.shutdown","data":{"reason":"update"}}`), nil)
		if reply.Error != nil {
			t.Fatalf("error = %+v", reply.Error)
		}
		want := `{"results":[{"plugin_id":"b","data":{"ready":true}},{"plugin_id":"c","error":{"code":"timeout","message":"no reply in 10s"}}]}`
		if string(reply.Result) != want {
			t.Fatalf("result = %s", reply.Result)
		}
	})
	t.Run("no subscribers reply with an empty results array", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, cli, req(protocol.MethodHookCall, `{"hook":"alone"}`), nil)
		if string(reply.Result) != `{"results":[]}` {
			t.Fatalf("result = %s", reply.Result)
		}
	})
	t.Run("missing hook", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, cli, req(protocol.MethodHookCall, `{}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("non-object data", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, cli,
			req(protocol.MethodHookCall, `{"hook":"h","data":7}`), nil)
		wantCode(t, reply, protocol.CodeInvalidParams)
	})
	t.Run("port error maps", func(t *testing.T) {
		port := fakes.NewHooks(nil)
		port.Err = contracts.NewError(protocol.CodeNotFound, "hook h is not declared", nil)
		reply := Hook{Port: port}.Serve(ctx, cli, req(protocol.MethodHookCall, `{"hook":"h"}`), nil)
		wantCode(t, reply, protocol.CodeNotFound)
	})
	t.Run("unknown method", func(t *testing.T) {
		reply := Hook{Port: fakes.NewHooks(nil)}.Serve(ctx, cli, req("hook.list", ""), nil)
		wantCode(t, reply, protocol.CodeUnknownMethod)
	})
}
