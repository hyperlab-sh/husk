package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// guardedDial makes the selected fixture the only possible upstream, even if a
// test accidentally supplies inherited session settings or a different socket.
func guardedDial(t *testing.T, allowed string) upstream.Dialer {
	t.Helper()
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if allowed == "" || network != "unix" || address != allowed {
			t.Errorf("refusing non-fixture upstream dial: %s %q", network, address)
			return nil, fmt.Errorf("non-fixture upstream")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}

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

func TestHardeningReconnectUnderLoad(t *testing.T) {
	r := newRig(t, "always", false, startupAndEvent)
	r.start(t)
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
	seen := map[string]bool{}
	dispatch := func(id string) {
		emitFocused(t, r, id)
		p := take(t, r.spawns)
		if p.command.Argv[0] != "./event" {
			t.Fatalf("startup reran on same server: %v", p.command.Argv)
		}
		var event struct {
			Data struct {
				Pane string `json:"pane_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(envMap(p.command.Env)["HERDR_PLUGIN_EVENT_JSON"]), &event); err != nil {
			t.Fatal(err)
		}
		if event.Data.Pane != id || seen[event.Data.Pane] {
			t.Fatalf("duplicate or reordered event: %+v", event)
		}
		seen[event.Data.Pane] = true
		active = append(active, p)
	}
	for i := 0; i < 12; i++ {
		dispatch(fmt.Sprintf("before-%d", i))
	}
	// Commands and event hooks are all still running when the transport vanishes.
	r.server.DropSubscriptions()
	r.host.until(t, protocol.MethodStateSet, "lost")
	if reply := r.invoke(t, "demo.go"); reply.Error == nil || reply.Error.Code != protocol.CodeUnavailable || !strings.Contains(string(reply.Error.Data), "herdr_absent") {
		t.Fatalf("gap reply: %+v", reply)
	}
	assertUnsignalled(t, active)
	r.clock.fire(t, 5*time.Second)
	r.host.until(t, protocol.MethodStateSet, "attached")
	for i := 0; i < 12; i++ {
		dispatch(fmt.Sprintf("after-%d", i))
	}
	// EOF fences all buffered events, so the spawn count is an exact assertion.
	r.server.DropSubscriptions()
	r.host.until(t, protocol.MethodStateSet, "lost")
	assertUnsignalled(t, active)
	if len(seen) != 24 || len(r.spawns) != 0 {
		t.Fatalf("events=%d extra spawns=%d", len(seen), len(r.spawns))
	}
}

func TestHardeningTwoInstancesShareServer(t *testing.T) {
	for _, policy := range []string{"auto", "always"} {
		t.Run(policy, func(t *testing.T) {
			first := newRig(t, policy, true, startupAndEvent)
			second := newRig(t, policy, true, startupAndEvent)
			// Each shim keeps its own host, proxy, clock and token; only upstream is shared.
			second.o.Socket = first.o.Socket
			second.o.Client = upstream.New(upstream.Options{Socket: first.o.Socket, Clock: second.clock, Dial: guardedDial(t, first.o.Socket)})
			first.start(t)
			second.start(t)
			for _, r := range []*rig{first, second} {
				raw := r.host.until(t, protocol.MethodStateSet, "attached")
				var s struct {
					Value state `json:"value"`
				}
				_ = json.Unmarshal(raw, &s)
				want := "server"
				if policy == "always" {
					want = "shim"
				}
				if s.Value.Hooks != want {
					t.Fatalf("hooks=%q want %q", s.Value.Hooks, want)
				}
			}
			emitFocused(t, first, "shared")
			if policy == "always" {
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
				// A successful action is a post-attach barrier even without a subscription.
				for _, r := range []*rig{first, second} {
					if reply := r.invoke(t, "demo.go"); reply.Error != nil {
						t.Fatal(reply.Error)
					}
					p := take(t, r.spawns)
					if p.command.Argv[0] != "./command" {
						t.Fatal("auto duplicated server hooks")
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

func TestHardeningEventCapAtClockEpoch(t *testing.T) {
	var seconds atomic.Int64
	r := newRig(t, "always", false, "[[events]]\non='pane.focused'\ncommand=['./event']\n")
	// A clock's zero value is a valid instant, not an uninitialized sentinel.
	r.o.Now = func() time.Time { return time.Time{}.Add(time.Duration(seconds.Load()) * time.Second) }
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	for batch := 0; batch < 3; batch++ {
		for i := 0; i < 40; i++ {
			emitFocused(t, r, fmt.Sprintf("%d-%d", batch, i))
		}
		r.server.DropSubscriptions()
		r.host.until(t, protocol.MethodStateSet, "lost")
		if len(r.spawns) != 32 {
			t.Fatalf("running=%d want exactly 32", len(r.spawns))
		}
		want := 1
		if batch == 2 {
			want = 2
		}
		if got := strings.Count(r.logs.String(), `"code":"command_limit"`); got != want {
			t.Fatalf("drop logs=%d want %d", got, want)
		}
		if batch < 2 {
			if batch == 1 {
				seconds.Store(1)
			}
			r.clock.fire(t, 5*time.Second)
			r.host.until(t, protocol.MethodStateSet, "attached")
		}
	}
}

type countedTerminal struct {
	TerminalProcess
	closes atomic.Int32
}

func (p *countedTerminal) Close() error { p.closes.Add(1); return p.TerminalProcess.Close() }

func TestHardeningRealPTYTeardownRaces(t *testing.T) {
	for _, mode := range []string{"exit_close", "exit_open", "eof_pending_open"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := paneRig(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			opened := make(chan context.Context, 1)
			checking := make(chan struct{}, 1)
			release := make(chan struct{})
			original := r.host.call
			r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
				if m == protocol.MethodSurfaceOpen {
					opened <- ctx
					if mode == "eof_pending_open" {
						<-ctx.Done()
						<-release
					}
				}
				if m == protocol.MethodStateGet && mode == "exit_close" {
					checking <- struct{}{}
					<-release
				}
				return original(ctx, m, p)
			}
			spawned := make(chan *countedTerminal, 1)
			r.o.SpawnTerminal = func(_ Command, w, h int) (TerminalProcess, error) {
				p, err := SpawnTerminal(Command{Argv: []string{exe, "--pane-io"}, Env: []string{"GORACE=atexit_sleep_ms=0"}}, w, h)
				if err != nil {
					return nil, err
				}
				wrapped := &countedTerminal{TerminalProcess: p}
				spawned <- wrapped
				if mode == "exit_open" {
					<-release
				}
				return wrapped, nil
			}
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			id := protocol.NewStringID("race")
			if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: json.RawMessage(`{"action":"demo.pane-panel"}`)}); err != nil {
				t.Fatal(err)
			}
			replies := make(chan protocol.Message, 1)
			go func() { var reply protocol.Message; _ = r.reply.Decode(&reply); replies <- reply }()
			requestCtx := take(t, opened)
			var p *countedTerminal
			if mode == "eof_pending_open" {
				_ = r.in.Close()
				take(t, requestCtx.Done())
				close(release)
			} else {
				p = take(t, spawned)
				t.Cleanup(func() { _ = p.Signal(syscall.SIGKILL); _ = p.TerminalProcess.Close() })
				if mode == "exit_open" {
					_ = r.in.Close()
					take(t, requestCtx.Done())
					close(release)
				} else {
					if reply := take(t, replies); reply.Error != nil {
						t.Fatal(reply.Error)
					}
					var attach protocol.TerminalAttachParams
					_ = json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attach)
					// Positive output proves the helper is running before host close and exit.
					r.host.until(t, protocol.MethodTerminalWrite, "")
					paneNotice(t, r, protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: attach.Surface}}})
					take(t, checking)
					if _, err := p.Write([]byte("exit\n")); err != nil {
						t.Fatal(err)
					}
					close(release)
					r.host.until(t, protocol.MethodStateSet, "attached")
					_ = r.in.Close()
				}
			}
			err = take(t, r.exit)
			r.exit <- err
			if err != nil {
				t.Fatal(err)
			}
			if p == nil {
				select {
				case p = <-spawned:
					t.Fatalf("pending open spawned pid %d after EOF", p.PID())
				default:
				}
			} else {
				if p.closes.Load() != 1 {
					t.Fatalf("terminal closed %d times", p.closes.Load())
				}
				if err := syscall.Kill(-p.PID(), 0); err != syscall.ESRCH {
					t.Fatalf("process group %d leaked: %v", p.PID(), err)
				}
			}
			closes := 0
			for len(r.host.events) > 0 {
				if (<-r.host.events).method == protocol.MethodSurfaceClose {
					closes++
				}
			}
			if closes > 1 {
				t.Fatalf("duplicate surface.close: %d", closes)
			}
		})
	}
}

// Keep the I/O contract explicit for the wrapper used above.
var _ io.ReadWriteCloser = (*countedTerminal)(nil)
