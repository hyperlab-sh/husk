package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func decode(t *testing.T, m protocol.Message) map[string]any {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("unexpected error reply: %+v", m.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(m.Result, &out); err != nil {
		t.Fatalf("result %s: %v", m.Result, err)
	}
	return out
}

func replyErr(t *testing.T, m protocol.Message) *protocol.Error {
	t.Helper()
	if m.Error == nil {
		t.Fatalf("expected error reply, got result %s", m.Result)
	}
	return m.Error
}

func req(t *testing.T, method, params string) protocol.Message {
	t.Helper()
	id, err := protocol.NewIntID(7)
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	return protocol.Message{ID: &id, Method: method, Params: json.RawMessage(params)}
}

func TestSettingsGetServesPluginAndCLI(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSettings(rec)
	port.Value = "monokai"
	port.Source = "user"
	h := Settings{Port: port}

	for _, caller := range []contracts.Caller{pluginCaller("files"), cliCaller} {
		reply := h.Serve(context.Background(), caller, req(t, "settings.get", `{"key":"theme.name"}`), nil)
		if caller.Kind == contracts.CallerPlugin && reply.Error != nil {
			t.Fatalf("plugin get denied: %+v", reply.Error)
		}
		got := decode(t, reply)
		if got["key"] != "theme.name" || got["value"] != "monokai" || got["source"] != "user" {
			t.Fatalf("result = %v", got)
		}
	}
	calls := rec.Methods()
	if len(calls) != 2 || calls[0] != "Settings.Get" || calls[1] != "Settings.Get" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSettingsGetDecodeErrors(t *testing.T) {
	port := fakes.NewSettings(nil)
	h := Settings{Port: port}
	for _, tc := range []struct {
		name   string
		params string
		field  string
	}{
		{"absent key", `{}`, "params.key"},
		{"empty key", `{"key":""}`, "params.key"},
		{"wrong type", `{"key":3}`, "params.key"},
		{"params not object", `["x"]`, "params"},
	} {
		errp := replyErr(t, h.Serve(context.Background(), cliCaller, req(t, "settings.get", tc.params), nil))
		if errp.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s: code = %s", tc.name, errp.Code)
		}
		var data map[string]string
		if err := json.Unmarshal(errp.Data, &data); err != nil || data["field"] != tc.field {
			t.Fatalf("%s: data = %s", tc.name, errp.Data)
		}
	}
}

func TestSettingsCLIMethodsDeniedToPluginBeforePort(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSettings(rec)
	h := Settings{Port: port}
	for _, method := range []string{"settings.list", "settings.schema", "settings.validate"} {
		reply := h.Serve(context.Background(), pluginCaller("files"), protocol.Message{Method: method}, nil)
		errp := replyErr(t, reply)
		if errp.Code != protocol.CodeDenied {
			t.Fatalf("%s: code = %s, want denied", method, errp.Code)
		}
	}
	if calls := rec.Methods(); len(calls) != 0 {
		t.Fatalf("port touched: %v", calls)
	}
}

func TestSettingsListSchemaValidateForCLI(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSettings(rec)
	port.Values = map[string]any{"theme.name": "mono"}
	port.Entries = []contracts.SettingEntry{{Key: "theme.name", Type: "string", Owner: "husk", Apply: "live", Doc: "theme"}}
	port.Validation = contracts.ValidationResult{Applied: true}
	h := Settings{Port: port}

	list := decode(t, h.Serve(context.Background(), cliCaller, req(t, "settings.list", `{}`), nil))
	if list["theme.name"] != "mono" {
		t.Fatalf("list = %v", list)
	}
	var entries []contracts.SettingEntry
	reply := h.Serve(context.Background(), cliCaller, req(t, "settings.schema", `{"owner":"husk"}`), nil)
	if reply.Error != nil || json.Unmarshal(reply.Result, &entries) != nil || len(entries) != 1 || entries[0].Key != "theme.name" {
		t.Fatalf("schema = %s / %+v", reply.Result, reply.Error)
	}
	if calls := rec.Methods(); calls[len(calls)-1] != "Settings.Schema" || rec.Calls()[len(rec.Calls())-1].Args[0] != "husk" {
		t.Fatalf("schema owner not passed: %v", rec.Calls())
	}
	validate := decode(t, h.Serve(context.Background(), cliCaller, req(t, "settings.validate", `{}`), nil))
	if validate["applied"] != true {
		t.Fatalf("validate = %v", validate)
	}
}

func TestSettingsNilListEncodesAsEmptyObject(t *testing.T) {
	port := fakes.NewSettings(nil)
	port.Values = nil
	h := Settings{Port: port}
	reply := h.Serve(context.Background(), cliCaller, req(t, "settings.list", `{}`), nil)
	if string(reply.Result) != "{}" {
		t.Fatalf("nil list = %s, want {}", reply.Result)
	}
}

func TestSettingsGetRateLimitedPerPlugin(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSettings(rec)
	clock := fakes.NewClock(time.Unix(0, 0))
	h := Settings{Port: port, Limiter: NewWindowLimiter(clock, contracts.RateLimitPerSecond)}
	r := req(t, "settings.get", `{"key":"theme.name"}`)

	for i := 0; i < contracts.RateLimitPerSecond; i++ {
		if reply := h.Serve(context.Background(), pluginCaller("files"), r, nil); reply.Error != nil {
			t.Fatalf("call %d denied: %+v", i+1, reply.Error)
		}
	}
	refused := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), r, nil))
	if refused.Code != protocol.CodeBusy {
		t.Fatalf("code = %s, want busy", refused.Code)
	}
	var data map[string]string
	if err := json.Unmarshal(refused.Data, &data); err != nil || data["reason"] != "rate_limited" {
		t.Fatalf("data = %s", refused.Data)
	}
	if calls := rec.Methods(); len(calls) != contracts.RateLimitPerSecond {
		t.Fatalf("port calls = %d, want %d (refusal must not reach the port)", len(calls), contracts.RateLimitPerSecond)
	}

	// The window is per plugin id: another plugin has its own budget.
	if reply := h.Serve(context.Background(), pluginCaller("other"), r, nil); reply.Error != nil {
		t.Fatalf("other plugin denied: %+v", reply.Error)
	}

	// CLI callers are never limited.
	rec.Reset()
	for i := 0; i < contracts.RateLimitPerSecond+5; i++ {
		if reply := h.Serve(context.Background(), cliCaller, r, nil); reply.Error != nil {
			t.Fatalf("CLI call %d limited: %+v", i+1, reply.Error)
		}
	}

	// One second after the window opened, the budget resets whole.
	clock.Advance(time.Second)
	if reply := h.Serve(context.Background(), pluginCaller("files"), r, nil); reply.Error != nil {
		t.Fatalf("after window reset: %+v", reply.Error)
	}
}

