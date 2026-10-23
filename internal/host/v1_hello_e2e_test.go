package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// buildV1Plugin compiles the fake major-1 plugin (testdata/v1plugin).
func buildV1Plugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "v1plugin")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/v1plugin").CombinedOutput(); err != nil {
		t.Fatalf("go build v1plugin: %v\n%s", err, out)
	}
	return bin
}

// v1PluginDir lays out a plugin directory with a v1-style manifest (no
// manifest_version, no protocol) running the fake: it claims the id claim in
// its hello and records the host's answer in out ("-": idle, no hello).
func v1PluginDir(t *testing.T, bin, id, out, claim string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v1plugin"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": id, "name": id, "version": "1", "command": []string{"./v1plugin", out, claim}})
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// legacySpy forwards to the real LegacyPeer and reports each id after the
// forward, so a test can wait for the call without sleeping.
type legacySpy struct {
	next server.LegacyPeer
	ids  chan string
}

func (s *legacySpy) FailLegacy(id string) { s.next.FailLegacy(id); s.ids <- id }

func withLegacySpy(spy *legacySpy) Option {
	return withServer(func(c server.Config) server.Server {
		spy.next = c.Legacy
		c.Legacy = spy
		return server.New(c)
	})
}

// chanNotifier passes each notification to a channel.
type chanNotifier struct {
	ch chan protocol.Message
}

func (n *chanNotifier) Notify(m protocol.Message) error { n.ch <- m; return nil }

func bootV1(t *testing.T, plugins ...string) (*harness, *legacySpy, *chanNotifier, <-chan string) {
	t.Helper()
	spy := &legacySpy{ids: make(chan string, 4)}
	logs := make(chan string, 64)
	logger := WithLogger(func(level, msg string) {
		select {
		case logs <- level + ": " + msg:
		default:
		}
	})
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = plugins }, withHook(hook), withLegacySpy(spy), logger)
	if err != nil {
		t.Fatal(err)
	}
	exited := &chanNotifier{ch: make(chan protocol.Message, 16)}
	h.loop(func() {
		if _, err := h.root.bus.Subscribe(exited, "pods.plugin-exited"); err != nil {
			t.Error(err)
		}
	})
	runHost(t, ready, h)
	return h, spy, exited, logs
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	ctx := boundCtx(t)
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("nothing arrived within the bound")
	}
	panic("unreachable")
}

// A v1 plugin that never writes stdout and speaks only over the socket is
// answered with the major-1 line, failed with version_mismatch and gone.
func TestV1HelloOnTheSocketFailsThePlugin(t *testing.T) {
	bin := buildV1Plugin(t)
	out := filepath.Join(t.TempDir(), "answer")
	h, spy, exited, _ := bootV1(t, v1PluginDir(t, bin, "v1", out, "v1"))

	if id := receive(t, spy.ids); id != "v1" {
		t.Fatalf("FailLegacy(%q), want v1", id)
	}
	var event struct {
		Name string
		Data map[string]any
	}
	for event.Data["plugin_id"] != "v1" {
		if err := json.Unmarshal(receive(t, exited.ch).Params, &event); err != nil {
			t.Fatal(err)
		}
	}
	if event.Data["status"] != "failed" || !strings.Contains(event.Data["last_error"].(string), "unsupported protocol major 1: host speaks "+protocol.Version) {
		t.Fatalf("pods.plugin-exited = %+v", event.Data)
	}

	// The fake wrote its answer before it exited.
	answer, err := os.ReadFile(out)
	if err != nil || string(answer) != string(protocol.LegacyRejectionLine()) {
		t.Fatalf("answer %q, %v; want the major-1 line", answer, err)
	}
	var done <-chan struct{}
	h.loop(func() {
		p, ok := h.root.pods.Pod("v1")
		if !ok {
			t.Error("no pod v1")
			return
		}
		done = p.ProcessDone()
	})
	receive(t, done) // the process is gone

	code, stdout, stderr := cliRun(h, "plugin", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var rows []struct {
		ID, Status string
		LastError  string `json:"last_error"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 1 || rows[0].ID != "v1" || rows[0].Status != "failed" || !strings.Contains(rows[0].LastError, "unsupported protocol major 1") {
		t.Fatalf("plugin list = %q (%v)", stdout, err)
	}
}

// A hello naming no live pod gets the line and the close; nothing is stopped.
func TestV1HelloNamingNoLivePodStopsNothing(t *testing.T) {
	bin := buildV1Plugin(t)
	out := filepath.Join(t.TempDir(), "answer")
	alpha := v1PluginDir(t, bin, "alpha", "-", "-")
	stranger := v1PluginDir(t, bin, "stranger", out, "someone-else")
	h, spy, exited, logs := bootV1(t, alpha, stranger)

	if id := receive(t, spy.ids); id != "someone-else" {
		t.Fatalf("FailLegacy(%q), want someone-else", id)
	}
	// Only the no-op branch of the loop handler writes this line, so seeing it
	// proves the hello was ignored rather than not yet applied.
	for line := receive(t, logs); !strings.Contains(line, `names no live plugin ("someone-else")`); line = receive(t, logs) {
	}
	// Fence: a wrongly failed alpha would publish pods.plugin-exited; stopping
	// the stranger is a deterministic event after it.
	h.mustRemove("stranger")
	for {
		var event struct{ Data map[string]any }
		if err := json.Unmarshal(receive(t, exited.ch).Params, &event); err != nil {
			t.Fatal(err)
		}
		if event.Data["plugin_id"] == "alpha" {
			t.Fatalf("alpha exited: %+v", event.Data)
		}
		if event.Data["plugin_id"] == "stranger" {
			break
		}
	}
	if got := h.podStatus("alpha"); got != "live" {
		t.Fatalf("alpha = %q, want live", got)
	}
}
