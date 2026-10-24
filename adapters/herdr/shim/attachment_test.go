package shim

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const startupAndEvent = "[[startup]]\ncommand=['./startup']\n[[events]]\non='pane.focused'\ncommand=['./event']\n"

func (r *rig) event(t *testing.T, want string) {
	t.Helper()
	if err := r.server.Emit("pane_focused", map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"}); err != nil {
		t.Fatal(err)
	}
	p := take(t, r.spawns)
	if p.command.Argv[0] != want {
		t.Fatalf("command=%v want %s", p.command.Argv, want)
	}
	p.process.done <- 0
}

func (r *rig) replaceServer(t *testing.T, options fakeherdr.Options) {
	t.Helper()
	// Retain the old inode so a fast filesystem allocator cannot reuse it.
	if err := os.Rename(r.o.Socket, r.o.Socket+".old"); err != nil {
		t.Fatal(err)
	}
	r.server.Close()
	next, err := fakeherdr.Start(r.o.Socket, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Close)
	r.server = next
}

func TestStartupRunsOnlyWhenServerIdentityChanges(t *testing.T) {
	for _, change := range []string{"socket", "version", "protocol"} {
		t.Run(change, func(t *testing.T) {
			var changed atomic.Bool
			fixture := func(o *fakeherdr.Options) {
				o.Reply = func(req fakeherdr.Request, result map[string]any) *fakeherdr.ReplyError {
					if req.Method == "ping" && changed.Load() {
						if change == "version" {
							result["version"] = "0.9.6"
						} else if change == "protocol" {
							result["protocol"] = 23
						}
					}
					return nil
				}
			}
			r := newRig(t, "always", false, startupAndEvent, fixture)
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			p := take(t, r.spawns)
			if p.command.Argv[0] != "./startup" {
				t.Fatal("first attach omitted startup")
			}
			p.process.done <- 0
			r.server.DropSubscriptions()
			r.host.until(t, protocol.MethodStateSet, "lost")
			r.clock.fire(t, 5*time.Second)
			r.host.until(t, protocol.MethodStateSet, "attached")
			r.event(t, "./event") // A dispatch barrier after startup setup: no duplicate.
			changed.Store(true)
			if change == "socket" {
				r.replaceServer(t, fakeherdr.Options{})
			} else {
				r.server.DropSubscriptions()
			}
			r.host.until(t, protocol.MethodStateSet, "lost")
			r.clock.fire(t, 5*time.Second)
			r.host.until(t, protocol.MethodStateSet, "attached")
			r.event(t, "./startup")
			take(t, r.spawns).process.done <- 0 // The queued event follows that startup.
		})
	}
}

func TestHookOwnershipForgetsPriorStartupIdentity(t *testing.T) {
	var enabled, changed atomic.Bool
	r := newRig(t, "auto", false, startupAndEvent, func(o *fakeherdr.Options) {
		o.Reply = func(req fakeherdr.Request, result map[string]any) *fakeherdr.ReplyError {
			if req.Method == "plugin.list" {
				result["plugins"] = []map[string]any{{"plugin_id": "demo", "enabled": enabled.Load()}}
			}
			if req.Method == "ping" && changed.Load() {
				result["protocol"] = 23
			}
			return nil
		}
	})
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	take(t, r.spawns).process.done <- 0
	enabled.Store(true)
	r.server.DropSubscriptions()
	r.host.until(t, protocol.MethodStateSet, "lost")
	r.clock.fire(t, 5*time.Second)
	r.host.until(t, protocol.MethodStateSet, "attached")
	timer := r.clock.next(t, 5*time.Second) // Server-owned hooks have no stream.
	changed.Store(true)
	timer.fire()
	r.host.until(t, protocol.MethodStateSet, "lost")
	// The probe differed, but the completed reattach will have the ORIGINAL
	// identity. Startup must run because server ownership forgot the prior run.
	changed.Store(false)
	enabled.Store(false)
	r.clock.fire(t, 5*time.Second)
	r.host.until(t, protocol.MethodStateSet, "attached")
	r.event(t, "./startup")
	take(t, r.spawns).process.done <- 0
}

