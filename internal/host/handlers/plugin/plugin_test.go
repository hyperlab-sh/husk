package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type fakePort struct {
	infos   []contracts.PluginInfo
	path    string
	err     error
	gotID   string
	gotKind contracts.CallerKind
	calls   int
}

func (f *fakePort) List(_ context.Context, c contracts.Caller) ([]contracts.PluginInfo, error) {
	f.calls++
	f.gotKind = c.Kind
	return f.infos, f.err
}

func (f *fakePort) Log(_ context.Context, c contracts.Caller, id string) (string, error) {
	f.calls++
	f.gotKind, f.gotID = c.Kind, id
	return f.path, f.err
}

func req(method, params string) protocol.Message {
	id := protocol.NewStringID("1")
	m := protocol.Message{ID: &id, Method: method}
	if params != "" {
		m.Params = json.RawMessage(params)
	}
	return m
}

func code(m protocol.Message) protocol.Code {
	if m.Error == nil {
		return ""
	}
	return m.Error.Code
}

func TestServe(t *testing.T) {
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	plug := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "p"}
	tests := []struct {
		name     string
		caller   contracts.Caller
		req      protocol.Message
		port     *fakePort
		wantCode protocol.Code
		wantBody string
		wantCall int
	}{
		{"list", cli, req(protocol.MethodPluginList, ""), &fakePort{infos: []contracts.PluginInfo{{ID: "a", Status: contracts.PodLive}}}, "", `"id":"a"`, 1},
		{"list empty is an array", cli, req(protocol.MethodPluginList, ""), &fakePort{}, "", `[]`, 1},
		{"list denies plugin before the port", plug, req(protocol.MethodPluginList, ""), &fakePort{}, protocol.CodeDenied, "", 0},
		{"log", cli, req(protocol.MethodPluginLog, `{"plugin":"a"}`), &fakePort{path: "/x/a.log"}, "", `{"path":"/x/a.log"}`, 1},
		{"log denies plugin before the port", plug, req(protocol.MethodPluginLog, `{"plugin":"a"}`), &fakePort{}, protocol.CodeDenied, "", 0},
		{"log needs a plugin", cli, req(protocol.MethodPluginLog, `{}`), &fakePort{}, protocol.CodeInvalidParams, "", 0},
		{"log rejects wrong type", cli, req(protocol.MethodPluginLog, `{"plugin":3}`), &fakePort{}, protocol.CodeInvalidParams, "", 0},
		{"log not found passes the code", cli, req(protocol.MethodPluginLog, `{"plugin":"zz"}`), &fakePort{err: contracts.NewError(protocol.CodeNotFound, "nope", nil)}, protocol.CodeNotFound, "", 1},
		{"other plugin method is unknown", cli, req("plugin.restart", ""), &fakePort{}, protocol.CodeUnknownMethod, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Plugin{Port: tc.port}.Serve(context.Background(), tc.caller, tc.req, nil)
			if code(got) != tc.wantCode {
				t.Fatalf("code = %q, want %q (%+v)", code(got), tc.wantCode, got.Error)
			}
			if tc.wantBody != "" && !contains(string(got.Result), tc.wantBody) {
				t.Fatalf("result = %s, want it to contain %s", got.Result, tc.wantBody)
			}
			if tc.port.calls != tc.wantCall {
				t.Fatalf("port calls = %d, want %d", tc.port.calls, tc.wantCall)
			}
		})
	}
}

func TestLogPassesPluginIDAndCaller(t *testing.T) {
	port := &fakePort{path: "p"}
	Plugin{Port: port}.Serve(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, req(protocol.MethodPluginLog, `{"plugin":"a.b"}`), nil)
	if port.gotID != "a.b" || port.gotKind != contracts.CallerCLI {
		t.Fatalf("port saw id %q kind %v", port.gotID, port.gotKind)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
