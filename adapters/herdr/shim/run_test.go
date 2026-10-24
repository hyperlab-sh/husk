package shim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/adapters/herdr/settings"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestActionStartsImmediatelyWithExactEnvironment(t *testing.T) {
	r := newRig(t, "never", true, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	reply := r.invoke(t, "demo.go")
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var result struct {
		Started bool
		PID     int
		Log     string
	}
	_ = json.Unmarshal(reply.Result, &result)
	spawn := take(t, r.spawns)
	if !result.Started || result.PID != spawn.process.pid || result.Log != "/logs/demo.log" {
		t.Fatalf("not an immediate started reply: %s", reply.Result)
	}
	env := envMap(spawn.command.Env)
	var c InvocationContext
	if err := json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &c); err != nil {
		t.Fatal(err)
	}
	if c.CorrelationID != "call" || c.InvocationSource != "api" || c.FocusedPaneID != "p" || c.WorkspaceID != "w" || c.TabID != "t" {
		t.Fatalf("context=%+v", c)
	}
	path := env["HERDR_SOCKET_PATH"]
	expected := map[string]string{"KEEP": "value", "HUSK_SOCKET_PATH": "/host.sock", "HUSK_INSTANCE": "unit", "HUSK_BIN_PATH": "/usr/local/husk", "HERDR_SOCKET_PATH": path, "HERDR_BIN_PATH": r.o.Binary, "HERDR_ENV": "1", "HERDR_PLUGIN_ID": "demo", "HERDR_PLUGIN_ROOT": r.o.Dir, "HERDR_PLUGIN_CONFIG_DIR": r.o.ConfigDir, "HERDR_PLUGIN_STATE_DIR": r.o.StateDir, "HERDR_PLUGIN_CONTEXT_JSON": env["HERDR_PLUGIN_CONTEXT_JSON"], "HERDR_PLUGIN_ACTION_ID": "go", "HERDR_WORKSPACE_ID": "w", "HERDR_TAB_ID": "t", "HERDR_PANE_ID": "p"}
	if !reflect.DeepEqual(env, expected) {
		keys := []string{}
		for k, v := range env {
			if want, ok := expected[k]; !ok || v != want {
				keys = append(keys, k)
			}
		}
		for k := range expected {
			if _, ok := env[k]; !ok {
				keys = append(keys, k)
			}
		}
		t.Fatalf("environment table differs at keys %v", keys)
	}
	if spawn.command.Dir != r.o.Dir || !reflect.DeepEqual(spawn.command.Argv, []string{"./command", "arg"}) {
		t.Fatalf("command=%+v", spawn.command)
	}
	socket, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if socket.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("proxy is not private")
	}
	spawn.process.done <- 2
	toast := r.host.until(t, protocol.MethodToastShow, "")
	if !strings.Contains(string(toast), "Demo: Go exited 2") {
		t.Fatalf("toast=%s", toast)
	}
	if !strings.Contains(r.logs.String(), "last stderr") {
		t.Fatal("missing bounded command stderr log")
	}
}

func TestHookPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  string
		enabled bool
		owner   string
		run     bool
	}{{"auto", true, "server", false}, {"auto", false, "shim", true}, {"always", true, "shim", true}, {"never", false, "never", false}} {
		t.Run(tc.policy+"_"+tc.owner, func(t *testing.T) {
			r := newRig(t, tc.policy, tc.enabled, "[[startup]]\ncommand=['./startup']\n")
			r.start(t)
			raw := r.host.until(t, protocol.MethodStateSet, "attached")
			var published struct{ Value state }
			_ = json.Unmarshal(raw, &published)
			if published.Value.Hooks != tc.owner {
				t.Fatalf("owner=%s", published.Value.Hooks)
			}
			if tc.run {
				p := take(t, r.spawns)
				env := envMap(p.command.Env)
				if env["HERDR_PLUGIN_EVENT"] != "startup" {
					t.Fatal(env)
				}
				p.process.done <- 0
				r.host.until(t, protocol.MethodStateSet, "attached")
			} else {
				reply := r.invoke(t, "demo.go")
				if reply.Error != nil {
					t.Fatal(reply.Error)
				}
				p := take(t, r.spawns)
				if p.command.Argv[0] != "./command" {
					t.Fatal("hook unexpectedly ran")
				}
				p.process.done <- 0
			}
		})
	}
}

