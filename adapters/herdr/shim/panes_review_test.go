package shim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func assertNoPaneClose(t *testing.T, h *testHost) {
	t.Helper()
	for {
		select {
		case event := <-h.events:
			if event.method == protocol.MethodSurfaceClose {
				t.Fatal("unexpected surface.close")
			}
		default:
			return
		}
	}
}

func TestPaneReviewInputQueueDoesNotKillBlockedProgram(t *testing.T) {
	r, spawns := paneRig(t)
	spawn := r.o.SpawnTerminal
	release := make(chan struct{})
	entered := make(chan struct{})
	writes := make(chan []byte, 256)
	var first sync.Once
	r.o.SpawnTerminal = func(c Command, w, h int) (TerminalProcess, error) {
		process, err := spawn(c, w, h)
		if err != nil {
			return nil, err
		}
		p := process.(*paneProcess)
		p.write = func(b []byte) (int, error) {
			first.Do(func() { close(entered) })
			select {
			case <-release:
			case <-p.closed:
				return 0, io.ErrClosedPipe
			}
			writes <- append([]byte(nil), b...)
			return len(b), nil
		}
		return p, nil
	}
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	spawned := take(t, spawns)
	var attached protocol.TerminalAttachParams
	_ = json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attached)
	send := func(s string) {
		paneNotice(t, r, protocol.MethodTerminalInput, protocol.TerminalInputParams{Surface: attached.Surface, Data: base64.StdEncoding.EncodeToString([]byte(s))})
	}
	expected := "000|"
	send(expected)
	take(t, entered)
	for i := 1; i < 200; i++ {
		s := fmt.Sprintf("%03d|", i)
		expected += s
		send(s)
	}
	// A subsequent request is a reader barrier, even while the PTY writer blocks.
	if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	assertNoPaneClose(t, r.host)
	select {
	case signal := <-spawned.process.signals:
		t.Fatalf("input killed the program: %v", signal)
	default:
	}
	close(release)
	actual := ""
	for len(actual) < len(expected) {
		actual += string(take(t, writes))
	}
	if actual != expected {
		t.Fatal("queued input was dropped or reordered")
	}
}

func TestPaneReviewInputByteCapAndWarningRate(t *testing.T) {
	q := newPaneInput()
	now := time.Unix(10, 0)
	if q.append([]byte(strings.Repeat("a", maxPaneInputBytes)), now) {
		t.Fatal("cap-sized input refused")
	}
	if !q.append([]byte("new"), now) || q.append([]byte("new"), now.Add(time.Millisecond)) || !q.append([]byte("new"), now.Add(time.Second)) {
		t.Fatal("overflow warnings are not limited to once a second")
	}
	total := 0
	for total < maxPaneInputBytes {
		b, ok := q.next(context.Background())
		if !ok || strings.Trim(string(b), "a") != "" {
			t.Fatal("overflow displaced older bytes")
		}
		total += len(b)
	}
	q.mu.Lock()
	remaining := len(q.data)
	q.mu.Unlock()
	if remaining != 0 {
		t.Fatal("newest overflow bytes were retained")
	}
}

