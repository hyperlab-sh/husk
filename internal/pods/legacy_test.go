package pods

import (
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
)

// drain feeds every message the fake loop holds back through Handle.
func drain(h *Host, l *fakeLoop) {
	for {
		select {
		case m := <-l.events:
			h.Handle(m)
		default:
			return
		}
	}
}

func TestFailLegacyOnlyFailsALivePod(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    string
		setup func(t *testing.T, h *Host)
	}{
		{"unknown id", "ghost", func(t *testing.T, h *Host) { addPod(t, h, "p"); prepareHost(t, h) }},
		{"empty id", "", func(t *testing.T, h *Host) { addPod(t, h, "p"); prepareHost(t, h) }},
		{"starting pod", "p", func(t *testing.T, h *Host) { addPod(t, h, "p") }},
		{"failed pod", "p", func(t *testing.T, h *Host) {
			addPod(t, h, "p")
			prepareHost(t, h)
			h.Handle(ExitMsg{Info: wrapper.Info{PluginID: "p", Status: wrapper.Failed, LastError: "crashed"}})
		}},
		{"exited pod", "p", func(t *testing.T, h *Host) {
			addPod(t, h, "p")
			prepareHost(t, h)
			h.Handle(ExitMsg{Info: wrapper.Info{PluginID: "p", Status: wrapper.Exited}})
		}},
		{"pod being removed", "p", func(t *testing.T, h *Host) {
			addPod(t, h, "p")
			prepareHost(t, h)
			h.removing["p"] = &removal{}
		}},
		{"host stopping", "p", func(t *testing.T, h *Host) {
			addPod(t, h, "p")
			prepareHost(t, h)
			h.closing = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, loop, _, procs := publisherFixtureWithLoop(t)
			tc.setup(t, h)
			before := len(loop.statuses)
			h.FailLegacy(tc.id)
			drain(h, loop)
			for id, p := range procs {
				if p.versionMismatch != 0 {
					t.Fatalf("pod %s was failed", id)
				}
			}
			if len(loop.statuses) != before {
				t.Fatalf("liveness written for a no-op: %+v", loop.statuses[before:])
			}
		})
	}
}

func publisherFixtureWithLoop(t *testing.T) (*Host, *fakeLoop, *recordingPublisher, map[string]*fakeProcess) {
	t.Helper()
	h, loop, _, procs := hostFixture(t)
	pub := &recordingPublisher{}
	h.opts.Publisher = pub
	return h, loop, pub, procs
}

func TestFailLegacyFailsTheLivePodWithVersionMismatch(t *testing.T) {
	h, loop, pub, procs := publisherFixtureWithLoop(t)
	addPod(t, h, "alpha")
	addPod(t, h, "beta")
	prepareHost(t, h)
	if _, ok := h.Resolve("test-token-alpha"); !ok {
		t.Fatal("token not registered")
	}
	h.FailLegacy("alpha")
	drain(h, loop)

	if procs["alpha"].versionMismatch != 1 || procs["beta"].versionMismatch != 0 {
		t.Fatalf("alpha %d beta %d", procs["alpha"].versionMismatch, procs["beta"].versionMismatch)
	}
	info := h.pods["alpha"].Info()
	if info.Status != wrapper.Failed || !strings.Contains(info.LastError, "unsupported protocol major 1") {
		t.Fatalf("alpha = %+v", info)
	}
	if h.pods["beta"].Info().Status != wrapper.Live {
		t.Fatal("beta was disturbed")
	}
	if _, ok := h.Resolve("test-token-alpha"); ok {
		t.Fatal("alpha's token survived the failure")
	}
	if _, ok := h.Resolve("test-token-beta"); !ok {
		t.Fatal("beta's token was revoked")
	}
	if len(pub.events) != 1 || pub.events[0].name != PluginExitedEvent {
		t.Fatalf("events = %+v", pub.events)
	}
	data := pub.events[0].data.(map[string]any)
	if data["plugin_id"] != "alpha" || data["status"] != "failed" || !strings.Contains(data["last_error"].(string), "unsupported protocol major 1") {
		t.Fatalf("payload = %#v", data)
	}
	// A second hello for the now-failed pod changes nothing.
	h.FailLegacy("alpha")
	drain(h, loop)
	if procs["alpha"].versionMismatch != 1 || len(pub.events) != 1 {
		t.Fatal("a failed pod was failed again")
	}
}
