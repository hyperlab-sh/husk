package pods

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// incompleteOpeningProcess models a Wrapper that spawned but failed before Live.
type incompleteOpeningProcess struct{ *fakeProcess }

func (p incompleteOpeningProcess) Start([]protocol.Message) error { return nil }

func TestAddNilStartWithoutLiveFailsOnce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    wrapper.Liveness
		lastError string
	}{
		{"starting with fault", wrapper.Starting, "opening pipe closed"},
		{"starting without detail", wrapper.Starting, ""},
		{"failed", wrapper.Failed, "opening failed"},
		{"exited", wrapper.Exited, "exited during opening"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, pending := additionFixture(t)
			pub := &recordingPublisher{}
			f.onLoop(func() {
				f.h.opts.Publisher = pub
				f.h.opts.Factory = func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
					return incompleteOpeningProcess{&fakeProcess{id: s.ID, info: wrapper.Info{PluginID: s.ID, Status: tc.status, LastError: tc.lastError, LogPath: "/logs/alpha.log"}, opts: o, done: make(chan struct{}), term: make(chan struct{})}}, nil
				}
			})
			path := writeAddPlugin(t, t.TempDir(), "alpha", "alpha")
			reply := startAdd(f, t.Context(), contracts.PluginSource{Path: path})
			gate := testclock.Receive(t, pending)
			gate.release <- Preparation{Reports: addReports()}
			got := testclock.Receive(t, reply)
			if got.err != nil || len(got.result.Added) != 1 || got.result.Added[0].Status != "failed" || got.result.Added[0].Log != "/logs/alpha.log" {
				t.Fatalf("reply=%+v err=%v", got.result, got.err)
			}
			wantError := tc.lastError
			if wantError == "" {
				wantError = "plugin failed during opening"
			}
			f.onLoop(func() {
				p := f.h.pods["alpha"]
				if p.info.Status != wrapper.Failed || p.info.LastError != wantError {
					t.Errorf("Pod info=%+v", p.info)
				}
				if _, ok := f.h.Resolve(p.process.Token()); ok {
					t.Error("failed opening retained credential")
				}
				f.h.Handle(LaunchFailedMsg{PluginID: "alpha", Err: errors.New(wantError)})
				tail := []string{"late stderr"}
				late := ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Failed, LastError: "stopped reading: broken pipe", PID: 42, ExitStatus: "exit status 1", StderrTail: tail, LogPath: "/logs/alpha.final.log"}}
				f.h.Handle(late)
				f.h.Handle(late) // identical reap metadata must not write State twice
				tail[0] = "mutated after delivery"
				if p.info.Status != wrapper.Failed || p.info.LastError != wantError || p.info.PID != 42 || p.info.ExitStatus != "exit status 1" || p.info.LogPath != "/logs/alpha.final.log" || !reflect.DeepEqual(p.info.StderrTail, []string{"late stderr"}) {
					t.Errorf("late exit replaced failure or lost metadata: %+v", p.info)
				}
				failed, live, added, exited, errorLogs := 0, 0, 0, 0, 0
				for _, change := range f.loop.statuses {
					if change.Info.Status == wrapper.Failed {
						failed++
						if change.Info.LastError != wantError {
							t.Errorf("failed write changed error: %+v", change.Info)
						}
					}
					if change.Info.Status == wrapper.Live {
						live++
					}
				}
				for _, event := range pub.events {
					if event.name == PluginExitedEvent {
						exited++
					}
					if event.name == PluginAddedEvent {
						added++
						if !reflect.DeepEqual(event.data, map[string]any{"plugin_id": "alpha", "status": "failed"}) {
							t.Errorf("event=%+v", event)
						}
					}
				}
				for _, log := range f.loop.logs {
					if log.Level == "error" {
						errorLogs++
					}
				}
				if failed != 2 || live != 0 || added != 1 || exited != 1 || errorLogs != 1 {
					t.Errorf("failed writes=%d live writes=%d added=%d exited=%d error logs=%d", failed, live, added, exited, errorLogs)
				}
			})
		})
	}
}

func TestFinishAdditionNormalizesUnexpectedPodStatus(t *testing.T) {
	for _, status := range []wrapper.Liveness{wrapper.Starting, wrapper.Exited, ""} {
		t.Run(string(status), func(t *testing.T) {
			h, pub, _ := publisherFixture(t)
			p := addPod(t, h, "alpha")
			p.info.Status = status
			h.adding[1] = &addition{done: make(chan struct{}), result: contracts.PluginAddition{Added: []contracts.PluginAdded{{Plugin: "alpha"}}}}
			entry := h.adding[1]
			h.finishAddition(1, nil)
			if entry.result.Added[0].Status != "failed" || p.info.Status != wrapper.Failed {
				t.Fatalf("reply=%+v info=%+v", entry.result, p.info)
			}
			added := 0
			for _, event := range pub.events {
				if event.name == PluginAddedEvent {
					added++
					if !reflect.DeepEqual(event.data, map[string]any{"plugin_id": "alpha", "status": "failed"}) {
						t.Errorf("event=%+v", event)
					}
				}
			}
			if added != 1 {
				t.Fatalf("added events=%d", added)
			}
			firstError := p.info.LastError
			h.Handle(ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Exited, LastError: "different reap error"}})
			exited := 0
			for _, event := range pub.events {
				if event.name == PluginExitedEvent {
					exited++
				}
			}
			if exited != 1 || p.info.Status != wrapper.Failed || p.info.LastError != firstError {
				t.Fatalf("late exit after normalization: info=%+v exited=%d", p.info, exited)
			}
		})
	}
}