func TestRateLimitCountsMalformedRequests(t *testing.T) {
	// services.md Limits: the cap counts requests, not valid requests. The
	// check runs before decode, so malformed params consume budget and a
	// later well-formed request is refused by the exhausted window.
	rec := fakes.NewRecorder()
	port := fakes.NewSettings(rec)
	clock := fakes.NewClock(time.Unix(0, 0))
	h := Settings{Port: port, Limiter: NewWindowLimiter(clock, contracts.RateLimitPerSecond)}
	bad := req(t, "settings.get", `{"key":123}`)
	for i := 0; i < contracts.RateLimitPerSecond; i++ {
		err := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), bad, nil))
		if err.Code != protocol.CodeInvalidParams {
			t.Fatalf("malformed call %d: code = %s, want invalid_params", i+1, err.Code)
		}
	}
	good := req(t, "settings.get", `{"key":"theme.name"}`)
	refused := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), good, nil))
	if refused.Code != protocol.CodeBusy {
		t.Fatalf("well-formed call after budget spent: code = %s, want busy", refused.Code)
	}
	if calls := rec.Methods(); len(calls) != 0 {
		t.Fatalf("port calls = %d, want 0", len(calls))
	}
	// Same ruling for secrets.get.
	secretsRec := fakes.NewRecorder()
	sh := Secrets{Port: fakes.NewSecrets(secretsRec), Limiter: NewWindowLimiter(clock, contracts.RateLimitPerSecond)}
	badSecret := req(t, "secrets.get", `{"name":123}`)
	for i := 0; i < contracts.RateLimitPerSecond; i++ {
		err := replyErr(t, sh.Serve(context.Background(), pluginCaller("files"), badSecret, nil))
		if err.Code != protocol.CodeInvalidParams {
			t.Fatalf("malformed secrets call %d: code = %s, want invalid_params", i+1, err.Code)
		}
	}
	goodSecret := req(t, "secrets.get", `{"name":"api_key"}`)
	refused = replyErr(t, sh.Serve(context.Background(), pluginCaller("files"), goodSecret, nil))
	if refused.Code != protocol.CodeBusy {
		t.Fatalf("well-formed secrets call after budget spent: code = %s, want busy", refused.Code)
	}
	if calls := secretsRec.Methods(); len(calls) != 0 {
		t.Fatalf("secrets port calls = %d, want 0", len(calls))
	}
}

