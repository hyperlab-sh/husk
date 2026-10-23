package services

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/secrets"
)

func TestGrantRevokeShutdownDoesNotWrite(t *testing.T) {
	for _, method := range []string{"grant", "revoke"} {
		for _, phase := range []string{"post rejected", "already closed", "queued before close"} {
			t.Run(method+"/"+phase, func(t *testing.T) {
				posted := make(chan func(), 1)
				var mode atomic.Int32 // 0: run, 1: reject, 2: hold
				var posts atomic.Int32
				r := newSecretRig(t, rigOpts{post: func(f func()) bool {
					posts.Add(1)
					switch mode.Load() {
					case 1:
						return false
					case 2:
						posted <- f
					default:
						f()
					}
					return true
				}})
				r.declare("a")
				if method == "revoke" {
					if err := r.s.Grant(context.Background(), cliCaller, "a", "git.token"); err != nil {
						t.Fatal(err)
					}
				}
				before, postsBefore := r.tree.Snapshot(), posts.Load()
				switch phase {
				case "post rejected":
					mode.Store(1)
				case "already closed":
					r.s.Close()
				case "queued before close":
					mode.Store(2)
				}
				done := make(chan error, 1)
				go func() {
					if method == "grant" {
						done <- r.s.Grant(context.Background(), cliCaller, "a", "git.token")
					} else {
						done <- r.s.Revoke(context.Background(), cliCaller, "a", "git.token")
					}
				}()
				if phase == "queued before close" {
					write := <-posted
					r.s.Close()
					write() // draining a queued callback must not mutate State
				}
				if err := <-done; reason(t, err) != "cancelled/shutdown" {
					t.Fatalf("%s = %v", method, err)
				}
				if r.tree.Snapshot() != before {
					t.Fatal("shutdown write changed grants")
				}
				if phase == "already closed" && posts.Load() != postsBefore {
					t.Fatal("shutdown still posted a write")
				}
			})
		}
	}
}

func TestAcceptedWriteRejectedByLoopDropsRead(t *testing.T) {
	r := newSecretRig(t, rigOpts{post: func(func()) bool { return false }})
	r.declare("a")
	before := r.tree.Snapshot()
	got := r.readAsync(context.Background(), "a", "git.token")
	r.next().reply <- secrets.Accepted
	if res := <-got; res.value != "" || reason(t, res.err) != "cancelled/shutdown" {
		t.Fatalf("rejected accept = %+v", res)
	}
	if len(r.log()) != 0 || r.tree.Snapshot() != before {
		t.Fatalf("dropped read wrote grants or published an event: %+v", r.log())
	}
}

// A host close and a Pod departure both drop waiting reads, rather than
// answering user_denied. Neither can revive an accepted but held grant write.
func TestLifecycleDropsPendingReads(t *testing.T) {
	for _, cause := range []string{"shutdown", "pod departed"} {
		for _, phase := range []string{"shown shared", "queued", "accepting"} {
			t.Run(cause+"/"+phase, func(t *testing.T) {
				posted := make(chan func(), 1)
				r := newSecretRig(t, rigOpts{post: func(f func()) bool { posted <- f; return true }})
				r.declare("a", "b")
				before := r.tree.Snapshot()
				target := "a"
				var blocker <-chan readResult
				var blockingPrompt *promptCall
				if phase == "queued" {
					blocker = r.readAsync(context.Background(), "a", "git.token")
					blockingPrompt = r.next()
					target = "b"
				}
				got := r.readAsync(context.Background(), target, "git.token")
				var pc *promptCall
				var write func()
				var shared <-chan readResult
				if phase == "queued" {
					r.joined(target, 1, true)
				} else {
					pc = r.next()
					if phase == "accepting" {
						pc.reply <- secrets.Accepted
						write = <-posted
					} else {
						shared = r.readAsync(context.Background(), target, "git.token")
						r.joined(target, 2, false)
					}
				}
				want := "cancelled/plugin_exited"
				if cause == "shutdown" {
					r.s.Close()
					want = "cancelled/shutdown"
				} else {
					r.s.DropPlugin(target)
				}
				for _, ch := range []<-chan readResult{got, shared} {
					if ch != nil {
						if res := <-ch; res.value != "" || reason(t, res.err) != want {
							t.Fatalf("dropped read = %+v, want %s", res, want)
						}
					}
				}
				if phase == "shown shared" {
					<-pc.closed
				}
				if write != nil {
					write()
				}
				if len(r.log()) != 0 || r.tree.Snapshot() != before {
					t.Fatalf("drop wrote grants or published an event: %+v", r.log())
				}
				if blocker != nil {
					if cause != "shutdown" {
						blockingPrompt.reply <- secrets.Dismissed
					}
					<-blocker
				}
				if cause != "shutdown" {
					// No grant or decline memory: a fresh read prompts again.
					again := r.readAsync(context.Background(), target, "git.token")
					pc2 := r.next()
					if pc2.p.Plugin != target {
						t.Fatalf("next prompt = %+v", pc2.p)
					}
					pc2.reply <- secrets.Dismissed
					<-again
				}
			})
		}
	}
}

