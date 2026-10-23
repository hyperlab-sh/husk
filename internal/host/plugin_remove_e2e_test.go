package host

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// idleArg selects the idle plugin mode of the test binary.
const idleArg = "idle-plugin"

// TestIdlePluginProcess is the plugin process of the remove e2e test: the test
// binary re-executed with idleArg, which reads stdin until EOF and exits (the
// stop policy's first step). Run normally it does nothing.
func TestIdlePluginProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != idleArg {
		t.Skip("plugin process mode only")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// writeIdlePlugin writes a plugin whose process is a real OS process that
// idles on stdin, declaring one action and one state slice. It is launched by
// the real wrapper (no factory), so the stop policy runs against a real
// process.
func writeIdlePlugin(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"manifest_version": 2, "protocol": 2, "id": id, "name": id, "version": "1",
		"command":  []string{os.Args[0], "-test.run=^TestIdlePluginProcess$", "--", idleArg},
		"surfaces": []any{map[string]any{"id": "sidebar-list"}},
		"actions":  []any{map[string]any{"id": id + ".new", "title": "New"}},
		"slice":    map[string]any{"scope": "instance", "persistent": false},
	})
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPluginRemoveAgainstRealHostAndProcess removes a plugin running as a real
// OS process and checks the reply, the readers derived from the declaration
// and the order of the published facts as one watcher sees them.
func TestPluginRemoveAgainstRealHostAndProcess(t *testing.T) {
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{writeIdlePlugin(t, "alpha")} }, withHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	cancel, _ := runHost(t, ready, h)
	defer cancel()

	// Wait until the plugin is live and its action is declared.
	for i := 0; ; i++ {
		if h.podStatus("alpha") == "live" {
			break
		}
		if i > 2000 {
			t.Fatal("plugin never went live")
		}
		h.loop(func() {})
		runtime.Gosched()
	}
	if code, out, stderr := cliRun(h, "action", "list", "--format", "json"); code != 0 || !strings.Contains(out, "alpha.new") {
		t.Fatalf("action list before: exit %d out %q err %q", code, out, stderr)
	}

	// One raw connection watches the three families (state.changed travels on
	// the bus like the pods.* and services.* facts).
	w := dial(t, instance.SocketPath(h.dir, h.id))
	for i, pattern := range []string{"pods.*", "state.*", "services.*"} {
		if rep := w.callParams(t, string(rune('a'+i)), protocol.MethodEventWatch, map[string]any{"pattern": pattern}); rep.Error != nil {
			t.Fatalf("event.watch %s: %+v", pattern, rep.Error)
		}
	}

	code, out, stderr := cliRun(h, "rpc", "plugin.remove", `{"plugin":"alpha"}`, "--format", "json")
	if code != 0 {
		t.Fatalf("plugin.remove: exit %d: %s", code, stderr)
	}
	var rep contracts.PluginRemoval
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("reply %q: %v", out, err)
	}
	if rep.Plugin != "alpha" || rep.Status != contracts.PodStatus("exited") || rep.Log == "" || len(rep.Reports) == 0 {
		t.Fatalf("reply = %+v", rep)
	}

	// No sleep between the reply and the readers: the action is gone already.
	if code, out, _ := cliRun(h, "action", "list", "--format", "json"); code != 0 || strings.Contains(out, "alpha.new") {
		t.Fatalf("action list after: exit %d out %q", code, out)
	}
	if code, out, _ := cliRun(h, "rpc", "plugin.list", "--format", "json"); code != 0 || strings.Contains(out, `"alpha"`) {
		t.Fatalf("plugin.list after: exit %d out %q", code, out)
	}
	if code, _, stderr := cliRun(h, "rpc", "plugin.remove", `{"plugin":"alpha"}`, "--format", "json"); code == 0 || !strings.Contains(stderr, `"code":"not_found"`) {
		t.Fatalf("second remove: exit %d err %q", code, stderr)
	}

	// Invariant 2: exited, the plugin's slice changing, removed, reloaded.
	var order []string
	_ = w.c.SetReadDeadline(time.Now().Add(failBound))
	for len(order) == 0 || order[len(order)-1] != "services.reloaded" {
		line, err := w.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("after %v: %v", order, err)
		}
		var m protocol.Message
		if err := json.Unmarshal(line, &m); err != nil || m.Method != protocol.MethodEventDeliver {
			continue
		}
		var p struct {
			Name string          `json:"name"`
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch {
		case p.Name == "pods.plugin-exited" || p.Name == "pods.plugin-removed" || p.Name == "services.reloaded":
			order = append(order, p.Name)
		case p.Name == "state.changed" && strings.Contains(string(p.Data), `"alpha.state"`):
			order = append(order, p.Name)
		}
	}
	want := []string{"pods.plugin-exited", "state.changed", "pods.plugin-removed", "services.reloaded"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}
