package pods

import (
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
)

func TestSharedKillWaitIsBoundedForUnreapableProcess(t *testing.T) {
	h, _, clock, procs := hostFixture(t)
	addPod(t, h, "p")
	prepareHost(t, h)
	procs["p"].ignore = true
	procs["p"].stubborn = true
	cmd := h.Stop()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	clock.Await(t, wrapper.StopGrace)
	clock.Advance(wrapper.StopGrace)
	clock.Await(t, wrapper.KillWait)
	clock.Advance(wrapper.KillWait)
	if stopped := testclock.Receive(t, result).(StoppedMsg); stopped.Err == nil {
		t.Fatal("unreapable process did not reach bounded failure")
	}
}

func TestCompletedGroupDoesNotProduceFalseSurvivorWarning(t *testing.T) {
	h, loop, clock, procs := hostFixture(t)
	addPod(t, h, "p")
	prepareHost(t, h)
	procs["p"].ignore = true
	procs["p"].killErr = os.ErrProcessDone // group empty while reader completion catches up
	result := make(chan tea.Msg, 1)
	cmd := h.Stop()
	go func() { result <- cmd() }()
	clock.Await(t, wrapper.StopGrace)
	clock.Advance(wrapper.StopGrace)
	if stopped := testclock.Receive(t, result).(StoppedMsg); stopped.Err != nil {
		t.Fatal(stopped.Err)
	}
	select {
	case msg := <-loop.events:
		t.Fatal("already-empty group produced a survivor warning", msg)
	default:
	}
}

func TestShutdownDoesNotRestartGraceAfterSignalingEachPlugin(t *testing.T) {
	h, _, clock, procs := hostFixture(t)
	addPod(t, h, "a")
	addPod(t, h, "b")
	prepareHost(t, h)
	for _, p := range procs {
		p.ignore = true
		p.termStep = func() { clock.Advance(1200 * time.Millisecond) }
	}
	cmd := h.Stop()
	if m := cmd().(StoppedMsg); m.Err != nil {
		t.Fatal(m.Err)
	}
	for _, p := range procs {
		if !p.killed {
			t.Fatal("signal overhead incorrectly extended the shared stop deadline")
		}
	}
}

func TestReapedBridgeRevokesBeforeFinalDrainWithoutChangingLiveness(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	pod := addPod(t, h, "p")
	prepareHost(t, h)
	before := len(loop.statuses)
	procs["p"].opts.Post(ReapedMsg{PluginID: "p"})
	if _, ok := h.Resolve(procs["p"].Token()); ok {
		t.Fatal("reaped credential remained valid")
	}
	if !h.Handle(testclock.Receive(t, loop.events)) || pod.Info().Status != wrapper.Live || len(loop.statuses) != before {
		t.Fatal("reap precursor cancelled calls or prematurely changed liveness")
	}
}

func TestShutdownDoesNotSignalCompletedWrappers(t *testing.T) {
	h, _, _, procs := hostFixture(t)
	addPod(t, h, "p")
	prepareHost(t, h)
	procs["p"].once.Do(func() { close(procs["p"].done) })
	if result := h.Stop()().(StoppedMsg); result.Err != nil {
		t.Fatal(result.Err)
	}
	if procs["p"].terminated || procs["p"].killed {
		t.Fatal("completed pid was signalled again")
	}
}

func TestImmediateExitRevokesTokenBeforeClosedIsDelivered(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	addPod(t, h, "p")
	base := h.opts.Factory
	h.opts.Factory = func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		p, err := base(s, o)
		if err == nil {
			p.(*fakeProcess).closeEarly = true
		}
		return p, err
	}
	prepareHost(t, h)
	if _, ok := testclock.Receive(t, loop.events).(ClosedMsg); !ok {
		t.Fatal("missing immediate Closed notice")
	}
	if _, ok := h.Resolve(procs["p"].Token()); ok {
		t.Fatal("token was registered after an immediate exit revoked it")
	}
}
