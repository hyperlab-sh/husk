package shim

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSubscribeTransportFailureRetriesWithoutExiting(t *testing.T) {
	for _, failures := range []int{1, 2} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			r := newRig(t, "auto", false, "[[startup]]\ncommand=['./startup']\n[[events]]\non='pane.focused'\ncommand=['./event']\n", func(o *fakeherdr.Options) {
				o.DisconnectFirst = map[string]int{"events.subscribe": failures}
			})
			r.start(t)
			for i := range failures {
				r.host.until(t, protocol.MethodStateSet, "lost")
				if reply := r.invoke(t, "demo.go"); reply.Error == nil || reply.Error.Code != protocol.CodeUnavailable {
					t.Fatalf("action during subscription retry: %+v", reply)
				}
				select {
				case <-r.spawns:
					t.Fatal("startup ran before a subscription was acknowledged")
				default:
				}
				r.clock.fire(t, (5*time.Second)<<i)
			}
			r.host.until(t, protocol.MethodStateSet, "attached")
			startup := take(t, r.spawns)
			startup.process.done <- 0
			if err := r.server.Emit("pane_focused", map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"}); err != nil {
				t.Fatal(err)
			}
			event := take(t, r.spawns)
			if event.command.Argv[0] != "./event" {
				t.Fatal("event hook did not resume after subscription retry")
			}
			event.process.done <- 0
			// A completed attach resets the persistent retry sequence.
			r.server.DropSubscriptions()
			r.host.until(t, protocol.MethodStateSet, "lost")
			r.clock.fire(t, 5*time.Second)
			r.host.until(t, protocol.MethodStateSet, "attached")
			take(t, r.spawns).process.done <- 0
			count := 0
			for _, req := range r.server.Requests() {
				if req.Method == "events.subscribe" {
					count++
				}
			}
			if count != failures+2 {
				t.Fatalf("subscriptions=%d want %d", count, failures+2)
			}
		})
	}
}

func TestContextFailureClassification(t *testing.T) {
	for _, method := range []string{"pane.current", "workspace.get", "tab.get"} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/remote=%t", method, remote), func(t *testing.T) {
				r := newRig(t, "never", false, "", func(o *fakeherdr.Options) {
					if remote {
						o.Errors = map[string]fakeherdr.ReplyError{method: {Code: "invalid_params", Message: "context refused"}}
					} else {
						o.DisconnectFirst = map[string]int{method: 1}
					}
				})
				r.start(t)
				r.host.until(t, protocol.MethodStateSet, "attached")
				reply := r.invoke(t, "demo.go")
				if remote {
					if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || !strings.Contains(reply.Error.Message, "context refused") {
						t.Fatalf("remote context error: %+v", reply)
					}
				} else {
					var reason struct{ Reason string }
					if reply.Error != nil {
						_ = json.Unmarshal(reply.Error.Data, &reason)
					}
					if reply.Error == nil || reply.Error.Code != protocol.CodeUnavailable || reason.Reason != "herdr_absent" {
						t.Fatalf("transport context error: %+v", reply)
					}
				}
				select {
				case <-r.spawns:
					t.Fatal("command started with an incomplete context")
				default:
				}
			})
		}
	}
}

func TestExplicitSubscriptionRefusalKeepsActionsAndStartupLive(t *testing.T) {
	r := newRig(t, "always", true, "[[startup]]\ncommand=['./startup']\n[[events]]\non='pane.focused'\ncommand=['./event']\n", func(o *fakeherdr.Options) {
		o.Errors = map[string]fakeherdr.ReplyError{"events.subscribe": {Code: "invalid_subscription", Message: "server refusal"}}
	})
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	take(t, r.spawns).process.done <- 0
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	take(t, r.spawns).process.done <- 0
	if !strings.Contains(r.logs.String(), "server refusal") {
		t.Fatal("explicit refusal was not logged")
	}
}
