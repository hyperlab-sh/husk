package handlers

import (
	"context"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// server.Notifier must satisfy contracts.Notifier for ServerHandler.Serve to
// pass one through unchanged.
var _ contracts.Notifier = server.Notifier(nil)

// recordingContractsHandler records the contracts caller and request it saw.
type recordingContractsHandler struct {
	caller contracts.Caller
	method string
}

func (h *recordingContractsHandler) Serve(_ context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	h.caller, h.method = c, req.Method
	return Reply(req, protocol.EmptyResult{})
}

func TestServerHandlerServes(t *testing.T) {
	h := &recordingContractsHandler{}
	adapted := ServerHandler{Handler: h}
	id := protocol.NewStringID("11")
	notifier := server.Notifier(nil)

	reply := adapted.Serve(context.Background(),
		server.Caller{Kind: server.CallerPlugin, PluginID: "atlas", ConnID: 42},
		protocol.Message{Method: protocol.MethodStateGet, ID: &id}, notifier)

	if reply.Error != nil {
		t.Fatalf("reply error = %+v", reply.Error)
	}
	if h.method != protocol.MethodStateGet {
		t.Fatalf("method = %s", h.method)
	}
	if h.caller.Kind != contracts.CallerPlugin || h.caller.PluginID != "atlas" || h.caller.ConnID != 42 {
		t.Fatalf("caller = %+v", h.caller)
	}
}

func TestFromServerCaller(t *testing.T) {
	cases := []struct {
		in   server.Caller
		want contracts.CallerKind
	}{
		{server.Caller{Kind: server.CallerCLI}, contracts.CallerCLI},
		{server.Caller{Kind: server.CallerCLI, PluginID: "", ConnID: 3}, contracts.CallerCLI},
		{server.Caller{Kind: server.CallerPlugin, PluginID: "p", ConnID: 4}, contracts.CallerPlugin},
	}
	for _, tc := range cases {
		got := FromServerCaller(tc.in)
		if got.Kind != tc.want || got.PluginID != tc.in.PluginID || got.ConnID != tc.in.ConnID {
			t.Fatalf("FromServerCaller(%+v) = %+v", tc.in, got)
		}
		if got.Kind == contracts.CallerStem {
			t.Fatal("a Stem caller never crosses the socket")
		}
	}
}