func TestHooksReconnectAndEvents(t *testing.T) {
	r := newRig(t, "auto", false, "[[startup]]\ncommand=['./startup']\n[[events]]\non='pane.focused'\ncommand=['./event']\n[[events]]\non='pane.output_matched'\ncommand=['./invalid']\n")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	startup := take(t, r.spawns)
	startup.process.done <- 0
	if !strings.Contains(r.logs.String(), "invalid_subscription") {
		t.Fatal("invalid subscription not logged")
	}
	if err := r.server.Emit("pane_focused", map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"}); err != nil {
		t.Fatal(err)
	}
	event := take(t, r.spawns)
	env := envMap(event.command.Env)
	if env["HERDR_PLUGIN_EVENT"] != "pane.focused" || !strings.Contains(env["HERDR_PLUGIN_EVENT_JSON"], `"event":"pane_focused"`) {
		t.Fatal("event name or event envelope is missing from command environment")
	}
	event.process.done <- 0
	r.server.DropSubscriptions()
	r.host.until(t, protocol.MethodStateSet, "lost")
	r.clock.fire(t, 5*time.Second)
	r.host.until(t, protocol.MethodStateSet, "attached")
	again := take(t, r.spawns)
	if again.command.Argv[0] != "./startup" {
		t.Fatal("startup not rerun on attach")
	}
	again.process.done <- 0
}

func TestEOFStopsProcessGroupOnInjectedGrace(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	p := take(t, r.spawns).process
	p.termStops = false
	_ = r.in.Close()
	if s := take(t, p.signals); s != syscall.SIGTERM {
		t.Fatalf("first signal=%v", s)
	}
	r.clock.fire(t, 2*time.Second)
	if s := take(t, p.signals); s != syscall.SIGKILL {
		t.Fatalf("second signal=%v", s)
	}
	// Cleanup consumes Run's result and checks that all workers join.
}

