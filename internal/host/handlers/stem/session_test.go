package stem

import (
	"context"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var (
	ctx      = context.Background()
	cli      = contracts.Caller{Kind: contracts.CallerCLI}
	plugin   = contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "atlas"}
	stemSide = contracts.Caller{Kind: contracts.CallerStem}
	nobody   = contracts.Caller{} // D36: unattributed, denied everywhere
)

func req(method string, params string) protocol.Message {
	m := protocol.Message{Method: method}
	if params != "" {
		m.Params = []byte(params)
	}
	return m
}

func wantCode(t *testing.T, reply protocol.Message, code protocol.Code) {
	t.Helper()
	if reply.Error == nil {
		t.Fatalf("want error %s, got success %s", code, reply.Result)
	}
	if reply.Error.Code != code {
		t.Fatalf("code = %s (%s), want %s", reply.Error.Code, reply.Error.Message, code)
	}
}

func TestSessionPing(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSession(rec)
	port.Result = protocol.SessionPingResult{ID: "inst-1", PID: 42, ProtocolVersion: "2.0"}
	h := Session{Port: port}

	reply := h.Serve(ctx, cli, req(protocol.MethodSessionPing, ""), nil)
	if reply.Error != nil {
		t.Fatalf("error = %+v", reply.Error)
	}
	if string(reply.Result) != `{"id":"inst-1","pid":42,"protocol_version":"2.0","host_version":"","uptime_ms":0}` {
		t.Fatalf("result = %s", reply.Result)
	}
	if calls := rec.Methods(); len(calls) != 1 || calls[0] != "Session.Ping" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSessionPortError(t *testing.T) {
	port := fakes.NewSession(nil)
	port.Err = contracts.NewError(protocol.CodeUnavailable, "identity not ready", nil)
	reply := Session{Port: port}.Serve(ctx, cli, req(protocol.MethodSessionPing, ""), nil)
	wantCode(t, reply, protocol.CodeUnavailable)
}

// session.ping is any-caller (CLI or plugin): every other kind — Stem
// in-process, the unattributed zero kind — is denied before the port.
func TestSessionAdmissionDenied(t *testing.T) {
	for name, caller := range map[string]contracts.Caller{"stem": stemSide, "unattributed": nobody} {
		t.Run(name, func(t *testing.T) {
			rec := fakes.NewRecorder()
			reply := Session{Port: fakes.NewSession(rec)}.Serve(ctx, caller, req(protocol.MethodSessionPing, ""), nil)
			wantCode(t, reply, protocol.CodeDenied)
			if calls := rec.Methods(); len(calls) != 0 {
				t.Fatalf("denied caller reached the port: %v", calls)
			}
		})
	}
}

func TestSessionUnknownMethod(t *testing.T) {
	port := fakes.NewSession(nil)
	reply := Session{Port: port}.Serve(ctx, cli, req("session.stats", ""), nil)
	wantCode(t, reply, protocol.CodeUnknownMethod)
	if len(port.Calls()) != 0 {
		t.Fatal("port must not be called for an unknown method")
	}
}
