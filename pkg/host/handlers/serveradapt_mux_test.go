package handlers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	stemadapters "github.com/hyperlab-sh/husk/pkg/host/handlers/stem"
	"github.com/hyperlab-sh/husk/pkg/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestServerHandlerThroughMux routes a real server.Caller through
// ServerHandler over a Mux with the Stem adapters registered over fakes:
// the full attribution → admission → port path one socket request takes.
func TestServerHandlerThroughMux(t *testing.T) {
	rec := fakes.NewRecorder()
	mux := handlers.NewMux()
	stemadapters.Register(mux, stemadapters.Ports{
		Session: fakes.NewSession(rec),
		Calls:   fakes.NewCalls(rec),
		Event:   fakes.NewEvents(rec),
		Hook:    fakes.NewHooks(rec),
		Surface: fakes.NewSurfaces(rec),
		State:   fakes.NewState(rec),
	})
	adapted := handlers.ServerHandler{Handler: mux}
	ctx := context.Background()
	id := protocol.NewStringID("31")
	setReq := protocol.Message{
		Method: protocol.MethodStateSet,
		ID:     &id,
		Params: json.RawMessage(`{"slice":"atlas.state","value":{"a":1}}`),
	}

	t.Run("attributed plugin caller reaches the port", func(t *testing.T) {
		reply := adapted.Serve(ctx,
			server.Caller{Kind: server.CallerPlugin, PluginID: "atlas", ConnID: 9},
			setReq, server.Notifier(nil))
		if reply.Error != nil {
			t.Fatalf("reply error = %+v", reply.Error)
		}
		calls := rec.Calls()
		if len(calls) != 1 || calls[0].Method != "State.Set" {
			t.Fatalf("port calls = %+v", calls)
		}
		c := calls[0]
		if c.Kind != contracts.CallerPlugin || c.PluginID != "atlas" {
			t.Fatalf("converted caller = %+v", c)
		}
	})

	t.Run("cli caller is denied before the port", func(t *testing.T) {
		rec.Reset()
		reply := adapted.Serve(ctx,
			server.Caller{Kind: server.CallerCLI, ConnID: 9},
			setReq, server.Notifier(nil))
		if reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
			t.Fatalf("reply error = %+v, want denied", reply.Error)
		}
		if calls := rec.Calls(); len(calls) != 0 {
			t.Fatalf("denied request reached the port: %+v", calls)
		}
	})

	t.Run("unattributed caller is denied before the port", func(t *testing.T) {
		rec.Reset()
		reply := adapted.Serve(ctx,
			server.Caller{ConnID: 9},
			setReq, server.Notifier(nil))
		if reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
			t.Fatalf("reply error = %+v, want denied", reply.Error)
		}
		if calls := rec.Calls(); len(calls) != 0 {
			t.Fatalf("unattributed request reached the port: %+v", calls)
		}
	})
}
