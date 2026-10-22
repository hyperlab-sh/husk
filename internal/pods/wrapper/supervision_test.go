//go:build linux || darwin

package wrapper

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func groupFixture(t *testing.T) (*Wrapper, *testclock.Clock, chan tea.Msg) {
	t.Helper()
	clock := testclock.New()
	events := make(chan tea.Msg, 64)
	sink := fixtureSink{events: events, ctx: t.Context()}
	w, err := New(manifest.Spec{ID: "fake", Protocol: protocol.Major, Command: []string{"unused"}}, Options{Clock: clock, Post: sink.post, Replies: sink, LogPath: func(string) (string, error) { return "unused", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.cancel)
	w.info.PID = 99 // no OS process: all group operations are injected below
	w.groupExists = func(int) bool { return true }
	w.signal = func(int, bool) error { return nil }
	return w, clock, events
}

func TestGroupPollingCompletesBeforeStopDeadlineAndCancelsEscalation(t *testing.T) {
	w, clock, events := groupFixture(t)
	var alive atomic.Bool
	alive.Store(true)
	w.groupExists = func(int) bool { return alive.Load() }
	term := make(chan struct{}, 1)
	var kills atomic.Int32
	w.signal = func(_ int, kill bool) error {
		if kill {
			kills.Add(1)
		} else {
			term <- struct{}{}
		}
		return nil
	}
	finished := make(chan struct{})
	go func() { w.finishGroup(); close(finished) }()
	testclock.Receive(t, term)
	clock.Await(t, groupPollInterval)
	alive.Store(false)
	clock.Advance(groupPollInterval)
	testclock.Receive(t, finished)
	if clock.Active(StopGrace) || kills.Load() != 0 {
		t.Fatal("prompt group exit retained escalation")
	}
	// Even an already-dispatched stale timer must recheck the terminal fence.
	clock.Advance(StopGrace)
	if kills.Load() != 0 || len(events) != 0 {
		t.Fatal("false survivor kill or warning")
	}
}

func TestObservedEmptyGroupIsNeverSignalledDuringReaderDrain(t *testing.T) {
	w, _, _ := groupFixture(t)
	var alive atomic.Bool
	w.groupExists = func(int) bool { return alive.Load() }
	var signals atomic.Int32
	w.signal = func(int, bool) error { signals.Add(1); return nil }
	finished := make(chan struct{})
	go func() { w.finishGroup(); close(finished) }()
	testclock.Receive(t, finished) // reader/final-notice completion has not happened
	alive.Store(true)              // model a later reuse of this process-group id
	if !errors.Is(w.Terminate(), os.ErrProcessDone) || !errors.Is(w.Kill(), os.ErrProcessDone) {
		t.Fatal("completed group was reopened")
	}
	if signals.Load() != 0 || !w.groupEnded || w.finished {
		t.Fatal("reader completion was used as the signal fence")
	}
}

func TestKillErrorTerminatesReapedGroupSupervision(t *testing.T) {
	f := newFixture(t, "clean", nil)
	f.w.groupExists = func(int) bool { return true }
	f.w.signal = func(_ int, kill bool) error {
		if kill {
			return syscall.EPERM
		}
		return nil
	}
	f.start(t)
	f.clock.Await(t, StopGrace)
	f.clock.Advance(StopGrace)
	exit := awaitNotice[ExitMsg](t, f)
	testclock.Receive(t, f.w.Done())
	if exit.Info.Status != Failed || !errors.Is(exit.Err, syscall.EPERM) {
		t.Fatal("kill failure stranded or hid group failure", exit)
	}
	found := false
	for _, notice := range f.notices {
		if log, ok := notice.(LogMsg); ok && log.Level == "error" && strings.Contains(log.Message, "kill process group") {
			found = true
		}
	}
	for !found {
		log := awaitNotice[LogMsg](t, f)
		found = log.Level == "error" && strings.Contains(log.Message, "kill process group")
	}
}

func TestKillKeepsBufferedRepliesUntilStdoutEOF(t *testing.T) {
	w, clock, events := groupFixture(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close(); write.Close() })
	w.stdout = read
	close(w.waited) // model an already-reaped leader with final buffered output
	release := make(chan struct{})
	go func() {
		testclock.Receive(t, release)
		w.readStdout(read)
	}()
	w.signal = func(_ int, kill bool) error {
		if !kill {
			return nil
		}
		_, err := write.Write([]byte("{\"id\":81,\"result\":{\"final\":true}}\n"))
		if err == nil {
			err = write.Close()
		}
		return err
	}
	if err := w.Kill(); err != nil {
		t.Fatal(err)
	}
	if !clock.Active(KillWait) {
		t.Fatal("kill did not establish a drain deadline")
	}
	close(release)
	testclock.Receive(t, w.readDone)
	found := false
	for len(events) > 0 {
		if notice, ok := testclock.Receive(t, events).(replyNotice); ok && notice.reply.ID.String() == "81" {
			found = true
		}
	}
	if !found {
		t.Fatal("SIGKILL discarded buffered final reply")
	}
}

func TestKillWaitClosesSurvivingPipeOnlyAtInjectedDeadline(t *testing.T) {
	w, clock, _ := groupFixture(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close(); write.Close() })
	w.stdout = read
	close(w.waited)
	go w.readStdout(read)
	if err := w.Kill(); err != nil {
		t.Fatal(err)
	}
	w.MarkShutdown() // must not cancel an already-running drain deadline
	clock.Await(t, KillWait)
	clock.Advance(KillWait - 1)
	select {
	case <-w.readDone:
		t.Fatal("stdout closed before kill wait")
	default:
	}
	clock.Advance(1)
	testclock.Receive(t, w.readDone)
}

func TestOpeningSendFailureReturnsBeforeUnbufferedPostDrains(t *testing.T) {
	events := make(chan tea.Msg)
	sink := fixtureSink{events: events, ctx: t.Context()}
	f := newFixture(t, "echo", func(o *Options) { o.Post = sink.post; o.Replies = sink })
	f.events = events
	opening := []protocol.Message{{Method: "sample.open", Params: json.RawMessage(`{"padding":"` + strings.Repeat("x", protocol.MaxLineBytes) + `"}`)}}
	returned := make(chan error, 1)
	go func() { returned <- f.w.Start(opening) }()
	if err := testclock.Receive(t, returned); err != nil {
		t.Fatal("process should start before the opening send fails", err)
	}
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Failed || !strings.HasPrefix(exit.Info.LastError, "stopped reading: ") {
		t.Fatal(exit)
	}
	if closedCount(f) != 1 {
		t.Fatal("opening failure did not close exactly once")
	}
}