func TestPaneStub(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	reply := r.invoke(t, "demo.pane-panel")
	if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || reply.Error.Message != "pane entrypoints are not supported yet" {
		t.Fatalf("pane=%+v", reply)
	}
	rr := &runner{m: manifest.Manifest{ID: "demo"}}
	pp, err := proxy.Start(context.Background(), proxy.Options{Dial: upstream.New(upstream.Options{Socket: r.o.Socket}).Dial, Intercept: rr.intercept})
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	errorShape := shape(t, "error_response")
	for _, req := range []proxy.Request{{ID: "a", Method: "plugin.pane.open", Params: json.RawMessage(`{"plugin_id":"demo","entrypoint":"panel"}`)}, {ID: "b", Method: "plugin.pane.focus", Params: json.RawMessage(`{"pane_id":"husk:demo.panel"}`)}, {ID: "c", Method: "plugin.pane.close", Params: json.RawMessage(`{"pane_id":"husk:demo.panel"}`)}, {ID: "d", Method: "popup.close", Params: json.RawMessage(`{}`)}} {
		_, failure, handled := rr.intercept(context.Background(), req)
		if !handled || failure == nil || failure.Message != "pane entrypoints are not supported yet" {
			t.Fatalf("%s: %v %v", req.Method, handled, failure)
		}
		raw, _ := json.Marshal(req)
		envelope := proxyCall(t, pp.Path(), string(raw)+"\n")
		if err := errorShape.Validate(envelope); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProxyNotificationShapeAndTrims(t *testing.T) {
	r := newRig(t, "never", true, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	p := take(t, r.spawns)
	path := envMap(p.command.Env)["HERDR_SOCKET_PATH"]
	result := proxyCall(t, path, "{\"id\":\"n\",\"method\":\"notification.show\",\"params\":{\"title\":\"Title\",\"body\":\"Body\"}}\n")
	if err := shape(t, "success_response").Validate(result); err != nil {
		t.Fatal(err)
	}
	answer := result["result"].(map[string]any)
	if answer["type"] != "notification_show" || answer["shown"] != true || answer["reason"] != "shown" {
		t.Fatal(result)
	}
	if raw := r.host.until(t, protocol.MethodToastShow, ""); !strings.Contains(string(raw), `Title\nBody`) {
		t.Fatalf("notification=%s", raw)
	}
	request, _ := json.Marshal(map[string]any{"id": "trim", "method": "notification.show", "params": map[string]any{"title": strings.Repeat("界", 81), "body": strings.Repeat("é", 241)}})
	_ = proxyCall(t, path, string(request)+"\n")
	raw := r.host.until(t, protocol.MethodToastShow, "")
	var trimmed struct{ Message string }
	_ = json.Unmarshal(raw, &trimmed)
	if trimmed.Message != strings.Repeat("界", 80)+"\n"+strings.Repeat("é", 240) {
		t.Fatal("notification rune trims drifted")
	}
	for _, req := range r.server.Requests() {
		if req.Method == "notification.show" {
			t.Fatal("notification was forwarded upstream")
		}
	}
	p.process.done <- 0
}

func TestRegistryRoutingAndNotFoundFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(path, []byte(`[{"plugin_id":"other","enabled":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	host := &testHost{events: make(chan hostRecord, 10)}
	r := &runner{m: manifest.Manifest{ID: "demo"}, o: Options{RegistryPath: path, Platform: "linux", Host: host}}
	host.invoke = func(_ context.Context, p any) (json.RawMessage, error) {
		raw, _ := json.Marshal(p)
		if !strings.Contains(string(raw), `"action":"other.go"`) || !strings.Contains(string(raw), `"invocation_source":"command_palette"`) {
			t.Fatalf("relay=%s", raw)
		}
		return json.RawMessage(`{"context":{"invocation_source":"command_palette"},"action_info":{"plugin_id":"other","action_id":"go","title":"Go","command":["./go"]},"command_log":{"log_id":"one","plugin_id":"other","command":["./go"],"status":"running","started_unix_ms":100}}`), nil
	}
	req := proxy.Request{Method: "plugin.action.invoke", Params: json.RawMessage(`{"plugin_id":"other","action_id":"go","context":{"invocation_source":"command_palette"}}`)}
	result, err, handled := r.intercept(context.Background(), req)
	if !handled || err != nil || result.(map[string]any)["type"] != "plugin_action_invoked" {
		t.Fatal(result, err, handled)
	}
	raw, _ := json.Marshal(map[string]any{"id": "relay", "result": result})
	var value any
	_ = json.Unmarshal(raw, &value)
	if err := shape(t, "success_response").Validate(value); err != nil {
		t.Fatal(err)
	}
	host.invoke = func(context.Context, any) (json.RawMessage, error) {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "missing"}
	}
	if _, _, handled = r.intercept(context.Background(), req); handled {
		t.Fatal("not_found did not fall back")
	}
	if err := os.WriteFile(path, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	host.invoke = func(context.Context, any) (json.RawMessage, error) {
		t.Fatal("native collision invoked")
		return nil, nil
	}
	if _, _, handled = r.intercept(context.Background(), req); handled {
		t.Fatal("registry removal was not observed")
	}
}

func TestUnavailableAndTerminalAttachFailures(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.server.Close()
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "absent")
	reply := r.invoke(t, "demo.go")
	if reply.Error == nil || reply.Error.Code != protocol.CodeUnavailable || !strings.Contains(string(reply.Error.Data), "herdr_absent") {
		t.Fatalf("absent=%+v", reply)
	}
}

func TestSpawnFailureIsFailed(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.o.Spawn = func(Command) (Process, error) { return nil, errors.New("spawn refused") }
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	reply := r.invoke(t, "demo.go")
	if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || reply.Error.Message != "spawn refused" {
		t.Fatalf("spawn=%+v", reply)
	}
}

func TestPerPluginPolicyOverrides(t *testing.T) {
	c := settings.Config{Hooks: "never", Plugins: map[string]settings.Plugin{"demo": {Hooks: "always"}}}
	if c.HookOwner("demo", true) != "shim" || c.HookOwner("other", true) != "never" {
		t.Fatal("per-plugin policy lost")
	}
}
