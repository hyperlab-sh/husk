//go:build linux || darwin

package wrapper

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// closedReason returns the reason of the one ClosedMsg among the notices the
// fixture has seen.
func closedReason(t *testing.T, f *fixture) string {
	t.Helper()
	var reasons []string
	for _, n := range f.notices {
		if c, ok := n.(ClosedMsg); ok {
			reasons = append(reasons, c.Reason.Error())
		}
	}
	if len(reasons) != 1 {
		t.Fatalf("Closed notices = %q, want exactly one", reasons)
	}
	return reasons[0]
}

// The exact texts a Pod's exit surfaces under (pods.md, wrapper invariants 4
// and 6 and the liveness table): the ClosedMsg reason Calls cancels pending
// requests with, and the LastError that husk.pods and pods.plugin-exited carry.
func TestExitReasonAndLastErrorTextsAreExact(t *testing.T) {
	big := protocol.Message{Method: "sample.frame", Params: json.RawMessage(`{"padding":"` + strings.Repeat("x", 2<<20) + `"}`)}
	// stalled drives the stdin queue of a plugin that ignores SIGTERM until it
	// fails, then lets the stop grace lapse so the process is killed.
	stalled := func(t *testing.T, f *fixture) {
		awaitNotice[ClosedMsg](t, f)
		f.clock.Await(t, StopGrace)
		f.clock.Advance(StopGrace)
	}
	for _, tc := range []struct {
		name       string
		mode       string
		alter      func(*Options)
		drive      func(t *testing.T, f *fixture)
		reason     string
		lastError  string
		wantStatus Liveness
	}{
		{
			name: "clean exit", mode: "clean",
			reason: "exited", wantStatus: Exited,
		},
		{
			name: "non-zero exit", mode: "crash",
			reason: "crashed (exit status 7): exit status 7", lastError: "exit status 7", wantStatus: Failed,
		},
		{
			name: "foreign signal", mode: "selfkill",
			reason: "crashed (signal: killed): signal: killed", lastError: "signal: killed", wantStatus: Failed,
		},
		{
			name: "stdout EOF with the process alive", mode: "eof",
			drive: func(t *testing.T, f *fixture) {
				awaitReply(t, f, "9000")
				f.clock.Await(t, EOFReapGrace)
				f.clock.Advance(EOFReapGrace)
			},
			reason: "stopped reading: wrapper: stdout closed while process is running", lastError: "stopped reading: wrapper: stdout closed while process is running", wantStatus: Failed,
		},
		{
			name: "stdout read error", mode: "ignore",
			drive: func(t *testing.T, f *fixture) {
				awaitReply(t, f, "9000")
				f.w.fail(errors.New("read stdout: connection reset by peer"))
				stalled(t, f)
			},
			reason: "stopped reading: read stdout: connection reset by peer", lastError: "stopped reading: read stdout: connection reset by peer", wantStatus: Failed,
		},
		{
			name: "stdin queue overflow", mode: "ignore", alter: func(o *Options) { o.QueueCapacity = 1 },
			drive: func(t *testing.T, f *fixture) {
				awaitReply(t, f, "9000")
				var err error
				for i := 0; i < 5 && err == nil; i++ {
					err = f.w.Send(big)
				}
				if !errors.Is(err, protocol.ErrSendQueueFull) {
					t.Fatal("expected queue overflow", err)
				}
				stalled(t, f)
			},
			reason: "stopped reading: protocol: send queue full (1 messages)", lastError: "stopped reading: protocol: send queue full (1 messages)", wantStatus: Failed,
		},
		{
			name: "stdin write timeout", mode: "ignore", alter: func(o *Options) { o.QueueCapacity = 1 },
			drive: func(t *testing.T, f *fixture) {
				awaitReply(t, f, "9000")
				if err := f.w.Send(big); err != nil {
					t.Fatal(err)
				}
				f.clock.Await(t, WriteTimeout)
				f.clock.Advance(WriteTimeout)
				stalled(t, f)
			},
			reason: "stopped reading: protocol: write timed out after 5s", lastError: "stopped reading: protocol: write timed out after 5s", wantStatus: Failed,
		},
		{
			name: "host stop", mode: "ignore",
			drive: func(t *testing.T, f *fixture) {
				awaitReply(t, f, "9000")
				f.w.MarkShutdown()
				f.w.CloseStdin()
				if err := f.w.Terminate(); err != nil {
					t.Fatal(err)
				}
				if err := f.w.Kill(); err != nil {
					t.Fatal(err)
				}
			},
			reason: "exited", wantStatus: Exited,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.mode, tc.alter)
			if tc.alter != nil {
				if err := f.w.Start(nil); err != nil {
					t.Fatal(err)
				}
			} else {
				f.start(t)
			}
			if tc.drive != nil {
				tc.drive(t, f)
			}
			exit := awaitNotice[ExitMsg](t, f)
			if got := closedReason(t, f); got != tc.reason {
				t.Errorf("Closed reason = %q, want %q", got, tc.reason)
			}
			if exit.Info.LastError != tc.lastError {
				t.Errorf("LastError = %q, want %q", exit.Info.LastError, tc.lastError)
			}
			if exit.Info.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", exit.Info.Status, tc.wantStatus)
			}
		})
	}
}
