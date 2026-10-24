package shim

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestRound3ActionArgsOnlyValidatePaneFields(t *testing.T) {
	for _, scope := range []string{"ordinary", "pane"} {
		t.Run(scope, func(t *testing.T) {
			for _, tc := range []struct{ name, args string }{
				{"width", `{"width":"abc","context":{"selected_text":"relayed"}}`},
				{"height", `{"height":"abc","context":{"selected_text":"relayed"}}`},
				{"env", `{"env":{"A":1},"context":{"selected_text":"relayed"}}`},
				{"combined", `{"width":"abc","env":{"A":1},"context":{"selected_text":"relayed"}}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := newRig(t, "never", false, "")
					r.start(t)
					r.host.until(t, protocol.MethodStateSet, "")
					action := "demo.go"
					if scope == "pane" {
						action = "demo.pane-panel"
					}
					id := protocol.NewStringID("round3")
					params, _ := json.Marshal(map[string]any{"action": action, "args": json.RawMessage(tc.args)})
					if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: params}); err != nil {
						t.Fatal(err)
					}
					var reply protocol.Message
					if err := r.reply.Decode(&reply); err != nil {
						t.Fatal(err)
					}
					if scope == "pane" {
						if reply.Error == nil || reply.Error.Code != protocol.CodeInvalidParams {
							t.Fatalf("invalid pane args accepted: %s %v", reply.Result, reply.Error)
						}
						select {
						case <-r.spawns:
							t.Fatal("invalid pane args started a command")
						default:
						}
						return
					}
					if reply.Error != nil {
						t.Fatalf("ordinary action validated unused pane fields: %v", reply.Error)
					}
					var result struct {
						Started bool
						PID     int
					}
					if err := json.Unmarshal(reply.Result, &result); err != nil {
						t.Fatal(err)
					}
					spawned := take(t, r.spawns)
					if !result.Started || result.PID != spawned.process.PID() {
						t.Fatal("ordinary action did not launch")
					}
					env := envMap(spawned.command.Env)
					var context InvocationContext
					if err := json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &context); err != nil {
						t.Fatal(err)
					}
					if context.SelectedText == nil || *context.SelectedText != "relayed" {
						t.Fatal("ordinary action lost its relayed context")
					}
					if _, ok := env["A"]; ok {
						t.Fatal("ordinary action applied pane env")
					}
					spawned.process.done <- 0
				})
			}
		})
	}
}

func TestRound3ProxyOpenHasOverallDeadline(t *testing.T) {
	for _, mode := range []string{"host_call", "gate_wait"} {
		t.Run(mode, func(t *testing.T) {
			r, panes := paneRig(t)
			original := r.host.call
			entered := make(chan context.Context, 1)
			r.host.call = func(ctx context.Context, method string, params any) (json.RawMessage, error) {
				if method == protocol.MethodSurfaceOpen {
					entered <- ctx
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return original(ctx, method, params)
			}
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "")
			if reply := r.invoke(t, "demo.go"); reply.Error != nil {
				t.Fatal(reply.Error)
			}
			ordinary := take(t, r.spawns)
			path := envMap(ordinary.command.Env)["HERDR_SOCKET_PATH"]
			ordinary.process.done <- 0
			type received struct {
				message protocol.Message
				err     error
			}
			var ownerTimer *testTimer
			var ownerCtx context.Context
			var ownerReply chan received
			if mode == "gate_wait" {
				id := protocol.NewStringID("gate-owner")
				if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: json.RawMessage(`{"action":"demo.pane-panel"}`)}); err != nil {
					t.Fatal(err)
				}
				ownerReply = make(chan received, 1)
				decoded := make(chan struct{})
				go func() {
					defer close(decoded)
					var message protocol.Message
					err := r.reply.Decode(&message)
					ownerReply <- received{message, err}
				}()
				defer func() { _ = r.in.Close(); take(t, decoded) }()
				ownerCtx = take(t, entered)
				ownerTimer = r.clock.next(t, 10*time.Second)
			}
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan received, 1)
			decoded := make(chan struct{})
			go func() {
				defer close(decoded)
				var message protocol.Message
				err := json.NewDecoder(conn).Decode(&message)
				result <- received{message, err}
			}()
			defer func() { _ = conn.Close(); take(t, decoded) }()
			if _, err = conn.Write([]byte("{\"id\":\"proxy-open\",\"method\":\"plugin.pane.open\",\"params\":{\"plugin_id\":\"demo\",\"entrypoint\":\"panel\"}}\n")); err != nil {
				t.Fatal(err)
			}
			if mode == "host_call" {
				ownerCtx = take(t, entered)
			}
			// This is the actual proxy dispatch timer, not a pane sub-call or an
			// action timer. Its context bounds the entire interception, including admission.
			r.clock.fire(t, 10*time.Second)
			reply := take(t, result)
			if reply.err != nil || reply.message.Error == nil || reply.message.Error.Code != protocol.CodeTimeout {
				t.Fatalf("proxy open exceeded overall bound: %s %v %v", reply.message.Result, reply.message.Error, reply.err)
			}
			if mode == "gate_wait" {
				select {
				case <-ownerCtx.Done():
					t.Fatal("waiting proxy request cancelled the gate owner")
				default:
				}
				ownerTimer.fire()
				owner := take(t, ownerReply)
				if owner.err != nil || owner.message.Error == nil || owner.message.Error.Code != protocol.CodeTimeout {
					t.Fatalf("gate owner: %v %v", owner.message.Error, owner.err)
				}
			} else if ownerCtx.Err() == nil {
				t.Fatal("proxy deadline did not cancel the host call")
			}
			select {
			case <-panes:
				t.Fatal("timed-out open started a PTY")
			default:
			}
		})
	}
}