func TestSharedBudgetCoversSettingsAndSecretsGet(t *testing.T) {
	clock := fakes.NewClock(time.Unix(0, 0))
	limiter := NewWindowLimiter(clock, contracts.RateLimitPerSecond)
	set := Settings{Port: fakes.NewSettings(nil), Limiter: limiter}
	sec := Secrets{Port: fakes.NewSecrets(nil), Limiter: limiter}
	files := pluginCaller("files")

	for i := 0; i < contracts.RateLimitPerSecond; i++ {
		if reply := set.Serve(context.Background(), files, req(t, "settings.get", `{"key":"husk.layout.rail"}`), nil); reply.Error != nil {
			t.Fatalf("settings.get %d: %+v", i+1, reply.Error)
		}
	}
	refused := replyErr(t, sec.Serve(context.Background(), files, req(t, "secrets.get", `{"name":"token"}`), nil))
	if refused.Code != protocol.CodeBusy {
		t.Fatalf("secrets.get code = %s, want busy: one shared budget per plugin", refused.Code)
	}
}

func TestSecretsServe(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSecrets(rec)
	port.Value = []byte("hunter2")
	port.Infos = []contracts.SecretInfo{{Name: "token", Set: true}}
	h := Secrets{Port: port}

	got := decode(t, h.Serve(context.Background(), pluginCaller("files"), req(t, "secrets.get", `{"name":"token"}`), nil))
	if got["name"] != "token" || got["value"] != "hunter2" {
		t.Fatalf("get = %v", got)
	}
	var infos []contracts.SecretInfo
	reply := h.Serve(context.Background(), pluginCaller("files"), req(t, "secrets.names", `{}`), nil)
	if reply.Error != nil || json.Unmarshal(reply.Result, &infos) != nil || len(infos) != 1 {
		t.Fatalf("names = %s / %+v", reply.Result, reply.Error)
	}
	reveal := decode(t, h.Serve(context.Background(), cliCaller, req(t, "secrets.reveal", `{"name":"token"}`), nil))
	if reveal["value"] != "hunter2" {
		t.Fatalf("reveal = %v", reveal)
	}
	if reply := h.Serve(context.Background(), cliCaller, req(t, "secrets.set", `{"name":"token","value":"v"}`), nil); reply.Error != nil || string(reply.Result) != "{}" {
		t.Fatalf("set = %+v %s", reply.Error, reply.Result)
	}
	if reply := h.Serve(context.Background(), cliCaller, req(t, "secrets.grant", `{"plugin":"files","name":"token"}`), nil); reply.Error != nil {
		t.Fatalf("grant: %+v", reply.Error)
	}
	if reply := h.Serve(context.Background(), cliCaller, req(t, "secrets.revoke", `{"plugin":"files","name":"token"}`), nil); reply.Error != nil {
		t.Fatalf("revoke: %+v", reply.Error)
	}
	want := []string{"Secrets.Get", "Secrets.Names", "Secrets.Reveal", "Secrets.Set", "Secrets.Grant", "Secrets.Revoke"}
	if got := rec.Methods(); !equalStrings(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestSecretsUserOnlyMethodsDeniedToPluginBeforePort(t *testing.T) {
	rec := fakes.NewRecorder()
	port := fakes.NewSecrets(rec)
	h := Secrets{Port: port}
	for _, method := range []string{"secrets.set", "secrets.grant", "secrets.revoke", "secrets.reveal"} {
		errp := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), protocol.Message{Method: method}, nil))
		if errp.Code != protocol.CodeDenied {
			t.Fatalf("%s: code = %s, want denied", method, errp.Code)
		}
	}
	if calls := rec.Methods(); len(calls) != 0 {
		t.Fatalf("port touched: %v", calls)
	}
}

