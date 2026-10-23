//go:build linux || darwin

package wrapper

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

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

const stdinTextQuestion = "QUESTION host-test-gaps: pods.md row 500 says \"stopped reading: send queue full (1024 messages)\" / \"write timed out after 5s\", the code prefixes \"protocol: \"; pin after the ruling"

// The exact ClosedMsg reason, LastError and status of the exits no other test
// pins by equality (clean, crash, signal and EOF live in process_test.go): host
// stop, and the two stdin failures, which wait on the stdinTextQuestion ruling.
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
		skip       string
	}{
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
			reason: "stopped reading: protocol: send queue full (1 messages)", lastError: "stopped reading: protocol: send queue full (1 messages)", wantStatus: Failed, skip: stdinTextQuestion,
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
			reason: "stopped reading: protocol: write timed out after 5s", lastError: "stopped reading: protocol: write timed out after 5s", wantStatus: Failed, skip: stdinTextQuestion,
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
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
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

// A read error on stdout is fatal for the launch: the reason is
// "stopped reading: " plus the reader's error wrapped by readStdout.
func TestStdoutReadErrorReasonIsExact(t *testing.T) {
	f := newFixture(t, "ignore", nil) // never started: only readStdout runs
	go f.w.readStdout(iotest.ErrReader(errors.New("connection reset by peer")))
	closed := awaitNotice[ClosedMsg](t, f)
	if got, want := closed.Reason.Error(), "stopped reading: read stdout: connection reset by peer"; got != want {
		t.Fatalf("Closed reason = %q, want %q", got, want)
	}
}
