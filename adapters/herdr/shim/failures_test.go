package shim

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalVersionFailurePublishesTooOld(t *testing.T) {
	r := newRig(t, false, "")
	path := filepath.Join(r.o.Dir, "herdr-plugin.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "min_herdr_version='0.9.0'", "min_herdr_version='9.0.0'", 1))
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	reply := r.invoke(t, "demo.go")
	if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || !strings.Contains(reply.Error.Message, "herdr_too_old") {
		t.Fatalf("version failure=%+v", reply)
	}
}

func TestMissingBinaryFailsBeforeCommands(t *testing.T) {
	r := newRig(t, false, "")
	r.o.Binary = ""
	if err := Run(context.Background(), r.o, strings.NewReader(""), io.Discard, io.Discard); err == nil || err.Error() != "herdr binary not found" {
		t.Fatalf("binary failure=%v", err)
	}
}

func TestCommandCapReturnsBusy(t *testing.T) {
	r, terminals := paneRig(t)
	// Route the proxy's host invocation back through the real stdio request path.
	r.host.invoke = func(context.Context, any) (json.RawMessage, error) {
		reply := r.invoke(t, "demo.go")
		if reply.Error != nil {
			return nil, reply.Error
		}
		return reply.Result, nil
	}
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	var path string
	for range 32 {
		if reply := r.invoke(t, "demo.go"); reply.Error != nil {
			t.Fatal(reply.Error)
		}
		path = envMap(take(t, r.spawns).command.Env)["HERDR_SOCKET_PATH"]
	}
	if reply := r.invoke(t, "demo.go"); reply.Error == nil || reply.Error.Code != protocol.CodeBusy || string(reply.Error.Data) != `{"reason":"too_many_commands"}` {
		t.Fatalf("cap=%+v", reply)
	}
	for _, tc := range []struct{ method, params string }{
		{"plugin.action.invoke", `{"plugin_id":"demo","action_id":"go"}`},
		{"plugin.pane.open", `{"plugin_id":"demo","entrypoint":"panel","placement":"overlay"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			result := proxyCall(t, path, `{"id":"cap","method":"`+tc.method+`","params":`+tc.params+"}\n")
			failure, ok := result["error"].(map[string]any)
			if !ok || failure["code"] != "plugin_command_limit_reached" {
				t.Fatalf("cap reply=%v", result)
			}
		})
	}
	select {
	case <-terminals:
		t.Fatal("cap refusal spawned terminal")
	default:
	}
}
