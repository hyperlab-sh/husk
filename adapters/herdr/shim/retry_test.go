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

func TestContextFailureClassification(t *testing.T) {
	for _, action := range []string{"demo.go", "demo.pane-panel"} {
		for _, method := range []string{"pane.current", "workspace.get", "tab.get"} {
			for _, failure := range []string{"transport", "remote", "timeout"} {
				t.Run(fmt.Sprintf("%s/%s/%s", action, method, failure), func(t *testing.T) {
					entered := make(chan struct{}, 1)
					r, terminals := paneRig(t, func(o *fakeherdr.Options) {
						switch failure {
						case "remote":
							o.Errors = map[string]fakeherdr.ReplyError{method: {Code: "invalid_params", Message: "context refused"}}
						case "transport":
							o.DisconnectFirst = map[string]int{method: 1}
						case "timeout":
							o.Gates = map[string]<-chan struct{}{method: make(chan struct{})}
							o.OnRequest = func(req fakeherdr.Request) {
								if req.Method == method {
									entered <- struct{}{}
								}
							}
						}
					})
					r.start(t)
					r.host.until(t, protocol.MethodStateSet, "")
					replies := make(chan protocol.Message, 1)
					go func() { replies <- r.invoke(t, action) }()
					if failure == "timeout" {
						take(t, entered)
						r.clock.fire(t, 2*time.Second)
					}
					reply := take(t, replies)
					if failure == "remote" {
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
						t.Fatal("command started with incomplete context")
					default:
					}
					select {
					case <-terminals:
						t.Fatal("terminal started with incomplete context")
					default:
					}
					if action == "demo.pane-panel" {
						r.host.until(t, protocol.MethodSurfaceClose, "")
					}
				})
			}
		}
	}
}
