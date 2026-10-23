package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// buildReferencePair builds the reference binary and lays out both reference
// plugin directories beside it, so "./reference" and
// "../reference/reference --contributor" resolve as in the source tree.
func buildReferencePair(t *testing.T) (publisher, contributor string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	root := t.TempDir()
	publisher = filepath.Join(root, "reference")
	contributor = filepath.Join(root, "reference-actions")
	for _, dir := range []string{publisher, contributor} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(goBin, "build", "-o", filepath.Join(publisher, "reference"), "github.com/hyperlab-sh/husk/plugins/reference")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for src, dst := range map[string]string{
		filepath.Join("..", "..", "plugins", "reference", manifest.FileName):         filepath.Join(publisher, manifest.FileName),
		filepath.Join("..", "..", "plugins", "reference-actions", manifest.FileName): filepath.Join(contributor, manifest.FileName),
	} {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return publisher, contributor
}

// TestReferencePairAgainstRealHost launches both reference plugins as real
// processes through the real wrapper, fires the publisher's refresh action as
// the CLI and checks what the publisher received from its before-refresh gate.
// The publisher's own view of the outcome is read from its accepted
// surface.update content (Config.Content).
func TestReferencePairAgainstRealHost(t *testing.T) {
	publisher, contributor := buildReferencePair(t)

	var mu sync.Mutex
	latest := map[protocol.SurfaceID][]string{} // publisher surface -> lines of its last surface.update
	tokens := map[string]string{}
	// The wrapper itself is the real one (the default factory in pods.Host); the factory only records each
	// process's token so the test can speak as the contributor.
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		p, err := wrapper.New(spec, o)
		if err == nil {
			mu.Lock()
			tokens[spec.ID] = p.Token()
			mu.Unlock()
		}
		return p, err
	}
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		pluginSettings(c)["paths"] = []string{publisher, contributor}
		c.Content = func(plugin string, p protocol.SurfaceUpdateParams) {
			if plugin != "go.reference" {
				return
			}
			mu.Lock()
			latest[p.Surface] = slices.Clone(p.Lines)
			mu.Unlock()
		}
	}, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	cancel, _ := runHost(t, ready, h)
	defer cancel()

	waitLive := func(id string) {
		t.Helper()
		deadline := time.Now().Add(failBound)
		for h.podStatus(id) != "live" {
			if time.Now().After(deadline) {
				t.Fatalf("%s never went live (status %q)", id, h.podStatus(id))
			}
			runtime.Gosched()
		}
	}
	waitLive("go.reference")
	waitLive("go.reference-actions")

	// The reference plugin draws only once the host says its surface is
	// visible and sized, which is the frontend's job: play the frontend.
	h.loop(func() {
		fe := h.root.Frontend()
		slices, err := fe.OwnSlices()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := slices.Geometry.Set(json.RawMessage(`{"terminal":{"width":160,"height":48},"too_small":false,"rects":{"main-stage":{"x":0,"y":0,"width":82,"height":22,"inner":{"x":1,"y":1,"width":80,"height":20}}}}`)); err != nil {
			t.Fatal(err)
		}
		if err := fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { v.Visible = []protocol.SurfaceID{protocol.SurfaceMainStage} }); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {}) // the tick after the write pushes surface.resized

	// The publisher refreshes asynchronously: the reply to action.invoke does
	// not carry the gate outcome, so wait for the publisher to render it.
	invoke := map[string]any{"action": "go.reference.refresh"}
	if m := rpc(t, h, "", protocol.MethodActionInvoke, invoke); m.Error != nil {
		t.Fatalf("action.invoke as cli: %+v", m.Error)
	}
	deadline := time.Now().Add(failBound)
	for {
		mu.Lock()
		var text string
		var allowed bool
		for _, lines := range latest {
			text += strings.Join(lines, "\n") + "\n"
			for _, l := range lines {
				// The status line is "size WxH · replies: <result>", padded.
				if _, res, ok := strings.Cut(l, "replies: "); ok && strings.TrimSpace(res) == "refresh allowed by 1" {
					allowed = true
				}
			}
		}
		mu.Unlock()
		if allowed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publisher never rendered the allowed outcome; last content:\n%s", text)
		}
		runtime.Gosched()
	}

	l := decodeList(t, rpc(t, h, "", protocol.MethodHookList, struct{}{}))
	var found bool
	for _, hk := range l.Hooks {
		if hk.ID != "go.reference.before-refresh" {
			continue
		}
		found = true
		if hk.Owner != "go.reference" || hk.Kind != "gate" || len(hk.Subscribers) != 1 ||
			hk.Subscribers[0].Plugin != "go.reference-actions" || !hk.Subscribers[0].Live {
			t.Fatalf("before-refresh = %+v", hk)
		}
	}
	if !found {
		t.Fatalf("hook.list lacks go.reference.before-refresh: %+v", l.Hooks)
	}

	// The caller rule: a foreign plugin cannot run the publisher's action and
	// the CLI never fires a hook (D74).
	mu.Lock()
	foreign := tokens["go.reference-actions"]
	mu.Unlock()
	if foreign == "" {
		t.Fatal("no token recorded for the contributor")
	}
	if m := rpc(t, h, foreign, protocol.MethodActionInvoke, invoke); m.Error == nil || m.Error.Code != protocol.CodeDenied {
		t.Fatalf("action.invoke as the contributor = %+v, want denied", m)
	}
	fire := map[string]any{"hook": "go.reference.before-refresh", "data": map[string]any{}}
	if m := rpc(t, h, "", protocol.MethodHookCall, fire); m.Error == nil || m.Error.Code != protocol.CodeDenied {
		t.Fatalf("hook.call as cli = %+v, want denied", m)
	}
}
