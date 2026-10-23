package host

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
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

	// One raw connection watches the facts: the events by name pattern, and
	// the plugin's slice through state.watch (state.changed is a watcher
	// notification from the State port, not a bus event).
	w := dial(t, instance.SocketPath(h.dir, h.id))
	for i, req := range []struct {
		method string
		params map[string]any
	}{
		{protocol.MethodEventWatch, map[string]any{"pattern": "pods.*"}},
		{protocol.MethodEventWatch, map[string]any{"pattern": "services.*"}},
		{protocol.MethodStateWatch, map[string]any{"slices": []string{"alpha.state", "alpha.occupants", "alpha.contributions", "husk.pods"}}},
	} {
		if rep := w.callParams(t, string(rune('a'+i)), req.method, req.params); rep.Error != nil {
			t.Fatalf("%s: %+v", req.method, rep.Error)
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

	// Invariants 2 and 3: pods.plugin-exited, then the three plugin slices
	// removed and husk.pods without the plugin (one flush, so contiguous and
	// before anything else), then pods.plugin-removed, then
	// services.reloaded. A husk.pods change that still lists the plugin (the
	// exit's own write) is not part of the removal.
	var order []string
	_ = w.c.SetReadDeadline(time.Now().Add(failBound))
	for len(order) == 0 || order[len(order)-1] != "services.reloaded" {
		line, err := w.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("after %v: %v", order, err)
		}
		var m protocol.Message
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if m.Method == protocol.MethodStateChanged {
			var c struct {
				Slice   string          `json:"slice"`
				Removed bool            `json:"removed"`
				Value   json.RawMessage `json:"value"`
			}
			_ = json.Unmarshal(m.Params, &c)
			switch {
			case c.Slice == "husk.pods":
				var doc struct {
					Plugins map[string]json.RawMessage `json:"plugins"`
				}
				_ = json.Unmarshal(c.Value, &doc)
				if _, listed := doc.Plugins["alpha"]; !listed {
					order = append(order, "slice:husk.pods")
				}
			case c.Removed:
				order = append(order, "slice:"+c.Slice)
			default:
				t.Fatalf("slice %s changed without being removed: %s", c.Slice, m.Params)
			}
			continue
		}
		if m.Method != protocol.MethodEventDeliver {
			continue
		}
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch p.Name {
		case "pods.plugin-exited", "pods.plugin-removed", "services.reloaded":
			order = append(order, p.Name)
		}
	}
	if len(order) != 7 || order[0] != "pods.plugin-exited" || order[5] != "pods.plugin-removed" || order[6] != "services.reloaded" {
		t.Fatalf("order = %v", order)
	}
	slices := append([]string(nil), order[1:5]...)
	sort.Strings(slices)
	if want := []string{"slice:alpha.contributions", "slice:alpha.occupants", "slice:alpha.state", "slice:husk.pods"}; !reflect.DeepEqual(slices, want) {
		t.Fatalf("slice changes between exited and removed = %v, want %v", slices, want)
	}
}

// Two removals whose teardowns land in one tick (two `starting` Pods in one
// launch gate, both removed while it is pending) are two pods.plugin-removed
// events, not one coalesced by name.
func TestPluginRemovedEventsOfOneTickAreNotCoalesced(t *testing.T) {
	proceed := make(chan struct{})
	ready := make(chan struct{})
	var once sync.Once
	hook := func(stage string) error {
		if stage != "run" {
			return nil
		}
		once.Do(func() { close(ready) })
		<-proceed // hold launchPlugins: the test adds the Pods itself
		return errors.New("test over")
	}
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = []string{writeIdlePlugin(t, "alpha"), writeIdlePlugin(t, "beta")}
	}, withHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	cancel, _ := runHost(t, ready, h)
	defer func() { close(proceed); cancel() }()

	w := dial(t, instance.SocketPath(h.dir, h.id))
	if rep := w.callParams(t, "w", protocol.MethodEventWatch, map[string]any{"pattern": "pods.*"}); rep.Error != nil {
		t.Fatalf("event.watch: %+v", rep.Error)
	}

	// Both Pods exist, `starting`, with no launch gate yet.
	h.loop(func() {
		for _, spec := range h.root.specs {
			if _, err := h.root.pods.Add(spec); err != nil {
				t.Error(err)
			}
		}
	})
	replies := make(chan error, 2)
	for _, id := range []string{"alpha", "beta"} {
		go func() { _, err := h.remove(id); replies <- err }()
	}
	for i := 0; ; i++ {
		var both bool
		h.loop(func() { both = h.root.pods.Removing("alpha") && h.root.pods.Removing("beta") })
		if both {
			break
		}
		if i > 5000 {
			t.Fatal("both removals never began")
		}
		runtime.Gosched()
	}
	// Completing the gate tears both down in one PreparedMsg.
	var cmd tea.Cmd
	h.loop(func() { cmd = h.root.pods.Prepare() })
	h.root.spawn(cmd)
	for range 2 {
		if err := <-replies; err != nil {
			t.Fatalf("remove: %v", err)
		}
	}

	_ = w.c.SetReadDeadline(time.Now().Add(failBound))
	got := map[string]bool{}
	for len(got) < 2 {
		line, err := w.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("after %v: %v", got, err)
		}
		var m protocol.Message
		_ = json.Unmarshal(line, &m)
		var p struct {
			Name string `json:"name"`
			Data struct {
				Plugin string `json:"plugin_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if m.Method == protocol.MethodEventDeliver && p.Name == "pods.plugin-removed" {
			got[p.Data.Plugin] = true
		}
	}
	if !got["alpha"] || !got["beta"] {
		t.Fatalf("pods.plugin-removed for %v, want alpha and beta", got)
	}
}