func TestCLIGrantRechecksPostedDeclaration(t *testing.T) {
	for _, requests := range []string{`[{"name":"git.token","purpose":"Changed"}]`, `[]`} {
		t.Run(requests, func(t *testing.T) {
			posted := make(chan func(), 1)
			r := newSecretRig(t, rigOpts{post: func(f func()) bool { posted <- f; return true }})
			r.declare("a")
			before := r.tree.Snapshot()
			done := make(chan error, 1)
			go func() { done <- r.s.Grant(context.Background(), cliCaller, "a", "git.token") }()
			write := <-posted
			apply(t, r.s, catalogWith("a", requests))
			write()
			if err := <-done; reason(t, err) != "not_found/not_declared" {
				t.Fatalf("stale CLI consent = %v", err)
			}
			if r.tree.Snapshot() != before {
				t.Fatal("stale CLI consent wrote a grant")
			}
		})
	}
}

func TestInvalidStoredTextCanBeReplaced(t *testing.T) {
	for _, bad := range []string{"\xff\xfe", strings.Repeat("x", secrets.MaxValue+1)} {
		for _, via := range []string{"plugin get", "cli get", "reveal"} {
			name := "not utf8/"
			if len(bad) > secrets.MaxValue {
				name = "over cap/"
			}
			t.Run(name+via, func(t *testing.T) {
				r := newSecretRig(t, rigOpts{})
				r.declare("a")
				if err := r.s.Grant(context.Background(), cliCaller, "a", "git.token"); err != nil {
					t.Fatal(err)
				}
				r.crypto.data["git.token"] = []byte(bad)
				read := func() (string, error) {
					switch via {
					case "plugin get":
						return r.s.Read(context.Background(), pluginCaller("a"), "git.token")
					case "cli get":
						return r.s.Read(context.Background(), cliCaller, "git.token")
					default:
						return r.s.Reveal(context.Background(), cliCaller, "git.token")
					}
				}
				v, err := read()
				if v != "" || reason(t, err) != "unavailable/storage" || r.lastEvent().Outcome != "unavailable" {
					t.Fatalf("invalid stored text returned %d bytes, error %v, events %+v", len(v), err, r.log())
				}
				if err := r.s.Set(cliCaller, "git.token", "replacement"); err != nil {
					t.Fatal(err)
				}
				if v, err := read(); err != nil || v != "replacement" {
					t.Fatalf("replacement = %q, %v", v, err)
				}
			})
		}
	}
}

func TestFailedAcceptedWritePromptsAgain(t *testing.T) {
	r := newSecretRig(t, rigOpts{maxBytes: len(`{"grants":[]}`)})
	r.declare("a")
	for i := 0; i < 2; i++ {
		got := r.readAsync(context.Background(), "a", "git.token")
		r.next().reply <- secrets.Accepted
		if res := <-got; res.value != "" || reason(t, res.err) != "unavailable/storage" {
			t.Fatalf("accept %d = %+v", i, res)
		}
	}
}
