package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type groupMember struct {
	Role string `json:"role"`
	PID  int    `json:"pid"`
	PGID int    `json:"pgid"`
	conn net.Conn
}

// Both members ignore terminal-master HUP. Only an explicit group TERM can
// stop the descendant while its independent control connection remains open.
// The leader reaps its child before exiting, avoiding orphan-zombie timing.
func paneGroupHelper(role, path string) int {
	signal.Ignore(syscall.SIGHUP)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	c, err := net.Dial("unix", path)
	if err != nil {
		return 2
	}
	defer c.Close()
	command := make(chan struct{})
	go func() { var b [1]byte; _, _ = c.Read(b[:]); close(command) }()
	var child *exec.Cmd
	if role == "leader" {
		child = exec.Command(os.Args[0], "--pane-group-child", path)
		child.Env = os.Environ()
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return 3
		}
	}
	if err := json.NewEncoder(c).Encode(groupMember{Role: role, PID: os.Getpid(), PGID: syscall.Getpgrp()}); err != nil {
		return 4
	}
	if role == "child" {
		select {
		case <-term:
		case <-command:
		}
		return 0
	}
	fmt.Print("group ready\n")
	select {
	case <-term:
		// Deliberately do not forward TERM to the child here. Leader-only signal
		// mutations leave it alive, and shutdown cannot finish until test cleanup.
		if err := child.Wait(); err != nil {
			return 5
		}
		if err := json.NewEncoder(c).Encode(groupMember{Role: "reaped", PID: child.Process.Pid}); err != nil {
			return 7
		}
		<-command // host-first tests decide when the leader may exit
	case <-command:
		_ = child.Process.Signal(syscall.SIGTERM)
		if err := child.Wait(); err != nil {
			return 6
		}
	}
	return 0
}

func (p *countedTerminal) GroupAlive() bool { return groupAlive(p.TerminalProcess) }

func TestHardeningRealPTYTeardownRaces(t *testing.T) {
	for _, mode := range []string{"exit_first", "host_first", "exit_open", "eof_pending_open"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := paneRig(t)
			host := &countedHost{testHost: r.host}
			r.o.Host = host
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			opened := make(chan context.Context, 1)
			checking := make(chan struct{}, 1)
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			original := r.host.call
			r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
				if m == protocol.MethodSurfaceOpen {
					opened <- ctx
					if mode == "eof_pending_open" {
						<-ctx.Done()
						<-release
					}
				}
				if m == protocol.MethodStateGet && (mode == "exit_first" || mode == "host_first") {
					checking <- struct{}{}
					<-release
				}
				return original(ctx, m, p)
			}
			path := filepath.Join(filepath.Dir(r.o.Socket), "group.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			members := make(chan groupMember, 2)
			go func() {
				for range 2 {
					c, err := listener.Accept()
					if err != nil {
						return
					}
					var m groupMember
					if err := json.NewDecoder(c).Decode(&m); err != nil {
						_ = c.Close()
						return
					}
					m.conn = c
					members <- m
				}
			}()
			spawned := make(chan *countedTerminal, 1)
			r.o.SpawnTerminal = func(_ Command, w, h int) (TerminalProcess, error) {
				process, err := SpawnTerminal(Command{Argv: []string{exe, "--pane-group-leader", path}, Env: []string{"GORACE=atexit_sleep_ms=0"}}, w, h)
				if err != nil {
					return nil, err
				}
				p := &countedTerminal{TerminalProcess: process, signals: make(chan syscall.Signal, 16)}
				spawned <- p
				if mode == "exit_open" {
					<-release
				}
				return p, nil
			}
			r.start(t)
			t.Cleanup(unblock)
			r.host.until(t, protocol.MethodStateSet, "attached")
			id := protocol.NewStringID("race")
			if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: json.RawMessage(`{"action":"demo.pane-panel"}`)}); err != nil {
				t.Fatal(err)
			}
			replies := make(chan protocol.Message, 1)
			go func() {
				var reply protocol.Message
				if err := r.reply.Decode(&reply); err != nil {
					t.Error(err)
				}
				replies <- reply
			}()
			requestCtx := take(t, opened)
			var p *countedTerminal
			var leader, child groupMember
			stopLeader := func() {
				if _, err := leader.conn.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "eof_pending_open" {
				_ = r.in.Close()
				take(t, requestCtx.Done())
				unblock()
			} else {
				p = take(t, spawned)
				// On an assertion failure release both control connections so even a
				// deliberate leader-only signal mutation cannot strand a child process.
				t.Cleanup(func() {
					if child.conn != nil {
						_ = child.conn.Close()
					}
					if leader.conn != nil {
						_, _ = leader.conn.Write([]byte{1})
						_ = leader.conn.Close()
					}
					_ = p.TerminalProcess.Close()
				})
				for range 2 {
					m := take(t, members)
					if m.PGID != p.PID() {
						t.Fatalf("%s pid %d in group %d, want %d", m.Role, m.PID, m.PGID, p.PID())
					}
					switch m.Role {
					case "leader":
						leader = m
					case "child":
						child = m
					default:
						t.Fatal(m.Role)
					}
				}
				if leader.PID != p.PID() || child.PID == 0 || child.PID == leader.PID {
					t.Fatal("missing same-group descendant")
				}
				if err := syscall.Kill(child.PID, 0); err != nil {
					t.Fatalf("descendant not alive before close: %v", err)
				}
				waitStop := func() {
					if take(t, p.signals) != syscall.SIGHUP || take(t, p.signals) != syscall.SIGTERM {
						t.Fatal("missing group stop signals")
					}
					type response struct {
						member groupMember
						err    error
					}
					ack := make(chan response, 1)
					go func() {
						var m groupMember
						err := json.NewDecoder(leader.conn).Decode(&m)
						ack <- response{m, err}
					}()
					got := take(t, ack)
					if got.err != nil {
						t.Fatal(got.err)
					}
					if got.member.Role != "reaped" || got.member.PID != child.PID {
						t.Fatal("group TERM did not reap descendant before leader exit")
					}
				}
				if mode == "exit_open" {
					_ = r.in.Close()
					take(t, requestCtx.Done())
					unblock()
					waitStop()
					stopLeader()
				} else {
					if reply := take(t, replies); reply.Error != nil {
						t.Fatal(reply.Error)
					}
					var attach protocol.TerminalAttachParams
					if err := json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attach); err != nil {
						t.Fatal(err)
					}
					paneNotice(t, r, protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: attach.Surface}}})
					take(t, checking)
					if mode == "exit_first" {
						stopLeader()
						r.host.until(t, protocol.MethodSurfaceClose, "")
						if host.closes.Load() != 1 {
							t.Fatal("exit did not release surface exactly once")
						}
						unblock()
					} else {
						unblock()
						waitStop() // host lifetime is confirmed gone before leader may exit
						if host.closes.Load() != 0 {
							t.Fatal("host close sent redundant surface.close")
						}
						stopLeader()
					}
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
				if err := syscall.Kill(child.PID, 0); err != syscall.ESRCH {
					t.Fatalf("descendant %d leaked: %v", child.PID, err)
				}
			}
			want := int32(1)
			if mode == "host_first" {
				want = 0
			}
			if got := host.closes.Load(); got != want {
				t.Fatalf("surface release count=%d want %d", got, want)
			}
		})
	}
}
