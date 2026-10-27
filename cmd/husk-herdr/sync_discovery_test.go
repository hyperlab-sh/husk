package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
)

func TestSyncDiscoveryWarningIsSkippedNotRefused(t *testing.T) {
	sockets, err := os.MkdirTemp("/tmp", "hw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
	root := t.TempDir()
	dir := syncFixture(t, root, "unparseable", true)
	if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte("id='unparseable'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]any{{"plugin_id": "unparseable", "plugin_root": dir, "enabled": true}})
	registry := filepath.Join(root, "plugins.json")
	if err := os.WriteFile(registry, raw, 0600); err != nil {
		t.Fatal(err)
	}
	env := guardUpstream(t, environment{executable: "/adapter", platform: runtime.GOOS, registryPath: registry})
	calls := 0
	env.runCLI = func(args []string) ([]byte, []byte, int) {
		calls++
		if args[0] != "plugin" || args[1] != "list" {
			t.Fatalf("discovery warning was added: %v", args)
		}
		return []byte("[]"), nil, 0
	}
	var out, diagnostic bytes.Buffer
	code := execute([]string{"sync"}, &out, &diagnostic, env)
	if code != 0 || out.Len() != 0 || calls != 1 || !strings.Contains(diagnostic.String(), `"code":"registry_warning"`) || !strings.Contains(diagnostic.String(), "min_herdr_version") {
		t.Fatalf("discovery skip: code=%d calls=%d stdout=%q stderr=%q", code, calls, &out, &diagnostic)
	}
}