func TestSecretsRequireNameAndPlugin(t *testing.T) {
	h := Secrets{Port: fakes.NewSecrets(nil)}
	for _, tc := range []struct {
		method string
		params string
		field  string
	}{
		{"secrets.get", `{"name":""}`, "params.name"},
		{"secrets.get", `["token"]`, "params"},
		{"secrets.grant", `{"name":"n"}`, "params.plugin"},
		{"secrets.revoke", `{"plugin":"p"}`, "params.name"},
	} {
		errp := replyErr(t, h.Serve(context.Background(), cliCaller, req(t, tc.method, tc.params), nil))
		if errp.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s %s: code = %s, want invalid_params", tc.method, tc.params, errp.Code)
		}
		var data map[string]string
		if err := json.Unmarshal(errp.Data, &data); err != nil || data["field"] != tc.field {
			t.Fatalf("%s: data = %s", tc.method, errp.Data)
		}
	}
}

func TestSecretsNilNamesEncodeAsEmptyArray(t *testing.T) {
	port := fakes.NewSecrets(nil)
	port.Infos = nil
	h := Secrets{Port: port}
	reply := h.Serve(context.Background(), cliCaller, req(t, "secrets.names", `{}`), nil)
	if string(reply.Result) != "[]" {
		t.Fatalf("nil names = %s, want []", reply.Result)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pluginCaller(id string) contracts.Caller {
	return contracts.Caller{Kind: contracts.CallerPlugin, PluginID: id}
}

var cliCaller = contracts.Caller{Kind: contracts.CallerCLI}

// codedErrorStub carries code+data through ErrorReply like the port wrappers.
type codedErrorStub struct {
	code protocol.Code
	data map[string]any
}

func (e codedErrorStub) Error() string        { return string(e.code) }
func (e codedErrorStub) Code() string         { return string(e.code) }
func (e codedErrorStub) Data() map[string]any { return e.data }

func TestSecretsSetMalformedParamsNeverQuoteValue(t *testing.T) {
	// A malformed secrets.set is decoded by handlers.DecodeParams, whose
	// invalid_params message carries the json error text. The json errors
	// never quote the offending value, and this test is the net that keeps
	// it that way: the secret rides in the raw params, a syntax error lands
	// mid-value, and neither the message nor the data may quote it.
	const value = "hunter2-super-secret"
	h := Secrets{Port: fakes.NewSecrets(nil)}
	for _, params := range []string{
		`{"name":"git.token","value":"` + value + `"@}`, // syntax error after the value
		`{"name":"git.token","value":` + value + `}`,    // syntax error mid-value
		`{"name":"git.token","value":123}`,              // type error where the value would ride
	} {
		errp := replyErr(t, h.Serve(context.Background(), cliCaller, req(t, "secrets.set", params), nil))
		if errp.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s: code = %s, want invalid_params", params, errp.Code)
		}
		if strings.Contains(errp.Message, value) || strings.Contains(string(errp.Data), value) {
			t.Fatalf("invalid_params quotes the value: %s %s", errp.Message, errp.Data)
		}
	}
}

func TestPortErrorsKeepCodeAndReason(t *testing.T) {
	port := fakes.NewSettings(nil)
	port.Err = codedErrorStub{code: protocol.CodeDenied, data: map[string]any{"reason": "needs_grant"}}
	h := Settings{Port: port}
	errp := replyErr(t, h.Serve(context.Background(), pluginCaller("files"), req(t, "settings.get", `{"key":"k"}`), nil))
	if errp.Code != protocol.CodeDenied {
		t.Fatalf("code = %s", errp.Code)
	}
	var data map[string]string
	if err := json.Unmarshal(errp.Data, &data); err != nil || data["reason"] != "needs_grant" {
		t.Fatalf("data = %s", errp.Data)
	}

	port.Err = errors.New("plain failure")
	errp = replyErr(t, h.Serve(context.Background(), cliCaller, req(t, "settings.get", `{"key":"k"}`), nil))
	if errp.Code != protocol.CodeInternal || errp.Message != "plain failure" {
		t.Fatalf("plain = %+v", errp)
	}
}
