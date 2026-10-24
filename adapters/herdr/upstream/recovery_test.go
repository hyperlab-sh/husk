package upstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestSubscribeRejectsParameterizedVariantsBeforeDial(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(fakeherdr.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	request := schema["schemas"].(map[string]any)["request"].(map[string]any)
	subscription := request["$defs"].(map[string]any)["Subscription"].(map[string]any)
	for _, branch := range subscription["oneOf"].([]any) {
		variant := branch.(map[string]any)
		if len(variant["required"].([]any)) == 1 {
			continue
		}
		name := variant["properties"].(map[string]any)["type"].(map[string]any)["const"].(string)
		t.Run(name, func(t *testing.T) {
			dials := 0
			c := upstream.New(upstream.Options{Dial: func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("must not dial")
			}})
			_, err := c.Subscribe(t.Context(), []string{"pane.focused", name})
			e := named(t, err, "invalid_subscription")
			if dials != 0 || !strings.Contains(e.Message, name) {
				t.Fatalf("dials=%d, error=%v", dials, e)
			}
		})
	}
}

func TestFakeValidatesWireRequests(t *testing.T) {
	path := socket(t)
	s := server(t, path, fakeherdr.Options{Passthrough: []string{"custom.method", "events.subscribe"}})
	c := client(path, newClock())
	for _, tt := range []struct {
		method string
		params any
	}{
		{"events.subscribe", map[string]any{"subscriptions": []any{map[string]any{"type": "pane.agent_status_changed"}}}},
		{"plugin.pane.open", map[string]any{"plugin_id": "sample"}},
		{"ping", []any{}},
		{"unknown.method", map[string]any{}},
	} {
		t.Run(tt.method, func(t *testing.T) {
			_, err := c.Call(t.Context(), tt.method, tt.params)
			named(t, err, "invalid_params")
		})
	}
	// Invalid requests remain recorded, but cannot register subscriptions.
	if len(s.Requests()) != 4 {
		t.Fatalf("records = %d", len(s.Requests()))
	}
	raw, err := c.Call(t.Context(), "custom.method", map[string]any{"vendor": true})
	if err != nil || !strings.Contains(string(raw), `"type":"ok"`) {
		t.Fatalf("allowlisted call: %s, %v", raw, err)
	}
	// Validation failure must not poison the listener or subsequent valid calls.
	if _, err = c.Attach(t.Context(), "0.9.0"); err != nil {
		t.Fatal(err)
	}
	stream, err := c.Subscribe(t.Context(), []string{"pane.focused"})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
}

// scheduled inspects a timer after a request barrier. Registration must precede
// the request, so an omitted deadline fails immediately rather than hanging.
func (c *manualClock) scheduled(t *testing.T, d time.Duration) *manualTimer {
	t.Helper()
	for {
		select {
		case timer := <-c.added:
			timer.mu.Lock()
			active := timer.active
			timer.mu.Unlock()
			if !active {
				continue
			}
			if timer.d != d {
				t.Fatalf("deadline = %v, want %v", timer.d, d)
			}
			return timer
		default:
			t.Fatal("request has no active deadline")
			return nil
		}
	}
}

func TestWaitAttachTimeoutsRetryWithBackoff(t *testing.T) {
	for _, method := range []string{"ping", "plugin.list"} {
		t.Run(method, func(t *testing.T) {
			path := socket(t)
			clock := newClock()
			gate := make(chan struct{})
			requests := make(chan struct{}, 8)
			server(t, path, fakeherdr.Options{Gates: map[string]<-chan struct{}{method: gate}, OnRequest: func(r fakeherdr.Request) {
				if r.Method == method {
					requests <- struct{}{}
				}
			}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			states := make(chan string, 8)
			type outcome struct {
				session *upstream.Session
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				s, e := client(path, clock).WaitAttach(ctx, "0.9.0", func(v string) { states <- v })
				done <- outcome{s, e}
			}()
			for _, delay := range []time.Duration{5 * time.Second, 10 * time.Second} {
				<-requests
				clock.scheduled(t, 2*time.Second).fire()
				select {
				case state := <-states:
					if state != "absent" {
						t.Fatalf("state = %s", state)
					}
				case result := <-done:
					t.Fatalf("deadline was terminal: %v", result.err)
				}
				retry := clock.take(t, delay)
				if delay == 10*time.Second {
					close(gate)
				}
				retry.fire()
			}
			result := <-done
			if result.err != nil || result.session.Version != "0.9.5" {
				t.Fatalf("attach: %+v", result)
			}
			if state := <-states; state != "attached" {
				t.Fatalf("state = %s", state)
			}
		})
	}
}

func TestWaitAttachProtocolFailuresAreNamedAndTerminal(t *testing.T) {
	const pong = `{"id":"1","result":{"type":"pong","version":"0.9.5","protocol":22}}` + "\n"
	for _, tt := range []struct{ name, method, response string }{
		{"malformed JSON", "ping", "not-json\n"},
		{"wrong reply id", "ping", `{"id":"wrong","result":{"type":"pong","version":"0.9.5"}}` + "\n"},
		{"malformed pong", "ping", `{"id":"1","result":{"type":"wrong"}}` + "\n"},
		{"invalid version", "ping", `{"id":"1","result":{"type":"pong","version":"broken"}}` + "\n"},
		{"invalid list", "plugin.list", `{"id":"1","result":{"type":"plugin_list","plugins":null}}` + "\n"},
		{"remote ping refusal", "ping", `{"id":"1","error":{"code":"denied","message":"refused"}}` + "\n"},
		{"remote list refusal", "plugin.list", `{"id":"1","error":{"code":"denied","message":"refused"}}` + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := upstream.New(upstream.Options{Clock: newClock(), Dial: func(context.Context, string, string) (net.Conn, error) {
				local, remote := net.Pipe()
				go func() {
					defer remote.Close()
					var request struct {
						Method string `json:"method"`
					}
					if json.NewDecoder(remote).Decode(&request) != nil {
						return
					}
					line := pong
					if request.Method == tt.method {
						line = tt.response
					}
					_, _ = io.WriteString(remote, line)
				}()
				return local, nil
			}})
			states := make(chan string, 1)
			done := make(chan error, 1)
			go func() { _, err := c.WaitAttach(ctx, "0.9.0", func(v string) { states <- v }); done <- err }()
			select {
			case err := <-done:
				e := named(t, err, "herdr_protocol")
				if !strings.Contains(e.Message, tt.method) {
					t.Fatalf("missing method: %v", e)
				}
			case state := <-states:
				t.Fatalf("protocol error was retried: %s", state)
			}
		})
	}
}
