package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func (f *fakePort) Add(_ context.Context, c contracts.Caller, source contracts.PluginSource) (contracts.PluginAddition, error) {
	f.calls++
	f.gotKind = c.Kind
	f.gotID = source.Path
	if source.Dir != "" {
		f.gotID = source.Dir
	}
	return contracts.PluginAddition{Added: []contracts.PluginAdded{}, Refused: []contracts.PluginAddRefusal{}, Reports: []contracts.ReloadReport{}}, f.err
}

func TestAddParamsRejectBeforePortOrConversion(t *testing.T) {
	for _, tc := range []struct{ name, params, reason string }{
		{"absent", "", "missing_path"}, {"neither", `{}`, "missing_path"},
		{"both", `{"path":"/a","dir":"/b"}`, "path_and_dir"},
		{"both one empty", `{"path":"","dir":"/b"}`, "path_and_dir"},
		{"both null", `{"path":null,"dir":null}`, "path_and_dir"},
		{"empty", `{"path":""}`, "missing_path"}, {"empty dir", `{"dir":""}`, "missing_path"},
		{"number", `{"path":4}`, "missing_path"}, {"null", `{"path":null}`, "missing_path"},
		{"object", `{"dir":{}}`, "missing_path"}, {"array", `{"path":[]}`, "missing_path"},
		{"relative", `{"path":"./a"}`, "relative_path"}, {"relative dir", `{"dir":"a"}`, "relative_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &fakePort{}
			got := (Plugin{Port: port}).Serve(t.Context(), contracts.Caller{Kind: contracts.CallerCLI}, req(protocol.MethodPluginAdd, tc.params), nil)
			if got.Error == nil || got.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("reply = %+v", got)
			}
			var data map[string]any
			if err := json.Unmarshal(got.Error.Data, &data); err != nil || data["reason"] != tc.reason || port.calls != 0 {
				t.Fatalf("data = %s, calls=%d", got.Error.Data, port.calls)
			}
		})
	}
}

func TestAddDeniesEveryNonCLICallerBeforeDecode(t *testing.T) {
	for _, kind := range []contracts.CallerKind{contracts.CallerPlugin, contracts.CallerInput, contracts.CallerHost, ""} {
		port := &fakePort{}
		got := (Plugin{Port: port}).Serve(t.Context(), contracts.Caller{Kind: kind}, req(protocol.MethodPluginAdd, `{"path":{}}`), nil)
		if got.Error == nil || got.Error.Code != protocol.CodeDenied || port.calls != 0 {
			t.Fatalf("caller %s: %+v / calls=%d", kind, got, port.calls)
		}
	}
}

func TestAddForwardsSourceAndRemoteFailure(t *testing.T) {
	for _, field := range []string{"path", "dir"} {
		port := &fakePort{}
		p := Plugin{Port: port}
		request := req(protocol.MethodPluginAdd, `{"`+field+`":"/plugins/alpha"}`)
		got := p.Serve(t.Context(), contracts.Caller{Kind: contracts.CallerCLI}, request, nil)
		if got.Error != nil || port.calls != 1 || port.gotID != "/plugins/alpha" || port.gotKind != contracts.CallerCLI || string(got.Result) != `{"added":[],"refused":[],"reports":[]}` {
			t.Fatalf("reply = %+v; port = %+v", got, port)
		}
		port.err = contracts.NewError(protocol.CodeConflict, "already loaded", map[string]any{"reason": "duplicate_plugin_id"})
		got = p.Serve(t.Context(), contracts.Caller{Kind: contracts.CallerCLI}, request, nil)
		if got.Error == nil || got.Error.Code != protocol.CodeConflict {
			t.Fatalf("lost error: %+v", got)
		}
	}
}
