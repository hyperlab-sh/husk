package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPluginAddDirectoryPublishesEveryOutcomeWithoutCoalescing(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"alpha", "beta", "broken", "unclaimed"} {
		path := filepath.Join(root, id)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if id == "unclaimed" {
			continue
		}
		raw, _ := json.Marshal(map[string]any{"manifest_version": 2, "protocol": 2, "id": id, "name": id, "version": "1", "command": []string{"plugin"}, "events": map[string]any{"subscribe": []string{"pods.*"}}})
		if id == "broken" {
			raw = []byte("{")
		}
		if err := os.WriteFile(filepath.Join(path, manifest.FileName), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f := newFakes()
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook), WithFactory(func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if s.ID == "beta" {
			return nil, errors.New("injected start failure")
		}
		return f.factory(s, o)
	}))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	w := dial(t, instance.SocketPath(h.dir, h.id))
	if reply := w.callParams(t, "watch", protocol.MethodEventWatch, map[string]any{"pattern": "pods.*"}); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	code, stdout, stderr := cliRun(h, "plugin", "add", "--dir", root)
	var result contracts.PluginAddition
	if code != 1 || !strings.Contains(stderr, "plugins failed to launch: beta") || json.Unmarshal([]byte(stdout), &result) != nil || len(result.Added) != 2 || result.Added[0].Plugin != "alpha" || result.Added[0].Status != "live" || result.Added[1].Plugin != "beta" || result.Added[1].Status != "failed" || len(result.Refused) != 1 || result.Refused[0].Path != filepath.Join(root, "broken") || len(result.Reports) != 5 {
		t.Fatalf("directory add: %d %s %s", code, stdout, stderr)
	}
	_ = w.c.SetReadDeadline(time.Now().Add(failBound))
	events := map[string]string{}
	for len(events) < 2 {
		line, err := w.r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("events=%v: %v", events, err)
		}
		var message protocol.Message
		_ = json.Unmarshal(line, &message)
		var event struct {
			Name string
			Data struct {
				Plugin string `json:"plugin_id"`
				Status string
			}
		}
		_ = json.Unmarshal(message.Params, &event)
		if event.Name == pods.PluginAddedEvent {
			events[event.Data.Plugin] = event.Data.Status
		}
	}
	if !reflect.DeepEqual(events, map[string]string{"alpha": "live", "beta": "failed"}) {
		t.Fatal(events)
	}
	h.loop(func() {
		if len(h.root.Frontend().Refusals()) != 0 {
			t.Error("runtime conversion failure entered frontend refusals")
		}
	})
	// The live subscriber receives its own event and the other plugin's failed
	// outcome, proving refresh of the event table before opening the gate.
	alpha := f.proc("alpha")
	alpha.mu.Lock()
	defer alpha.mu.Unlock()
	own, failed := false, false
	for _, message := range alpha.sent {
		var event struct {
			Name string
			Data struct {
				Plugin string `json:"plugin_id"`
			}
		}
		_ = json.Unmarshal(message.Params, &event)
		if event.Name == pods.PluginAddedEvent {
			own = own || event.Data.Plugin == "alpha"
			failed = failed || event.Data.Plugin == "beta"
		}
	}
	if !own || !failed {
		t.Error("added subscriber did not receive every outcome")
	}
}
