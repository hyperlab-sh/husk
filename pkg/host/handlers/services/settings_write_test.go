package services

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSettingsSetPreservesJSONNumberPrecision(t *testing.T) {
	rec := fakes.NewRecorder()
	h := Settings{Port: fakes.NewSettings(rec)}
	r := h.Serve(context.Background(), cliCaller, req(t, protocol.MethodSettingsSet, `{"key":"dist.count","value":9223372036854775807}`), nil)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	calls := rec.Calls()
	if len(calls) != 1 || calls[0].Args[1] != json.Number("9223372036854775807") {
		t.Fatalf("%+v", calls)
	}
}

func TestSettingsSetDenialBeforeValueDecode(t *testing.T) {
	for _, tc := range []struct {
		caller contracts.Caller
		reason string
	}{
		{pluginCaller("unit"), "user_only"},
		{contracts.Caller{Kind: contracts.CallerPlugin}, "user_only"},
		{contracts.Caller{}, "invalid_caller"},
		{contracts.Caller{Kind: contracts.CallerInput}, "invalid_caller"},
		{contracts.Caller{Kind: contracts.CallerHost}, "invalid_caller"},
		{contracts.Caller{Kind: contracts.CallerCLI, PluginID: "unit"}, "invalid_caller"},
	} {
		rec := fakes.NewRecorder()
		h := Settings{Port: fakes.NewSettings(rec)}
		r := h.Serve(context.Background(), tc.caller, protocol.Message{Method: protocol.MethodSettingsSet, Params: json.RawMessage(`{"key":42,"value":`)}, nil)
		var data map[string]any
		if r.Error == nil || r.Error.Code != protocol.CodeDenied || json.Unmarshal(r.Error.Data, &data) != nil || data["reason"] != tc.reason || len(rec.Methods()) != 0 {
			t.Fatalf("%+v reply=%+v data=%v calls=%v", tc.caller, r.Error, data, rec.Methods())
		}
	}
}
