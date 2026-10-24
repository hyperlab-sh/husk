package shim

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func emitFocused(t *testing.T, r *rig, id string) {
	t.Helper()
	if err := r.server.Emit("pane_focused", map[string]any{"type": "pane_focused", "pane_id": id, "workspace_id": "w"}); err != nil {
		t.Fatal(err)
	}
}

func assertUnsignalled(t *testing.T, processes []spawnRecord) {
	t.Helper()
	for _, p := range processes {
		select {
		case signal := <-p.process.signals:
			t.Fatalf("reconnect signalled pid %d: %v", p.process.PID(), signal)
		default:
		}
	}
}

func eventPaneID(t *testing.T, p spawnRecord) string {
	t.Helper()
	var event struct {
		Data struct {
			Pane string `json:"pane_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(envMap(p.command.Env)["HERDR_PLUGIN_EVENT_JSON"]), &event); err != nil {
		t.Fatal(err)
	}
	return event.Data.Pane
}

func TestHardeningReconnectUnderLoad(t *testing.T) {
	var gated atomic.Bool
	gate := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(gate) })
	pending := make(chan struct{}, 64)
	r := newRig(t, "always", false, startupAndEvent, func(o *fakeherdr.Options) {
		o.Reply = func(req fakeherdr.Request, _ map[string]any) *fakeherdr.ReplyError {
			if req.Method == "pane.current" && gated.Load() {
				pending <- struct{}{}
				<-gate
			}
			return nil
		}
	})
	r.start(t)
	t.Cleanup(unblock)
	r.host.until(t, protocol.MethodStateSet, "attached")
	startup := take(t, r.spawns)
	if startup.command.Argv[0] != "./startup" {
		t.Fatal("startup missing")
	}
	active := []spawnRecord{startup}
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	active = append(active, take(t, r.spawns))
	gated.Store(true)
	const burst = 8
	// Do not await dispatch: the first hook blocks in pane.current and the rest
	// remain queued on the stream while a separate action also builds context.
	for i := 0; i < burst; i++ {
		emitFocused(t, r, fmt.Sprintf("before-%d", i))
	}
	take(t, pending)
	action := make(chan protocol.Message, 1)
	go func() { action <- r.invoke(t, "demo.go") }()
	take(t, pending)
	if len(r.spawns) != 0 {
		t.Fatal("gated context unexpectedly spawned")
	}
	r.server.DropSubscriptions()
	assertUnsignalled(t, active)
	unblock()
	reply := take(t, action)
	if reply.Error != nil && (reply.Error.Code != protocol.CodeUnavailable || !strings.Contains(string(reply.Error.Data), "herdr_absent")) {
		t.Fatalf("gated action: %+v", reply)
	}
	r.host.until(t, protocol.MethodStateSet, "lost") // EOF fences buffered dispatch.
	seen := map[string]bool{}
	events, actions := 0, 0
	for len(r.spawns) > 0 {
		p := take(t, r.spawns)
		active = append(active, p)
		switch p.command.Argv[0] {
		case "./event":
			id := eventPaneID(t, p)
			if !strings.HasPrefix(id, "before-") || seen[id] {
				t.Fatalf("duplicate or foreign event %q", id)
			}
			seen[id] = true
			events++
		case "./command":
			actions++
		default:
			t.Fatalf("unexpected spawn during EOF: %v", p.command.Argv)
		}
	}
	if events < 1 || events > burst {
		t.Fatalf("event spawns=%d burst=%d", events, burst)
	}
	if (reply.Error == nil && actions != 1) || (reply.Error != nil && actions != 0) {
		t.Fatalf("action spawns=%d reply=%+v", actions, reply)
	}
	assertUnsignalled(t, active)
	r.clock.fire(t, 5*time.Second)
	r.host.until(t, protocol.MethodStateSet, "attached")
	emitFocused(t, r, "after")
	p := take(t, r.spawns)
	if p.command.Argv[0] != "./event" || eventPaneID(t, p) != "after" {
		t.Fatalf("startup or pre-drop event replayed: %v", p.command.Argv)
	}
	active = append(active, p)
	r.server.DropSubscriptions()
	r.host.until(t, protocol.MethodStateSet, "lost")
	if len(r.spawns) != 0 {
		t.Fatal("extra spawn after reconnect fence")
	}
	assertUnsignalled(t, active)
}

func TestHardeningTwoInstancesShareServer(t *testing.T) {
	for _, policy := range []string{"auto", "always"} {
		t.Run(policy, func(t *testing.T) {
			first := newRig(t, policy, true, startupAndEvent)
			second := newRig(t, policy, true, startupAndEvent)
			// Hosts, proxies and clocks are independent; only upstream is shared.
			second.o.Socket = first.o.Socket
			second.o.Client = upstream.New(upstream.Options{Socket: first.o.Socket, Clock: second.clock, Dial: guardedDial(t, first.o.Socket)})
			first.start(t)
			second.start(t)
			for _, r := range []*rig{first, second} {
				raw := r.host.until(t, protocol.MethodStateSet, "attached")
				var s struct {
					Value state `json:"value"`
				}
				if err := json.Unmarshal(raw, &s); err != nil {
					t.Fatal(err)
				}
				want := "server"
				if policy == "always" {
					want = "shim"
				}
				if s.Value.Hooks != want {
					t.Fatalf("hooks=%q want %q", s.Value.Hooks, want)
				}
			}
			if policy == "always" {
				// Attached is published only after both subscriptions are acknowledged.
				emitFocused(t, first, "shared")
				for _, r := range []*rig{first, second} {
					for _, want := range []string{"./startup", "./event"} {
						p := take(t, r.spawns)
						if p.command.Argv[0] != want {
							t.Fatalf("command=%v want %s", p.command.Argv, want)
						}
					}
				}
				first.server.DropSubscriptions()
				for _, r := range []*rig{first, second} {
					r.host.until(t, protocol.MethodStateSet, "lost")
				}
			} else {
				// Recorder assertions are deterministic even if a wrongly-created stream
				// has not dispatched anything yet. No Emit to an empty subscriber set.
				for _, req := range first.server.Requests() {
					if req.Method == "events.subscribe" {
						t.Fatal("auto subscribed to server-owned hooks")
					}
				}
				for _, r := range []*rig{first, second} {
					// Scheduling the no-stream probe fences the entire startup
					// section, not merely the earlier attached state publication.
					r.clock.next(t, 5*time.Second)
					if len(r.spawns) != 0 {
						t.Fatal("auto ran startup or event hook before action")
					}
					if reply := r.invoke(t, "demo.go"); reply.Error != nil {
						t.Fatal(reply.Error)
					}
					p := take(t, r.spawns)
					if p.command.Argv[0] != "./command" {
						t.Fatal("auto ran startup or event hook")
					}
				}
			}
			for _, r := range []*rig{first, second} {
				if len(r.spawns) != 0 {
					t.Fatal("extra hook command")
				}
			}
		})
	}
}

func TestHardeningEventCapAndWarningCadence(t *testing.T) {
	for _, base := range []time.Time{{}, time.Unix(100, 0)} {
		t.Run(base.Format(time.RFC3339), func(t *testing.T) {
			var elapsed atomic.Int64
			r := newRig(t, "always", false, "[[events]]\non='pane.focused'\ncommand=['./event']\n")
			r.o.Now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			for i, step := range []struct {
				at   time.Duration
				logs int
			}{{0, 1}, {999 * time.Millisecond, 1}, {time.Second, 2}, {1500 * time.Millisecond, 2}} {
				elapsed.Store(int64(step.at))
				for n := 0; n < 40; n++ {
					emitFocused(t, r, fmt.Sprintf("%d-%d", i, n))
				}
				r.server.DropSubscriptions()
				r.host.until(t, protocol.MethodStateSet, "lost")
				if len(r.spawns) != 32 {
					t.Fatalf("running=%d want exactly 32", len(r.spawns))
				}
				limits := 0
				for _, line := range strings.Split(strings.TrimSpace(r.logs.String()), "\n") {
					var record map[string]any
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatal(err)
					}
					if record["code"] == "command_limit" {
						limits++
					} else if strings.Contains(line, "maximum concurrent commands reached") {
						t.Fatalf("unthrottled limit log: %s", line)
					}
				}
				if limits != step.logs {
					t.Fatalf("at %s drop logs=%d want %d", step.at, limits, step.logs)
				}
				if i < 3 {
					r.clock.fire(t, 5*time.Second)
					r.host.until(t, protocol.MethodStateSet, "attached")
				}
			}
		})
	}
}

func TestHardeningQueueCapExcludesDequeuedBytes(t *testing.T) {
	q := newPaneInput()
	now := time.Time{}
	first := bytes.Repeat([]byte("a"), protocol.MaxTerminalBytes)
	if q.append(first, now) {
		t.Fatal("first chunk refused")
	}
	dequeued, ok := q.next(t.Context())
	if !ok || !bytes.Equal(dequeued, first) {
		t.Fatal("dequeued chunk changed")
	}
	queued := bytes.Repeat([]byte("b"), maxPaneInputBytes)
	if q.append(queued, now) {
		t.Fatal("dequeued bytes counted against queue cap")
	}
	if !q.append([]byte("newest"), now) || q.append([]byte("newest"), now) || !q.append([]byte("newest"), now.Add(time.Second)) {
		t.Fatal("overflow warning cadence changed")
	}
	for remaining := maxPaneInputBytes; remaining > 0; {
		chunk, ok := q.next(t.Context())
		if !ok || len(chunk) == 0 || len(chunk) > protocol.MaxTerminalBytes || !bytes.Equal(chunk, bytes.Repeat([]byte("b"), len(chunk))) {
			t.Fatal("queued input reordered, oversized or displaced by overflow")
		}
		remaining -= len(chunk)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.data) != 0 {
		t.Fatal("newest overflow chunk retained")
	}
}

func TestHardeningBlockedInputHasOneChunkInFlight(t *testing.T) {
	r, spawns := paneRig(t)
	r.logs.notices = make(chan struct{}, 128)
	spawn := r.o.SpawnTerminal
	release := make(chan struct{})
	entered := make(chan struct{}, 128)
	writes := make(chan []byte, 128)
	var calls atomic.Int32
	r.o.SpawnTerminal = func(c Command, w, h int) (TerminalProcess, error) {
		process, err := spawn(c, w, h)
		if err != nil {
			return nil, err
		}
		p := process.(*paneProcess)
		p.write = func(b []byte) (int, error) {
			calls.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
			case <-p.closed:
				return 0, context.Canceled
			}
			writes <- append([]byte(nil), b...)
			return len(b), nil
		}
		return p, nil
	}
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	process := take(t, spawns).process
	var attach protocol.TerminalAttachParams
	if err := json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attach); err != nil {
		t.Fatal(err)
	}
	send := func(b []byte) {
		paneNotice(t, r, protocol.MethodTerminalInput, protocol.TerminalInputParams{Surface: attach.Surface, Data: base64.StdEncoding.EncodeToString(b)})
	}
	first := bytes.Repeat([]byte{0}, protocol.MaxTerminalBytes)
	send(first)
	take(t, entered)
	expected := append([]byte(nil), first...)
	for i := 1; i <= maxPaneInputBytes/protocol.MaxTerminalBytes; i++ {
		b := bytes.Repeat([]byte{byte(i)}, protocol.MaxTerminalBytes)
		send(b)
		expected = append(expected, b...)
	}
	send(bytes.Repeat([]byte{255}, protocol.MaxTerminalBytes)) // newest overflow
	// This request fences the stdin reader without releasing the PTY writer.
	if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("outstanding Write calls=%d want 1", got)
	}
	for !strings.Contains(r.logs.String(), `"code":"terminal_input_overflow"`) {
		take(t, r.logs.notices)
	}
	if got := strings.Count(r.logs.String(), `"code":"terminal_input_overflow"`); got != 1 {
		t.Fatalf("overflow warnings=%d want 1", got)
	}
	assertNoPaneClose(t, r.host)
	select {
	case s := <-process.signals:
		t.Fatalf("input overflow signalled %v", s)
	default:
	}
	close(release)
	actual := make([]byte, 0, len(expected))
	for len(actual) < len(expected) {
		b := take(t, writes)
		if len(b) > protocol.MaxTerminalBytes {
			t.Fatalf("in-flight chunk=%d", len(b))
		}
		actual = append(actual, b...)
	}
	// The bytes after the blocked first write are exactly the full 4 MiB queue;
	// none of the overflow chunk may be delivered or displace older input.
	if !bytes.Equal(actual, expected) {
		t.Fatal("blocked queue lost, reordered or retained overflow bytes")
	}
	assertNoPaneClose(t, r.host)
	select {
	case s := <-process.signals:
		t.Fatalf("drain signalled %v", s)
	default:
	}
	_ = r.in.Close()
	err := take(t, r.exit)
	r.exit <- err
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 0 || calls.Load() != 65 {
		t.Fatalf("overflow delivered: writes=%d calls=%d", len(writes), calls.Load())
	}
}

type countedHost struct {
	*testHost
	closes atomic.Int32
}

func (h *countedHost) Notify(ctx context.Context, method string, params any) error {
	if method == protocol.MethodSurfaceClose {
		h.closes.Add(1)
	}
	return h.testHost.Notify(ctx, method, params)
}

type countedTerminal struct {
	TerminalProcess
	closes  atomic.Int32
	signals chan syscall.Signal
}

func (p *countedTerminal) Close() error { p.closes.Add(1); return p.TerminalProcess.Close() }
func (p *countedTerminal) Signal(s syscall.Signal) error {
	err := p.TerminalProcess.Signal(s)
	p.signals <- s
	return err
}
