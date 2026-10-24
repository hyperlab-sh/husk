package guide

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type messageSink chan protocol.Message

func (s messageSink) Write(b []byte) (int, error) {
	_ = protocol.ReadLoop(strings.NewReader(string(b)), func(m protocol.Message) { s <- m }, nil)
	return len(b), nil
}

type recoverySocket struct {
	*scriptedSocket
	input      *io.PipeWriter
	fail       atomic.Bool
	failMethod string
}

func (s *recoverySocket) Write(b []byte) (int, error) {
	var method string
	_ = protocol.ReadLoop(strings.NewReader(string(b)), func(m protocol.Message) { method = m.Method }, nil)
	if s.fail.Load() && (s.failMethod == "" || s.failMethod == method) {
		return 0, io.ErrClosedPipe
	}
	return s.scriptedSocket.Write(b)
}

func (s *recoverySocket) send(t *testing.T, messages ...protocol.Message) {
	t.Helper()
	var batch strings.Builder
	for _, m := range messages {
		batch.Write(raw(m))
		batch.WriteByte('\n')
	}
	if _, err := io.WriteString(s.input, batch.String()); err != nil {
		t.Fatal(err)
	}
}

type runRig struct {
	input   *io.PipeWriter
	out     messageSink
	dials   chan io.ReadWriteCloser
	started chan struct{}
	waits   chan time.Duration
	advance chan struct{}
	done    chan error
	count   atomic.Int64
	stopped bool
}