func TestNoStreamProbeFailurePolicy(t *testing.T) {
	for _, mode := range []string{"none", "filtered", "refused"} {
		for _, failure := range []string{"missing", "refused", "transport", "remote", "timeout", "reset"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				var fail, monitoring atomic.Bool
				blocked := make(chan struct{})
				defer close(blocked)
				pingSeen, dialed := make(chan struct{}, 8), make(chan struct{}, 8)
				extra := ""
				if mode == "filtered" {
					extra = "[[events]]\non='pane.output_matched'\ncommand=['./event']\n"
				} else if mode == "refused" {
					extra = "[[events]]\non='pane.focused'\ncommand=['./event']\n"
				}
				r := newRig(t, "always", false, extra, func(o *fakeherdr.Options) {
					if mode == "refused" {
						o.Errors = map[string]fakeherdr.ReplyError{"events.subscribe": {Code: "unavailable", Message: "explicit server refusal"}}
					}
					o.Reply = func(req fakeherdr.Request, _ map[string]any) *fakeherdr.ReplyError {
						if req.Method != "ping" {
							return nil
						}
						broken := fail.Load()
						if monitoring.Load() {
							pingSeen <- struct{}{}
						}
						if !broken {
							return nil
						}
						if failure == "timeout" {
							<-blocked
							return nil
						}
						return &fakeherdr.ReplyError{Code: "busy", Message: "probe refused"}
					}
				})
				dialErrors := make(chan error, 1)
				r.o.Client = upstream.New(upstream.Options{Socket: r.o.Socket, Clock: r.clock, Dial: func(ctx context.Context, network, path string) (net.Conn, error) {
					select {
					case err := <-dialErrors:
						dialed <- struct{}{}
						return nil, err
					default:
						return guardedDial(t, r.o.Socket)(ctx, network, path)
					}
				}})
				r.start(t)
				r.host.until(t, protocol.MethodStateSet, "attached")
				timer := r.clock.next(t, 5*time.Second)
				monitoring.Store(true)
				steps := []bool{true, true, true}
				if failure == "missing" || failure == "refused" {
					steps = []bool{true}
				} else if failure == "reset" {
					steps = []bool{true, true, false, true, true, true}
				}
				for i, broken := range steps {
					fail.Store(broken && (failure == "remote" || failure == "timeout" || failure == "reset"))
					if failure == "missing" {
						dialErrors <- &net.OpError{Op: "dial", Err: syscall.ENOENT}
					} else if failure == "refused" {
						dialErrors <- &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
					} else if failure == "transport" {
						dialErrors <- &net.OpError{Op: "read", Err: syscall.ENOENT}
					}
					timer.fire()
					if failure == "missing" || failure == "refused" || failure == "transport" {
						take(t, dialed)
					} else {
						take(t, pingSeen)
					}
					var scheduled *testTimer
					if failure == "timeout" {
						// A 5 s tick must already be scheduled while ping is still
						// blocked, rather than starting 5 s after its 2 s deadline.
						scheduled = r.clock.next(t, 5*time.Second)
						r.clock.fire(t, 2*time.Second)
					}
					if i == len(steps)-1 {
						r.host.until(t, protocol.MethodStateSet, "lost")
						break
					}
					if scheduled != nil {
						timer = scheduled
					} else {
						timer = r.clock.next(t, 5*time.Second)
					}
					if reply := r.invoke(t, "demo.go"); reply.Error != nil {
						t.Fatalf("premature loss on probe %d: %v", i+1, reply.Error)
					}
					take(t, r.spawns).process.done <- 0
				}
			})
		}
	}
}

func TestNoStreamProbeDetectsReplacementSocket(t *testing.T) {
	r := newRig(t, "always", false, "[[startup]]\ncommand=['./startup']\n")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	take(t, r.spawns).process.done <- 0
	timer := r.clock.next(t, 5*time.Second)
	r.replaceServer(t, fakeherdr.Options{})
	timer.fire()
	r.host.until(t, protocol.MethodStateSet, "lost")
	r.clock.fire(t, 5*time.Second)
	r.host.until(t, protocol.MethodStateSet, "attached")
	p := take(t, r.spawns)
	if p.command.Argv[0] != "./startup" {
		t.Fatal("replacement server omitted startup")
	}
	p.process.done <- 0
}

func TestAcknowledgedStreamDoesNotScheduleProbes(t *testing.T) {
	r := newRig(t, "always", false, startupAndEvent)
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	take(t, r.spawns).process.done <- 0
	r.event(t, "./event")
	for len(r.clock.timers) > 0 {
		timer := <-r.clock.timers
		timer.mu.Lock()
		probe := timer.active && timer.d == 5*time.Second
		timer.mu.Unlock()
		if probe {
			t.Fatal("acknowledged stream scheduled a loss probe")
		}
	}
	pings := 0
	for _, req := range r.server.Requests() {
		if req.Method == "ping" {
			pings++
		}
	}
	if pings != 1 {
		t.Fatalf("stream attach sent %d pings", pings)
	}
}
