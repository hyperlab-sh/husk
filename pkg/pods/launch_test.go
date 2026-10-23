package pods

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestLaunchFailureReturnsToLoopBeforeUnbufferedPostDrains(t *testing.T) {
	loop := &fakeLoop{events: make(chan tea.Msg), ctx: t.Context()}
	dir := t.TempDir()
	h, err := New(loop, Options{Wrapper: wrapper.Options{
		Clock:   testclock.New(),
		LogPath: func(string) (string, error) { return filepath.Join(dir, "missing.log"), nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.cancel)
	pod, err := h.AddPod(manifest.Spec{ID: "missing", Protocol: protocol.Major, Command: []string{filepath.Join(dir, "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareAll(h)()
	returned := make(chan bool, 1)
	go func() { returned <- h.Handle(prepared) }()
	// No receiver for Loop.Post exists until Handle has returned. Program.Send
	// has this exact backpressure shape; a buffered fake would hide a deadlock.
	if !testclock.Receive(t, returned) {
		t.Fatal("preparation was not handled")
	}
	if _, ok := h.Resolve(pod.process.Token()); ok {
		t.Fatal("failed launch retained a credential")
	}
	first := testclock.Receive(t, loop.events)
	if _, ok := first.(ClosedMsg); !ok {
		t.Fatalf("first notice %T, want Closed", first)
	}
	second := testclock.Receive(t, loop.events)
	if _, ok := second.(LaunchFailedMsg); !ok {
		t.Fatalf("second notice %T, want launch failure", second)
	}
	h.Handle(second)
	testclock.Receive(t, pod.process.Done())
	if pod.Info().Status != wrapper.Failed {
		t.Fatal("launch failure did not reach loop liveness")
	}
}

func TestChannelCloseRevokesBeforeReapGraceWithoutCancellingCalls(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	pod := addPod(t, h, "p")
	prepareHost(t, h)
	procs["p"].opts.Post(ChannelClosedMsg{PluginID: "p"})
	if _, ok := h.Resolve(procs["p"].Token()); ok {
		t.Fatal("closed channel credential survived")
	}
	msg := testclock.Receive(t, loop.events)
	if _, ok := msg.(ChannelClosedMsg); !ok || !h.Handle(msg) {
		t.Fatal("precursor was mistaken for Calls cleanup")
	}
	if pod.Info().Status != wrapper.Live {
		t.Fatal("credential precursor changed loop liveness")
	}
}
