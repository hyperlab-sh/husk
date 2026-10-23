//go:build linux || darwin

package server

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestBlockedLoopDiagnosticsNeverStallOneThousandRejectedRequests(t *testing.T) {
	for _, mode := range []string{"busy", "denied", "unknown", "malformed", "ignored"} {
		t.Run(mode, func(t *testing.T) {
			s := inertServer(t)
			clock := s.cfg.Clock.(*testclock.Clock)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			entered := make(chan Notice, 4)
			var posts atomic.Int32
			s.cfg.Post = func(v any) bool {
				posts.Add(1)
				entered <- v.(Notice)
				select {
				case <-release:
					return true
				case <-t.Context().Done():
					return false
				}
			}
			started := make(chan struct{}, MaxInFlight)
			s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, _ protocol.Message, _ Notifier) protocol.Message {
				started <- struct{}{}
				<-ctx.Done()
				return protocol.Message{}
			}))
			start(t, s)
			c, r := dial(t, s)
			if mode == "busy" {
				for i := 0; i < MaxInFlight; i++ {
					write(t, c, fmt.Sprintf(`{"id":%d,"method":"sample.block"}`, 10000+i))
				}
				for i := 0; i < MaxInFlight; i++ {
					testclock.Receive(t, started)
				}
			}
			write(t, c, `{"id":0,"method":"unknown.seed"}`)
			if m := read(t, r); m.Error == nil {
				t.Fatal(m)
			}
			clock.Await(t, DiagnosticInterval)
			clock.Advance(DiagnosticInterval)
			if n := testclock.Receive(t, entered); n.Count != 1 || n.LastCode != protocol.CodeUnknownMethod {
				t.Fatal(n)
			}
			// Post is now blocked. No reader path may call it or wait for it.
			want := protocol.CodeUnknownMethod
			for i := 1; i <= 1000; i++ {
				var raw string
				switch mode {
				case "busy":
					raw = fmt.Sprintf(`{"id":%d,"method":"sample.block"}`, i)
					want = protocol.CodeBusy
				case "denied":
					raw = fmt.Sprintf(`{"id":%d,"method":"sample.block","token":"unknown"}`, i)
					want = protocol.CodeDenied
				case "malformed":
					raw = fmt.Sprintf(`{"id":%d,"method":"session.ping","params":[]}`, i)
					want = protocol.CodeInvalidParams
				case "ignored":
					switch i % 3 {
					case 0:
						raw = `{"id":7,"result":{}}`
					case 1:
						raw = `{"method":"protocol.error","params":{}}`
					default:
						raw = `{"method":"unknown.notice"}`
					}
				default:
					raw = fmt.Sprintf(`{"id":%d,"method":"unknown.method"}`, i)
				}
				write(t, c, raw)
			}
			write(t, c, `{"id":2001,"method":"session.ping"}`)
			if mode != "ignored" {
				for i := 1; i <= 1000; i++ {
					m := read(t, r)
					if m.ID.String() != fmt.Sprint(i) || m.Error == nil || m.Error.Code != want {
						t.Fatal(i, m)
					}
				}
			}
			if m := read(t, r); m.ID.String() != "2001" {
				t.Fatal("reader stalled or ignored traffic was answered", m)
			}
			clock.Await(t, DiagnosticInterval)
			clock.Advance(DiagnosticInterval)
			if posts.Load() != 1 {
				t.Fatal("blocked loop accumulated diagnostic workers", posts.Load())
			}
			closed := make(chan error, 1)
			go func() { closed <- s.Close() }()
			if mode == "busy" {
				// The blocked handlers hold Close until the drain bound.
				clock.Await(t, WriteTimeout)
				clock.Advance(WriteTimeout)
			}
			if err := testclock.Receive(t, closed); err != nil {
				t.Fatal(err)
			}
			if posts.Load() != 1 {
				t.Fatal("close bypassed diagnostic coalescing")
			}
		})
	}
}

func TestLoopDiagnosticsCoalesceCountsAndRespectOneSecondInterval(t *testing.T) {
	s := inertServer(t)
	clock := s.cfg.Clock.(*testclock.Clock)
	notices := make(chan Notice, 4)
	s.cfg.Post = func(v any) bool { notices <- v.(Notice); return true }
	start(t, s)
	c, r := dial(t, s)
	for i := 0; i < 1000; i++ {
		write(t, c, fmt.Sprintf(`{"id":%d,"method":"unknown.method"}`, i))
	}
	for i := 0; i < 1000; i++ {
		if m := read(t, r); m.Error == nil {
			t.Fatal(m)
		}
	}
	select {
	case n := <-notices:
		t.Fatal("per-error or early loop notice", n)
	default:
	}
	clock.Await(t, DiagnosticInterval)
	clock.Advance(DiagnosticInterval)
	first := testclock.Receive(t, notices)
	if first.Count != 1000 || first.LastCode != protocol.CodeUnknownMethod {
		t.Fatal(first)
	}
	// A context deadline guards synchronization only, never rate behavior.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for s.diagnosticPosting.Load() {
		select {
		case <-ctx.Done():
			t.Fatal("diagnostic callback did not return")
		default:
			runtime.Gosched()
		}
	}
	write(t, c, `{"id":1001,"method":"session.ping","token":"unknown"}`)
	if m := read(t, r); m.Error == nil {
		t.Fatal(m)
	}
	clock.Await(t, DiagnosticInterval)
	clock.Advance(DiagnosticInterval - 1)
	select {
	case n := <-notices:
		t.Fatal("more than one notice per second", n)
	default:
	}
	clock.Advance(1)
	if next := testclock.Receive(t, notices); next.Count != 1 || next.LastCode != protocol.CodeDenied {
		t.Fatal(next)
	}
}