func newRunRig(t *testing.T, stdout io.Writer) *runRig {
	t.Helper()
	r := &runRig{out: make(messageSink, 32), dials: make(chan io.ReadWriteCloser), started: make(chan struct{}, 32), waits: make(chan time.Duration), advance: make(chan struct{}), done: make(chan error, 1)}
	in, input := io.Pipe()
	r.input = input
	ctx, cancel := context.WithCancel(context.Background())
	if stdout == nil {
		stdout = r.out
	}
	cfg := config{token: "test-token", dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
		r.count.Add(1)
		r.started <- struct{}{}
		select {
		case c := <-r.dials:
			return c, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, retry: func(ctx context.Context, d time.Duration) error {
		select {
		case r.waits <- d:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-r.advance:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	go func() { r.done <- run(ctx, cfg, in, stdout, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		_ = input.Close()
		if !r.stopped {
			receive(t, r.done)
		}
	})
	return r
}

func (r *runRig) host(t *testing.T, messages ...protocol.Message) {
	t.Helper()
	(&recoverySocket{input: r.input}).send(t, messages...)
}

func (r *runRig) connect(t *testing.T, keys bool, failMethod string) *recoverySocket {
	t.Helper()
	receive(t, r.started)
	reader, input := io.Pipe()
	s := &recoverySocket{scriptedSocket: &scriptedSocket{reader, make(chan protocol.Message, 32)}, input: input, failMethod: failMethod}
	s.fail.Store(failMethod != "")
	select {
	case r.dials <- s:
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not accept connection")
	}
	watch := receive(t, s.writes)
	if watch.Method != protocol.MethodStateWatch {
		t.Fatalf("expected watch, got %+v", watch)
	}
	var keyRequest protocol.Message
	if keys {
		keyRequest = receive(t, s.writes)
		if keyRequest.Method != protocol.MethodKeysList {
			t.Fatal("missing initial keys request")
		}
	}
	l := fixture()
	s.send(t, protocol.Message{ID: watch.ID, Result: raw(map[string]any{"slices": []sliceValue{{"husk.geometry", raw(l.Geometry)}, {"husk.surfaces", raw(l.Surfaces)}}})})
	if keys {
		s.send(t, protocol.Message{ID: keyRequest.ID, Result: raw(fixtureKeys())})
	}
	return s
}

func TestRunHideRecoveryCompletesHostBatch(t *testing.T) {
	for _, replayFailure := range []string{"", protocol.MethodSurfaceUpdate, protocol.MethodStateSet} {
		t.Run("replay-failure-"+replayFailure, func(t *testing.T) {
			r := newRunRig(t, nil)
			s := r.connect(t, true, "")
			r.host(t, toggleRequest(1))
			keys := receive(t, s.writes)
			s.send(t, protocol.Message{ID: keys.ID, Result: raw(fixtureKeys())})
			receive(t, s.writes) // initial frame
			receive(t, s.writes) // shown state
			if reply := receive(t, r.out); string(reply.Result) != `{"shown":true}` {
				t.Fatalf("show reply: %+v", reply)
			}
			s.fail.Store(true)
			palette := testPalette()
			palette.Accent = "#667788"
			r.host(t, toggleRequest(2), protocol.Message{Method: protocol.MethodThemeChanged, Params: raw(protocol.ThemeChangedParams{Palette: palette})}, protocol.Message{ID: toggleRequest(3).ID, Method: "unknown.method"})
			if reply := receive(t, r.out); reply.ID.String() != "2" || string(reply.Result) != `{"shown":false}` {
				t.Fatalf("hide reply: %+v", reply)
			}
			if reply := receive(t, r.out); reply.ID.String() != "3" || reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
				t.Fatalf("trailing batch reply: %+v", reply)
			}
			if delay := receive(t, r.waits); delay != 100*time.Millisecond {
				t.Fatalf("retry delay: %s", delay)
			}
			r.host(t, toggleRequest(4))
			if reply := receive(t, r.out); reply.Error == nil || reply.Error.Code != protocol.CodeUnavailable {
				t.Fatalf("disconnected toggle reply: %+v", reply)
			}
			if r.count.Load() != 1 {
				t.Fatal("extra dial before retry advancement")
			}
			r.advance <- struct{}{}
			s = r.connect(t, false, replayFailure)
			if replayFailure != "" {
				if delay := receive(t, r.waits); delay != 100*time.Millisecond {
					t.Fatalf("replay failure retry: %s", delay)
				}
				r.advance <- struct{}{}
				s = r.connect(t, false, "")
			}
			clear := receive(t, s.writes)
			if clear.Method != protocol.MethodSurfaceUpdate || !strings.Contains(string(clear.Params), `"lines":[]`) {
				t.Fatalf("recovery did not clear overlay first: %+v", clear)
			}
			state := receive(t, s.writes)
			if state.Method != protocol.MethodStateSet || string(state.Params) != `{"slice":"guide.state","value":{"shown":false}}` {
				t.Fatalf("recovery did not publish hidden state: %+v", state)
			}
			// Force a hidden flush, then toggle: a retained hidePending would queue
			// another clear before keys.list, violating the next assertion.
			r.host(t, protocol.Message{ID: toggleRequest(6).ID, Method: "unknown.method"})
			receive(t, r.out)
			// A later toggle also proves the batch's theme mutation survived.
			r.host(t, toggleRequest(5))
			keys = receive(t, s.writes)
			if keys.Method != protocol.MethodKeysList {
				t.Fatalf("unexpected hidden replay: %+v", keys)
			}
			s.send(t, protocol.Message{ID: keys.ID, Result: raw(fixtureKeys())})
			frame := receive(t, s.writes)
			if frame.Method != protocol.MethodSurfaceUpdate || !strings.Contains(string(frame.Params), `38;2;102;119;136`) {
				t.Fatal("host batch lost theme mutation")
			}
			receive(t, s.writes)
			receive(t, r.out)
		})
	}
}

func TestRunDeferredToggleReplyAfterReconnect(t *testing.T) {
	r := newRunRig(t, nil)
	s := r.connect(t, true, "")
	s.fail.Store(true)
	r.host(t, toggleRequest(1)) // keys.list fails; toggle-on remains pending.
	if delay := receive(t, r.waits); delay != 100*time.Millisecond {
		t.Fatalf("pending toggle retry: %s", delay)
	}
	r.advance <- struct{}{}
	s = r.connect(t, true, "")
	if frame := receive(t, s.writes); frame.Method != protocol.MethodSurfaceUpdate {
		t.Fatalf("missing recovered frame: %+v", frame)
	}
	if state := receive(t, s.writes); state.Method != protocol.MethodStateSet {
		t.Fatalf("missing recovered state: %+v", state)
	}
	if reply := receive(t, r.out); reply.ID.String() != "1" || string(reply.Result) != `{"shown":true}` {
		t.Fatalf("pending toggle lost on reconnect: %+v", reply)
	}
}

func TestRunStdoutFailureIsFatal(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "dial-blocked", true: "socket-healthy"}[connected], func(t *testing.T) {
			r := newRunRig(t, &failedSend{})
			if connected {
				r.connect(t, true, "")
			} else {
				receive(t, r.started)
			}
			r.host(t, protocol.Message{ID: toggleRequest(1).ID, Method: "unknown.method"})
			err := receive(t, r.done)
			r.stopped = true
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("stdout failure not returned: %v", err)
			}
			if count := r.count.Load(); count != 1 {
				t.Fatalf("stdout failure launched %d dials", count)
			}
			select {
			case delay := <-r.waits:
				t.Fatalf("stdout failure retried socket after %s", delay)
			default:
			}
		})
	}
}
