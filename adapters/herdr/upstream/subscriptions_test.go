package upstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestFilterHookSubscriptionsPerEntry(t *testing.T) {
	names := []string{"pane.focused", "pane.agent_status_changed", "future.event", "pane.output_matched", "pane.focused", "workspace.renamed", "pane.scroll_changed", "pane.agent_status_changed"}
	original := append([]string(nil), names...)
	wantAllowed := []string{"pane.focused", "future.event", "workspace.renamed"}
	wantRefused := []string{"pane.agent_status_changed", "pane.output_matched", "pane.scroll_changed"}
	// Each call represents a fresh attach: refusals must not be globally silenced,
	// and duplicate on names must produce only one loggable refusal in that attach.
	for attach := 0; attach < 2; attach++ {
		allowed, refused := upstream.FilterHookSubscriptions(names)
		if !reflect.DeepEqual(allowed, wantAllowed) {
			t.Fatalf("allowed = %v, want %v", allowed, wantAllowed)
		}
		var refusedNames []string
		for _, refusal := range refused {
			refusedNames = append(refusedNames, refusal.Name)
			e := named(t, refusal.Err, "invalid_subscription")
			if !strings.Contains(e.Message, refusal.Name) {
				t.Fatalf("refusal missing on name: %v", e)
			}
		}
		if !reflect.DeepEqual(refusedNames, wantRefused) {
			t.Fatalf("refused = %v, want %v", refusedNames, wantRefused)
		}
	}
	if !reflect.DeepEqual(names, original) {
		t.Fatal("filter changed the caller's names")
	}
	allowed, refused := upstream.FilterHookSubscriptions(wantRefused)
	if len(allowed) != 0 || len(refused) != 3 {
		t.Fatalf("all refused: allowed=%v, refused=%v", allowed, refused)
	}
}