func TestPaneReviewStateCheckRetryAndHostCloseOrigin(t *testing.T) {
	for _, mode := range []string{"present", "gone", "failed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			clk := &testClock{timers: make(chan *testTimer, 64)}
			h := &testHost{events: make(chan hostRecord, 64)}
			logs := &lockedBuffer{}
			calls := 0
			h.call = func(ctx context.Context, m string, _ any) (json.RawMessage, error) {
				if m != protocol.MethodStateGet {
					return json.RawMessage(`{}`), nil
				}
				calls++
				if mode == "timeout" {
					clk.fire(t, 2*time.Second)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if calls == 1 || mode == "failed" {
					return nil, errors.New("temporary host error")
				}
				if mode == "present" {
					return json.RawMessage(`{"value":{"runtime":{"demo.pane":{}}}}`), nil
				}
				return json.RawMessage(`{"value":{"runtime":{}}}`), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rd, wr := io.Pipe()
			process := &paneProcess{testProcess: &testProcess{pid: 1, done: make(chan int, 1), signals: make(chan syscall.Signal, 8), termStops: true}, reader: rd, output: wr, closed: make(chan struct{})}
			defer process.Close()
			p := &pane{id: "demo.pane", process: process, ctx: ctx, cancel: cancel}
			r := &runner{ctx: ctx, log: logs, o: Options{Host: h, Clock: clk}, panes: map[protocol.SurfaceID]*pane{p.id: p}, processes: map[Process]bool{process: true}, state: state{Surfaces: map[string]any{string(p.id): map[string]any{}}}}
			r.checkPaneLifetime(p)
			wantCalls := 2
			if mode == "timeout" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("state check attempts=%d want %d", calls, wantCalls)
			}
			if mode == "gone" {
				if len(r.panes) != 0 || !p.closed {
					t.Fatal("confirmed closed lifetime leaked")
				}
				if take(t, process.signals) != syscall.SIGHUP || take(t, process.signals) != syscall.SIGTERM {
					t.Fatal("host close did not stop PTY")
				}
			} else {
				if len(r.panes) != 1 || p.closed {
					t.Fatal("unconfirmed closure killed the pane")
				}
				select {
				case <-process.signals:
					t.Fatal("live or unknown lifetime signalled")
				default:
				}
			}
			if (mode == "failed" || mode == "timeout") && !strings.Contains(logs.String(), "runtime_state_check_failed") {
				t.Fatal("failed retry was silent")
			}
			assertNoPaneClose(t, h)
		})
	}
}

func TestPaneReviewEOFStopsPTYAndPendingAttach(t *testing.T) {
	for _, mode := range []string{"running", "attach_wait", "late_open"} {
		t.Run(mode, func(t *testing.T) {
			r, spawns := paneRig(t)
			original := r.host.call
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
				if mode == "attach_wait" && m == protocol.MethodTerminalAttach {
					once.Do(func() { close(entered) })
					return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "waiting for geometry"}
				}
				if mode == "late_open" && m == protocol.MethodSurfaceOpen {
					once.Do(func() { close(entered) })
					<-ctx.Done()
					<-release
				}
				return original(ctx, m, p)
			}
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "")
			var process *paneProcess
			if mode == "running" {
				if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
					t.Fatal(reply.Error)
				}
				process = take(t, spawns).process
				defer process.Signal(syscall.SIGKILL)
				process.termStops = false
			} else {
				id := protocol.NewStringID("pending")
				raw := json.RawMessage(`{"action":"demo.pane-panel"}`)
				if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: raw}); err != nil {
					t.Fatal(err)
				}
				take(t, entered)
				// Drain the cancelled action reply so the output pipe cannot hold Run open.
				go func() { var reply protocol.Message; _ = r.reply.Decode(&reply) }()
			}
			_ = r.in.Close()
			if mode == "late_open" {
				close(release)
			}
			r.host.until(t, protocol.MethodSurfaceClose, "")
			if process != nil {
				if take(t, process.signals) != syscall.SIGHUP || take(t, process.signals) != syscall.SIGTERM {
					t.Fatal("EOF did not stop pane group")
				}
				select {
				case signal := <-process.signals:
					t.Fatalf("signal before the shutdown grace: %v", signal)
				default:
				}
				r.clock.fire(t, 2*time.Second)
				if take(t, process.signals) != syscall.SIGKILL {
					t.Fatal("EOF did not escalate after grace")
				}
			}
			err := take(t, r.exit)
			r.exit <- err
			if err != nil {
				t.Fatal(err)
			}
			if mode != "running" {
				select {
				case <-spawns:
					t.Fatal("pending open spawned a PTY after EOF")
				default:
				}
			}
		})
	}
}

func TestPaneReviewProxyBusyReasons(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{{"modal_open", "ui_busy"}, {"too_many_surfaces", "limit_reached"}, {"too_many_commands", "limit_reached"}, {"", "busy"}} {
		data, _ := json.Marshal(map[string]string{"reason": tc.reason})
		failure := paneProxyError(&protocol.Error{Code: protocol.CodeBusy, Message: "capacity reached", Data: data})
		if failure.Code != tc.want {
			t.Fatalf("%s mapped to %s want %s", tc.reason, failure.Code, tc.want)
		}
	}
}
