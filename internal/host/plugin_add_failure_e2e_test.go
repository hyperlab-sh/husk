package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
)

func TestPluginAddMissingCommandExitsOneAndKeepsOutcomes(t *testing.T) {
	path := writeIdlePlugin(t, "alpha")
	file := filepath.Join(path, manifest.FileName)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["command"] = []string{filepath.Join(path, "no-such-command")}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	code, stdout, stderr := cliRun(h, "plugin", "add", path)
	var result contracts.PluginAddition
	if code != 1 || json.Unmarshal([]byte(stdout), &result) != nil || len(result.Added) != 1 || result.Added[0].Plugin != "alpha" || result.Added[0].Status != "failed" || result.Added[0].Log == "" || len(result.Reports) != 5 {
		t.Fatalf("add: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var diagnostic struct {
		Error struct{ Code, Message string }
	}
	if json.Unmarshal([]byte(stderr), &diagnostic) != nil || diagnostic.Error.Code != "failed" || !strings.Contains(diagnostic.Error.Message, "alpha") {
		t.Fatalf("diagnostic=%s", stderr)
	}
	code, stdout, stderr = cliRun(h, "plugin", "list")
	if code != 0 || !strings.Contains(stdout, `"status":"failed"`) || !strings.Contains(stdout, "no-such-command") {
		t.Fatalf("list: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}
