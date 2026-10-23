package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// recordingHandler answers from a table and records served methods.
type recordingHandler struct {
	mu     sync.Mutex
	served []string
	reply  protocol.Message
}

func (h *recordingHandler) Serve(_ context.Context, _ contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	h.mu.Lock()
	h.served = append(h.served, req.Method)
	h.mu.Unlock()
	if h.reply.Error != nil || h.reply.Result != nil {
		return h.reply
	}
	return Reply(req, protocol.EmptyResult{})
}

func (h *recordingHandler) calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.served))
	copy(out, h.served)
	return out
}

func TestMuxRoutesByPrefix(t *testing.T) {
	state := &recordingHandler{}
	session := &recordingHandler{}
	m := NewMux()
	m.Register("state", state)
	m.Register("session", session)

	ctx := context.Background()
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	notifier := contracts.Notifier(nil)

	for _, method := range []string{protocol.MethodStateGet, protocol.MethodStateWatch, protocol.MethodStateClear} {
		if m.Serve(ctx, cli, protocol.Message{Method: method}, notifier).Error != nil {
			t.Fatalf("%s should reach the state adapter", method)
		}
	}
	if m.Serve(ctx, cli, protocol.Message{Method: protocol.MethodSessionPing}, notifier).Error != nil {
		t.Fatal("session.ping should reach the session adapter")
	}
	if len(state.calls()) != 3 || len(session.calls()) != 1 {
		t.Fatalf("state served %v, session served %v", state.calls(), session.calls())
	}
}

func TestMuxUnknownPrefix(t *testing.T) {
	m := NewMux()
	m.Register("state", &recordingHandler{})
	m.Serve(context.Background(), contracts.Caller{Kind: contracts.CallerCLI},
		protocol.Message{Method: protocol.MethodSettingsList}, nil)
	id := protocol.NewStringID("9")
	reply := m.Serve(context.Background(), contracts.Caller{Kind: contracts.CallerCLI},
		protocol.Message{Method: protocol.MethodSettingsList, ID: &id}, nil)
	if reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("reply = %+v", reply.Error)
	}
	if idOf(reply) != `"9"` {
		t.Fatalf("id = %s", idOf(reply))
	}
}

func TestMuxRegistrationPanics(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		h      contracts.Handler
		setup  func(m *Mux)
	}{
		{"nil handler", "state", nil, nil},
		{"empty prefix", "", &recordingHandler{}, nil},
		{"dotted prefix", "state.deep", &recordingHandler{}, nil},
		{"duplicate prefix", "state", &recordingHandler{}, func(m *Mux) { m.Register("state", &recordingHandler{}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want panic")
				}
			}()
			m := NewMux()
			if tc.setup != nil {
				tc.setup(m)
			}
			m.Register(tc.prefix, tc.h)
		})
	}
}

func TestMuxNotificationShape(t *testing.T) {
	// A notification (no id) served through the Mux still gets a reply
	// message; the Server drops its result, so the shape stays uniform.
	h := &recordingHandler{}
	m := NewMux()
	m.Register("event", h)
	reply := m.Serve(context.Background(), contracts.Caller{Kind: contracts.CallerPlugin},
		protocol.Message{Method: "event.emit"}, nil)
	if reply.ID != nil {
		t.Fatalf("notification reply has id %v", reply.ID)
	}
	var v any
	if err := json.Unmarshal(reply.Result, &v); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
}

func TestMuxPassesNotifier(t *testing.T) {
	var got contracts.Notifier
	h := captureNotifier{&got}
	m := NewMux()
	m.Register("state", h)
	n := notifierFunc(func(protocol.Message) error { return nil })
	m.Serve(context.Background(), contracts.Caller{Kind: contracts.CallerCLI},
		protocol.Message{Method: protocol.MethodStateWatch}, n)
	if got == nil {
		t.Fatal("notifier not passed through")
	}
}

type notifierFunc func(protocol.Message) error

func (f notifierFunc) Notify(m protocol.Message) error { return f(m) }

// captureNotifier records the notifier it is served with.
type captureNotifier struct{ got *contracts.Notifier }

func (h captureNotifier) Serve(_ context.Context, _ contracts.Caller, _ protocol.Message, n contracts.Notifier) protocol.Message {
	*h.got = n
	return protocol.Message{Result: json.RawMessage("{}")}
}
