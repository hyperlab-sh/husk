package pods

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
)

type publishedEvent struct {
	name string
	data any
}

type recordingPublisher struct{ events []publishedEvent }

func (p *recordingPublisher) Publish(name string, data any) {
	p.events = append(p.events, publishedEvent{name, data})
}

func publisherFixture(t *testing.T) (*Host, *recordingPublisher, map[string]*fakeProcess) {
	t.Helper()
	h, _, _, procs := hostFixture(t)
	pub := &recordingPublisher{}
	h.opts.Publisher = pub
	return h, pub, procs
}

func TestExitPublishesPluginExitedWithTheHuskPodsEntry(t *testing.T) {
	started := time.Date(2026, 10, 22, 6, 1, 2, 0, time.UTC)
	for _, tc := range []struct {
		name string
		info wrapper.Info
		want map[string]any
	}{
		{"non-zero exit", wrapper.Info{PluginID: "p", Status: wrapper.Failed, PID: 4242, StartedAt: started, LastError: "exit status 7", LogPath: "/logs/p.log"},
			map[string]any{"plugin_id": "p", "status": "failed", "pid": 4242, "started_at": "2026-10-22T06:01:02Z", "last_error": "exit status 7", "log": "/logs/p.log"}},
		{"stdout EOF with the process alive", wrapper.Info{PluginID: "p", Status: wrapper.Failed, PID: 4242, LastError: "stopped reading: wrapper: stdout closed while process is running"},
			map[string]any{"plugin_id": "p", "status": "failed", "pid": 4242, "last_error": "stopped reading: wrapper: stdout closed while process is running"}},
		{"stdout read error", wrapper.Info{PluginID: "p", Status: wrapper.Failed, PID: 4242, LastError: "stopped reading: read stdout: connection reset by peer"},
			map[string]any{"plugin_id": "p", "status": "failed", "pid": 4242, "last_error": "stopped reading: read stdout: connection reset by peer"}},
		{"clean exit", wrapper.Info{PluginID: "p", Status: wrapper.Exited, PID: 4242},
			map[string]any{"plugin_id": "p", "status": "exited", "pid": 4242}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, pub, _ := publisherFixture(t)
			addPod(t, h, "p")
			prepareHost(t, h)
			h.Handle(ExitMsg{Info: tc.info})
			if len(pub.events) != 1 || pub.events[0].name != "pods.plugin-exited" {
				t.Fatalf("events = %+v, want one pods.plugin-exited", pub.events)
			}
			if got := pub.events[0].data; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestLaunchFailurePublishesPluginExitedOnce(t *testing.T) {
	h, pub, _ := publisherFixture(t)
	addPod(t, h, "files")
	err := errors.New("exec: no such file or directory")
	h.Handle(LaunchFailedMsg{PluginID: "files", Err: err})
	h.Handle(LaunchFailedMsg{PluginID: "files", Err: err, LogPath: "/logs/files.log"}) // the Wrapper's duplicate
	if len(pub.events) != 1 {
		t.Fatalf("events = %+v, want exactly one", pub.events)
	}
	want := map[string]any{"plugin_id": "files", "status": "failed", "last_error": err.Error()}
	if got := pub.events[0]; got.name != "pods.plugin-exited" || !reflect.DeepEqual(got.data, want) {
		t.Fatalf("event = %#v, want pods.plugin-exited %#v", got, want)
	}
}

func TestStartFailurePublishesPluginExited(t *testing.T) {
	h, pub, procs := publisherFixture(t)
	addPod(t, h, "p")
	cmd := prepareAll(h)
	// The process refuses to start; the gate reports the failure.
	h.opts.Factory = func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		p := &fakeProcess{id: s.ID, info: wrapper.Info{PluginID: s.ID, Status: wrapper.Starting}, opts: o, done: make(chan struct{}), term: make(chan struct{}), startErr: errors.New("spawn refused")}
		procs[s.ID] = p
		return p, nil
	}
	h.Handle(cmd())
	if len(pub.events) != 1 || pub.events[0].data.(map[string]any)["last_error"] != "spawn refused" {
		t.Fatalf("events = %+v, want one failed event for the start error", pub.events)
	}
}

func TestExitOfUnknownPodPublishesNothingAndNilPublisherIsSafe(t *testing.T) {
	h, pub, _ := publisherFixture(t)
	h.Handle(ExitMsg{Info: wrapper.Info{PluginID: "ghost", Status: wrapper.Failed}})
	h.Handle(LaunchFailedMsg{PluginID: "ghost", Err: errors.New("x")})
	if len(pub.events) != 0 {
		t.Fatalf("events = %+v for a Pod that does not exist", pub.events)
	}

	quiet, _, _, _ := hostFixture(t) // no Publisher
	addPod(t, quiet, "p")
	quiet.Handle(ExitMsg{Info: wrapper.Info{PluginID: "p", Status: wrapper.Failed}})
}
