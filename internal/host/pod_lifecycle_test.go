package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// stageOccupant is the resolved occupant of main-stage.
func stageOccupant(h *harness) string {
	var occupant string
	h.loop(func() {
		res, _ := decodeSurfaces(h.root.st.Snapshot())
		occupant = res.Occupants["main-stage"].Plugin
	})
	return occupant
}

// D70: a plugin whose command does not exist never holds the stage; the next
// claimant does.
func TestFailedLaunchDoesNotHoldASurface(t *testing.T) {
	broken := writeFrontendPlugin(t, "alpha", []string{"main-stage"}, nil)
	good := writeFrontendPlugin(t, "beta", []string{"main-stage"}, nil)
	f := newFakes()
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if spec.ID == "alpha" {
			return nil, errors.New("exec: no such file or directory")
		}
		return f.factory(spec, o)
	}
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{broken, good} }, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started // beta; alpha never starts

	if got := h.podStatus("alpha"); got != "failed" {
		t.Fatalf("alpha status = %q, want failed", got)
	}
	if got := stageOccupant(h); got != "beta" {
		t.Fatalf("main-stage occupant = %q, want beta", got)
	}
}

// D70: when the occupant leaves live the waiting claimant takes the surface
// and is told its size.
func TestOccupantThatFailsHandsTheStageOverAndIsTold(t *testing.T) {
	alpha := writeFrontendPlugin(t, "alpha", []string{"main-stage"}, nil)
	beta := writeFrontendPlugin(t, "beta", []string{"main-stage"}, nil)
	h, _, fe := bootFrontendWith(t, nil, alpha, beta)
	var told recNotifier
	h.loop(func() {
		s, err := fe.OwnSlices()
		if err != nil {
			t.Error(err)
			return
		}
		h.root.engine.WatchPlugin("beta", &told)
		if _, err := s.Geometry.Set(geometryOf(map[string][2]int{"main-stage": {80, 20}})); err != nil {
			t.Error(err)
		}
	})
	if got := stageOccupant(h); got != "alpha" {
		t.Fatalf("precondition: main-stage occupant = %q, want alpha", got)
	}
	h.loop(func() {
		if got := told.resized(t); len(got) != 0 {
			t.Errorf("beta heard about a surface it does not hold: %+v", got)
		}
	})

	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Failed, LastError: "crashed (exit status 1)"}})
	h.loop(func() {
		res, _ := decodeSurfaces(fe.Snapshot())
		if got := res.Occupants["main-stage"].Plugin; got != "beta" {
			t.Errorf("main-stage occupant after alpha failed = %q, want beta", got)
		}
		got := told.resized(t)
		if len(got) != 1 || len(got[0]) != 1 || got[0][0].Surface != protocol.SurfaceID("main-stage") || got[0][0].Width != 80 || got[0][0].Height != 20 {
			t.Errorf("beta's surface.resized = %+v, want main-stage at 80x20", got)
		}
	})
}

// D71b: a plugin refused before a Pod exists reaches the frontend through
// Refusals, once per refusal, and the healthy plugin still launches.
func TestRefusedPluginsAreListedForTheFrontend(t *testing.T) {
	good := writeFrontendPlugin(t, "good", []string{"main-stage"}, nil)
	root := t.TempDir()
	copyDir := func(name, doc string) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const base = `{"manifest_version":2,"protocol":%d,"id":%q,"name":"n","version":"1","command":["./p"]%s}`
	copyDir("a-invalid", `{"manifest_version":2,"protocol":2,"id":"invalid","name":"n","command":["./p"]}`) // no version
	copyDir("b-first", fmt.Sprintf(base, 2, "twin", ""))
	copyDir("c-second", fmt.Sprintf(base, 2, "twin", ""))
	copyDir("d-old", fmt.Sprintf(base, 1, "old", ""))
	copyDir("e-ok", fmt.Sprintf(base, 2, "ok", ""))

	f := newFakes()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = []string{good}
		c.PluginDirs = []string{root}
	}, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	for range 3 { // good, twin, ok
		<-f.started
	}

	got := h.root.Frontend().Refusals()
	byPlugin := map[string]string{}
	var anonymous []string
	for _, r := range got {
		if r.Plugin == "" {
			anonymous = append(anonymous, r.Reason)
			continue
		}
		byPlugin[r.Plugin] = r.Reason
	}
	if byPlugin["twin"] != "duplicate plugin id" {
		t.Errorf("duplicate id: refusals = %+v", got)
	}
	if len(anonymous) != 2 {
		t.Fatalf("manifest refusals = %q, want the invalid manifest and the protocol mismatch (all: %+v)", anonymous, got)
	}
	var invalid, mismatch bool
	for _, reason := range anonymous {
		invalid = invalid || (strings.Contains(reason, "a-invalid") && strings.Contains(reason, "version"))
		mismatch = mismatch || (strings.Contains(reason, "d-old") && strings.Contains(reason, "protocol"))
	}
	if !invalid || !mismatch {
		t.Errorf("manifest refusals = %q, want one naming a-invalid's version and one naming d-old's protocol", anonymous)
	}
	if len(got) != 3 {
		t.Errorf("refusals = %+v, want exactly the three", got)
	}
}

