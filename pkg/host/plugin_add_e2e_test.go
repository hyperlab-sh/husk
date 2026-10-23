package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/pods"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestPluginAddRealHostCatalogEventOrderAndPersistentReadd(t *testing.T) {
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
	doc["slice"] = map[string]any{"scope": "instance", "persistent": true, "retention": map[string]any{"policy": "keep-latest"}}
	doc["keybindings"] = []any{map[string]any{"key": "n", "action": "alpha.new", "focus": "sidebar-list"}}
	doc["secret_requests"] = []any{map[string]any{"name": "api.token", "purpose": "test access"}}
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	created := make(chan wrapper.Process, 2)
	var prompts atomic.Int32
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook), WithFactory(func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		p, err := wrapper.New(s, o)
		if err == nil {
			created <- p
		}
		return p, err
	}), WithPrompt(func(context.Context, secrets.Prompt) secrets.Answer { prompts.Add(1); return secrets.Declined }), func(o *options) { o.keyring = &memRing{m: map[string]string{}} })
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	h.loop(func() {}) // settle initial core slices before subscribing
	w := dial(t, instance.SocketPath(h.dir, h.id))
	for i, spec := range []struct {
		method string
		params map[string]any
	}{
		{protocol.MethodEventWatch, map[string]any{"pattern": "pods.*"}},
		{protocol.MethodEventWatch, map[string]any{"pattern": "services.*"}},
		{protocol.MethodEventWatch, map[string]any{"pattern": "state.changed"}},
		{protocol.MethodStateWatch, map[string]any{"slices": []string{"husk.pods"}}},
	} {
		if reply := w.callParams(t, string(rune('a'+i)), spec.method, spec.params); reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	add := func() contracts.PluginAddition {
		t.Helper()
		code, stdout, stderr := cliRun(h, "plugin", "add", path, "--format", "json")
		var result contracts.PluginAddition
		if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil || len(result.Added) != 1 || result.Added[0].Status != "live" || result.Added[0].Log == "" || len(result.Reports) != 5 {
			t.Fatalf("add exit=%d result=%s error=%s", code, stdout, stderr)
		}
		return result
	}
	first := add()
	oldProcess := <-created
	oldToken := oldProcess.Token()
	if code, stdout, stderr := cliRun(h, "plugin", "list"); code != 0 || !strings.Contains(stdout, `"status":"live"`) || strings.Contains(stdout, `"status":"starting"`) {
		t.Fatalf("list: %d %s %s", code, stdout, stderr)
	}
	if code, stdout, stderr := cliRun(h, "rpc", protocol.MethodKeysList, `{}`); code != 0 || !strings.Contains(stdout, "alpha.new") {
		t.Fatalf("keys: %d %s %s", code, stdout, stderr)
	}
	if code, stdout, stderr := cliRun(h, "action", "list"); code != 0 || !strings.Contains(stdout, "alpha.new") {
		t.Fatalf("actions: %d %s %s", code, stdout, stderr)
	}
	if code, _, stderr := cliRun(h, "plugin", "add", path); code != 1 || !strings.Contains(stderr, "duplicate_plugin_id") {
		t.Fatalf("duplicate: %d %s", code, stderr)
	}
	_ = w.c.SetReadDeadline(time.Now().Add(failBound))
	order := []string{}
	busOrder := []string{}
	for len(order) == 0 || order[len(order)-1] != pods.PluginAddedEvent {
		line, err := w.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("event order %v: %v", order, err)
		}
		var message protocol.Message
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatal(err)
		}
		switch message.Method {
		case protocol.MethodStateChanged:
			var change struct {
				Value struct {
					Plugins map[string]struct{ Status string }
				}
			}
			_ = json.Unmarshal(message.Params, &change)
			if status := change.Value.Plugins["alpha"].Status; status != "" {
				order = append(order, status)
			}
		case protocol.MethodEventDeliver:
			var event struct {
				Name string
				Data struct {
					Reason   string
					Slice    string
					PluginID string `json:"plugin_id"`
					Status   string
				}
			}
			_ = json.Unmarshal(message.Params, &event)
			if event.Name == "state.changed" && event.Data.Slice == "husk.pods" {
				busOrder = append(busOrder, event.Name)
			}
			if event.Name == "services.reloaded" && event.Data.Reason == "plugin" {
				order = append(order, event.Name)
				busOrder = append(busOrder, event.Name)
			}
			if event.Name == pods.PluginAddedEvent {
				if event.Data.PluginID != "alpha" || event.Data.Status != "live" {
					t.Fatalf("added event = %s", message.Params)
				}
				order = append(order, event.Name)
				busOrder = append(busOrder, event.Name)
			}
		}
	}
	if !reflect.DeepEqual(busOrder, []string{"state.changed", "services.reloaded", "state.changed", pods.PluginAddedEvent}) {
		t.Fatal("bus event order", busOrder)
	}
	if !reflect.DeepEqual(order, []string{"starting", "services.reloaded", "live", pods.PluginAddedEvent}) {
		t.Fatal("event order", order)
	}
	h.loop(func() {
		handle, ok := pods.NewPodSlices(h.root.pods).Handle("alpha", "alpha.state")
		if !ok {
			t.Error("added Pod has no slice")
		} else if _, err := handle.Set(json.RawMessage(`{"cursor":7}`)); err != nil {
			t.Error(err)
		}
	})
	before := h.root.st.Snapshot().Slices["alpha.state"].Revision
	for _, call := range []struct{ method, params string }{
		{protocol.MethodSecretsSet, `{"name":"api.token","value":"test-value"}`},
		{protocol.MethodSecretsGrant, `{"plugin":"alpha","name":"api.token"}`},
	} {
		if code, _, stderr := cliRun(h, "rpc", call.method, call.params); code != 0 {
			t.Fatalf("%s: %d %s", call.method, code, stderr)
		}
	}
	h.mustRemove("alpha")
	if _, ok := h.root.pods.Resolve(oldToken); ok {
		t.Fatal("removed credential still resolves")
	}
	second := add()
	freshProcess := <-created
	if freshProcess.Token() == oldToken || first.Added[0].Log == second.Added[0].Log {
		t.Fatal("re-add reused credential or log")
	}
	entry := h.root.st.Snapshot().Slices["alpha.state"]
	if string(entry.Value) != `{"cursor":7}` || entry.Revision <= before {
		t.Fatalf("hydrated value=%s revision=%d previous=%d", entry.Value, entry.Revision, before)
	}
	if _, err := h.root.rt.Secrets.Read(t.Context(), secrets.Caller{Kind: protocol.CallerPlugin, PluginID: "alpha"}, "api.token"); err != nil || prompts.Load() != 0 {
		t.Fatalf("re-add lost grant: %v, prompts=%d", err, prompts.Load())
	}
	h.loop(func() {
		if len(h.root.Frontend().Refusals()) != 0 {
			t.Error("runtime refusals entered startup toast list")
		}
		if h.root.podWatch["alpha"] == nil {
			t.Error("added Pod missing root delivery bookkeeping")
		}
		if h.root.store.Declared(store.DeclaredPlugins)["alpha"] == nil {
			t.Error("re-add did not declare catalog")
		}
	})
}