func TestFilteredHooksSubscribeAndReceive(t *testing.T) {
	path := socket(t)
	s := server(t, path, fakeherdr.Options{})
	allowed, refused := upstream.FilterHookSubscriptions([]string{"pane.focused", "pane.agent_status_changed", "workspace.renamed", "pane.output_matched", "pane.scroll_changed"})
	if len(refused) != 3 {
		t.Fatalf("refused = %v", refused)
	}
	stream, err := client(path, newClock()).Subscribe(t.Context(), allowed)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	requests := s.Requests()
	if len(requests) != 1 || requests[0].Method != "events.subscribe" {
		t.Fatalf("requests = %v", requests)
	}
	entries := requests[0].Params["subscriptions"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["type"] != "pane.focused" || entries[1].(map[string]any)["type"] != "workspace.renamed" {
		t.Fatalf("entries = %v", entries)
	}
	for _, event := range []struct {
		name string
		data map[string]any
	}{
		{"pane_focused", map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"}},
		{"workspace_renamed", map[string]any{"type": "workspace_renamed", "workspace_id": "w", "label": "renamed"}},
	} {
		if err = s.Emit(event.name, event.data); err != nil {
			t.Fatal(err)
		}
		got, err := stream.Next()
		if err != nil || got.Event != event.name {
			t.Fatalf("event = %+v, error = %v", got, err)
		}
	}
}

type scriptedRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type scriptClient struct {
	client   *upstream.Client
	clock    *manualClock
	dials    atomic.Int32
	requests chan scriptedRequest
}

func newScriptClient(t *testing.T, respond func(scriptedRequest) string) *scriptClient {
	t.Helper()
	script := &scriptClient{clock: newClock(), requests: make(chan scriptedRequest, 16)}
	var mu sync.Mutex
	var connections []net.Conn
	var workers sync.WaitGroup
	script.client = upstream.New(upstream.Options{Clock: script.clock, Dial: func(context.Context, string, string) (net.Conn, error) {
		script.dials.Add(1)
		local, remote := net.Pipe()
		mu.Lock()
		connections = append(connections, local, remote)
		workers.Add(1)
		mu.Unlock()
		go func() {
			defer workers.Done()
			defer remote.Close()
			var req scriptedRequest
			if json.NewDecoder(remote).Decode(&req) != nil {
				return
			}
			script.requests <- req
			if _, err := io.WriteString(remote, respond(req)); err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, remote) // let the client own the lifetime after the reply.
		}()
		return local, nil
	}})
	t.Cleanup(func() {
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return script
}

func TestSubscribePreservesServerRefusalAndPassesUnknownNames(t *testing.T) {
	for _, code := range []string{"server_rejected", "invalid_params", "unavailable"} {
		t.Run(code, func(t *testing.T) {
			const message = "the server's exact refusal"
			var subscriptions atomic.Int32
			script := newScriptClient(t, func(req scriptedRequest) string {
				switch req.Method {
				case "events.subscribe":
					if subscriptions.Add(1) == 1 {
						raw, _ := json.Marshal(map[string]any{"id": req.ID, "error": map[string]string{"code": code, "message": message}})
						return string(raw) + "\n"
					}
					return `{"id":"1","result":{"type":"subscription_started"}}` + "\n" + `{"event":"pane_focused","data":{"type":"pane_focused","pane_id":"p","workspace_id":"w"}}` + "\n"
				case "ping":
					return `{"id":"1","result":{"type":"pong","version":"0.9.5","protocol":22}}` + "\n"
				case "plugin.list":
					return `{"id":"1","result":{"type":"plugin_list","plugins":[]}}` + "\n"
				default:
					return ""
				}
			})
			// Unknown names are not locally rewritten, validated, or called unknown_event.
			stream, err := script.client.Subscribe(t.Context(), []string{"future.event"})
			if stream != nil {
				stream.Close()
				t.Fatal("refusal returned a stream")
			}
			e := named(t, err, code)
			if e.Message != message || e.Data != nil {
				t.Fatalf("remote refusal changed: %+v", e)
			}
			if script.dials.Load() != 1 || len(script.clock.added) != 0 {
				t.Fatal("subscription refusal triggered a retry or deadline")
			}
			req := <-script.requests
			var params struct {
				Subscriptions []struct {
					Type string `json:"type"`
				} `json:"subscriptions"`
			}
			if json.Unmarshal(req.Params, &params) != nil || len(params.Subscriptions) != 1 || params.Subscriptions[0].Type != "future.event" {
				t.Fatalf("names not passed through: %s", req.Params)
			}
			// A refusal does not poison the client or force a reconnect loop. The caller
			// can keep serving until its next attach, then explicitly retry event hooks.
			session, err := script.client.Attach(t.Context(), "0.9.0")
			if err != nil || session.Version != "0.9.5" {
				t.Fatalf("next attach = %+v, %v", session, err)
			}
			if subscriptions.Load() != 1 {
				t.Fatal("reattach subscribed without the caller's decision")
			}
			stream, err = script.client.Subscribe(t.Context(), []string{"pane.focused"})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			event, err := stream.Next()
			if err != nil || event.Event != "pane_focused" || subscriptions.Load() != 2 {
				t.Fatalf("next subscription: %+v, %v", event, err)
			}
		})
	}
}

func TestSubscribeMalformedAcknowledgementIsProtocol(t *testing.T) {
	for _, tt := range []struct{ name, line string }{
		{"wrong type", `{"id":"1","result":{"type":"ok"}}`},
		{"wrong id", `{"id":"wrong","result":{"type":"subscription_started"}}`},
		{"event before ack", `{"event":"pane_focused","data":{}}`},
		{"bad JSON", `not-json`},
		{"mistyped error", `{"id":"1","error":{"code":7,"message":"bad"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := newScriptClient(t, func(scriptedRequest) string { return tt.line + "\n" })
			stream, err := script.client.Subscribe(t.Context(), []string{"pane.focused"})
			if stream != nil {
				stream.Close()
				t.Fatal("malformed acknowledgement returned a stream")
			}
			e := named(t, err, "herdr_protocol")
			if !strings.Contains(e.Message, "events.subscribe") || script.dials.Load() != 1 {
				t.Fatalf("error = %v, dials=%d", e, script.dials.Load())
			}
		})
	}
}

func TestProtocolCodeScopedToAttachAndAcknowledgement(t *testing.T) {
	for _, line := range []string{`not-json`, `{"id":"wrong","result":{"type":"ok"}}`} {
		t.Run(line, func(t *testing.T) {
			script := newScriptClient(t, func(scriptedRequest) string { return line + "\n" })
			_, err := script.client.Call(t.Context(), "pane.current", nil)
			if err == nil {
				t.Fatal("malformed generic reply was accepted")
			}
			var named *upstream.Error
			if errors.As(err, &named) && named.Code == "herdr_protocol" {
				t.Fatalf("attach-only code leaked to generic call: %v", err)
			}
		})
	}
}