// D71a: pods.plugin-exited reaches an event watcher when a Pod fails to
// launch and when it exits.
func TestPluginExitedEventReachesAWatcher(t *testing.T) {
	broken := writeFrontendPlugin(t, "alpha", []string{"main-stage"}, nil)
	good := writeFrontendPlugin(t, "beta", []string{"sidebar-list"}, nil)
	f := newFakes()
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if spec.ID == "alpha" {
			return nil, errors.New("exec: no such file or directory")
		}
		return f.factory(spec, o)
	}
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{broken, good} }, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	var watcher recNotifier
	h.loop(func() {
		if _, err := h.root.bus.Subscribe(&watcher, "pods.plugin-exited"); err != nil {
			t.Error(err)
		}
	})
	runHost(t, ready, h)
	<-f.started // beta

	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "beta", Status: wrapper.Failed, LastError: "crashed (exit status 3)"}})
	// The exit's Update publishes inline and the bus delivers at the end of
	// that tick; one loop turn makes sure the posted ExitMsg was applied.
	h.loop(func() {})

	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	seen := map[string]map[string]any{}
	for _, m := range watcher.msgs {
		var p struct {
			Name string         `json:"name"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.Name != "pods.plugin-exited" {
			continue
		}
		seen[p.Data["plugin_id"].(string)] = p.Data
	}
	if got := seen["alpha"]; got["status"] != "failed" || got["last_error"] != "exec: no such file or directory" {
		t.Errorf("launch failure event = %+v", got)
	}
	if got := seen["beta"]; got["status"] != "failed" || got["last_error"] != "crashed (exit status 3)" {
		t.Errorf("exit event = %+v", got)
	}
}

// A real Post (tea.Program.Send) blocks until the loop reads, so publishing
// pods.plugin-exited from inside Update through Post stalls the loop that
// runs it: a sibling's launch waited out the whole settle window.
func TestPluginExitedIsNotPostedFromTheLoop(t *testing.T) {
	broken := writeFrontendPlugin(t, "alpha", []string{"main-stage"}, nil)
	good := writeFrontendPlugin(t, "beta", []string{"sidebar-list"}, nil)
	f := newFakes()
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if spec.ID == "alpha" {
			return nil, errors.New("exec: no such file or directory")
		}
		return f.factory(spec, o)
	}
	var h *harness
	var onLoop atomic.Bool
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = []string{broken, good}
		next := c.Post
		c.Post = func(m any) bool {
			if h.root.onLoop() {
				onLoop.Store(true)
				return false
			}
			return next(m)
		}
	}, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started // beta launches after alpha failed
	h.loop(func() {})
	if got := h.podStatus("alpha"); got != "failed" {
		t.Fatalf("alpha status = %q, want failed", got)
	}
	if onLoop.Load() {
		t.Error("Post was called from the loop goroutine")
	}
}

// D71a: the event is the Pod's husk.pods entry. An exit report that lacks
// fields an earlier write set leaves them in the entry (the write merges), so
// the event must keep them too.
func TestPluginExitedEventCarriesTheMergedEntry(t *testing.T) {
	good := writeFrontendPlugin(t, "beta", []string{"sidebar-list"}, nil)
	f := newFakes()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{good} }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	var watcher recNotifier
	h.loop(func() {
		if _, err := h.root.bus.Subscribe(&watcher, "pods.plugin-exited"); err != nil {
			t.Error(err)
		}
	})
	runHost(t, ready, h)
	<-f.started

	started := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "beta", Status: wrapper.Failed, PID: 4242, StartedAt: started, LogPath: "/logs/beta.log", LastError: "crashed (exit status 3)"}})
	h.loop(func() {})
	// A later report with none of pid, started_at or log.
	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "beta", Status: wrapper.Failed, LastError: "stdout closed"}})
	h.loop(func() {})

	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if len(watcher.msgs) < 2 {
		t.Fatalf("events = %d, want 2", len(watcher.msgs))
	}
	var p struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(watcher.msgs[len(watcher.msgs)-1].Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.Data["last_error"] != "stdout closed" || p.Data["log"] != "/logs/beta.log" || p.Data["pid"] != float64(4242) || p.Data["started_at"] != started.Format(time.RFC3339Nano) || p.Data["plugin_id"] != "beta" {
		t.Errorf("event = %+v, want the merged entry plus plugin_id", p.Data)
	}
}
