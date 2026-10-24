package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func execConverterTestRole(args []string) int {
	switch args[0] {
	case "discover":
		return 0
	case "detect":
		if _, err := os.Stat(filepath.Join(args[1], "external.json")); err != nil {
			return 1
		}
		return 0
	case "convert":
		raw, err := os.ReadFile(filepath.Join(args[1], "external.json"))
		if err != nil {
			return 2
		}
		_, err = os.Stdout.Write(raw)
		if err != nil {
			return 2
		}
		return 0
	case "converter-action-list":
		var out, stderr bytes.Buffer
		env := cli.OSEnv()
		env.Stdout = &out
		env.Stderr = &stderr
		code := cli.Main(context.Background(), []string{"action", "list", "--format", "json"}, env)
		if code != 0 {
			out.Write(stderr.Bytes())
		}
		if err := os.WriteFile(args[1], out.Bytes(), 0600); err != nil {
			return 3
		}
		conn, err := net.Dial("unix", os.Getenv("HUSK_SOCKET_PATH"))
		if err != nil {
			return 4
		}
		params, _ := json.Marshal(protocol.SurfaceUpdateParams{Surface: protocol.SurfaceWorkspaceStage, Lines: []string{"Converted action catalog ready"}})
		message := protocol.Message{Method: protocol.MethodSurfaceUpdate, Token: os.Getenv("HUSK_PLUGIN_TOKEN"), Params: params}
		if err := protocol.NewWriter(conn).Send(message); err != nil {
			_ = conn.Close()
			return 4
		}
		defer conn.Close()
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
		}
		return code
	}
	return 2
}

func TestConverterFlagListsConvertedActions(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	if err := os.Mkdir(plugin, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(root, "actions.json")
	raw, _ := json.Marshal(map[string]any{"manifest_version": 2, "protocol": 2, "id": "converted", "name": "Converted", "version": "1", "command": []string{exe, "converter-action-list", result}, "actions": []any{map[string]any{"id": "converted.open", "title": "Converted action"}}, "surfaces": []any{map[string]any{"id": protocol.SurfaceWorkspaceStage}}})
	if err := os.WriteFile(filepath.Join(plugin, "external.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "--dump-frame", "--converter", exe, "--plugin", plugin)
	runtime := shortRuntimeDir(t, "converter")
	// The host strips this test-main switch before invoking the converter, whose
	// role is selected solely by argv. No fixture relies on inherited HUSK data.
	cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "GORACE=atexit_sleep_ms=0", "HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_DATA_HOME="+filepath.Join(root, "appdata"), "XDG_RUNTIME_DIR="+runtime)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launch: %v: %s", err, output)
	}
	actions, err := os.ReadFile(result)
	if err != nil || !strings.Contains(string(actions), "converted.open") {
		t.Fatalf("actions=%s err=%v launch=%s", actions, err, output)
	}
}
