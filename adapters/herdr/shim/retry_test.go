package shim

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestContextFailureClassification(t *testing.T) {
	for _, method := range []string{"pane.current", "workspace.get", "tab.get"} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/remote=%t", method, remote), func(t *testing.T) {
				r := newRig(t, false, "", func(o *fakeherdr.Options) {
					if remote {
						o.Errors = map[string]fakeherdr.ReplyError{method: {Code: "invalid_params", Message: "context refused"}}
					} else {
						o.DisconnectFirst = map[string]int{method: 1}
					}
				})
				r.start(t)
				r.host.until(t, protocol.MethodStateSet, "")
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
