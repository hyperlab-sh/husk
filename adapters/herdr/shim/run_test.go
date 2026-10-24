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
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestActionStartsImmediatelyWithExactEnvironment(t *testing.T) {
	r := newRig(t, "never", true, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
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

func TestEOFStopsProcessGroupOnInjectedGrace(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
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
	r.host.until(t, protocol.MethodStateSet, "")
	reply := r.invoke(t, "demo.go")
	if reply.Error == nil || reply.Error.Code != protocol.CodeUnavailable || !strings.Contains(string(reply.Error.Data), "herdr_absent") {
		t.Fatalf("absent=%+v", reply)
	}
}

func TestSpawnFailureIsFailed(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.o.Spawn = func(Command) (Process, error) { return nil, errors.New("spawn refused") }
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	reply := r.invoke(t, "demo.go")
	if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || reply.Error.Message != "spawn refused" {
		t.Fatalf("spawn=%+v", reply)
	}
}
