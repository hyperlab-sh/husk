//go:build linux || darwin

package server

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type recordingLegacy struct{ calls chan string }

func newRecordingLegacy() *recordingLegacy      { return &recordingLegacy{calls: make(chan string, 8)} }
func (l *recordingLegacy) FailLegacy(id string) { l.calls <- id }

// noticeLog collects the Notices the Server posts to the loop.
type noticeLog struct {
	mu      sync.Mutex
	notices []Notice
}

func (n *noticeLog) post(v any) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if notice, ok := v.(Notice); ok {
		n.notices = append(n.notices, notice)
	}
	return true
}
func (n *noticeLog) all() []Notice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Notice(nil), n.notices...)
}

func legacyServer(t *testing.T) (*SocketServer, *recordingLegacy, *noticeLog, *int) {
	t.Helper()
	s := inertServer(t)
	legacy, notices := newRecordingLegacy(), &noticeLog{}
	s.cfg.Legacy = legacy
	s.cfg.Post = notices.post
	var mu sync.Mutex
	handled := new(int)
	s.Handle("sample", handlerFunc(func(_ context.Context, _ Caller, m protocol.Message, _ Notifier) protocol.Message {
		mu.Lock()
		*handled++
		mu.Unlock()
		return protocol.Message{ID: m.ID, Result: []byte(`{}`)}
	}))
	return s, legacy, notices, handled
}

func TestFirstLineMajorOneHelloIsAnsweredAndClosed(t *testing.T) {
	for _, tc := range []struct {
		name, line, id, logged string
	}{
		{"names a plugin", `{"type":"hello","hello":{"plugin_id":"alpha","name":"fake","protocol_version":"1.0"}}`, "alpha", "plugin alpha spoke protocol major 1 on the socket"},
		{"no hello object", `{"type":"hello"}`, "", "plugin unknown plugin spoke protocol major 1 on the socket"},
		{"carries a token", `{"type":"hello","token":"x","hello":{"plugin_id":"beta"}}`, "beta", "plugin beta spoke protocol major 1 on the socket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, legacy, notices, handled := legacyServer(t)
			start(t, s)
			c, r := dial(t, s)
			write(t, c, tc.line)
			got, err := r.ReadBytes('\n')
			if err != nil || string(got) != string(protocol.LegacyRejectionLine()) {
				t.Fatalf("reply %q, %v; want the major-1 line", got, err)
			}
			if rest, err := io.ReadAll(r); err != nil || len(rest) != 0 {
				t.Fatalf("after the line: %q, %v; want EOF", rest, err)
			}
			if id := testclock.Receive(t, legacy.calls); id != tc.id {
				t.Fatalf("FailLegacy(%q), want %q", id, tc.id)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case id := <-legacy.calls:
				t.Fatalf("FailLegacy called twice (second %q)", id)
			default:
			}
			var warns int
			for _, n := range notices.all() {
				if n.Count != 0 || n.LastCode != "" {
					t.Fatalf("hello counted as a diagnostic: %+v", n)
				}
				if n.Level == "warn" && n.Message == tc.logged {
					warns++
				}
			}
			if warns != 1 || len(notices.all()) != 1 || *handled != 0 {
				t.Fatalf("notices %+v handled %d; want exactly the one warn %q", notices.all(), *handled, tc.logged)
			}
		})
	}
}

func TestMajorOneHelloAfterTheFirstLineIsInvalidMessage(t *testing.T) {
	s, legacy, _, _ := legacyServer(t)
	start(t, s)
	c, r := dial(t, s)
	write(t, c, `{"id":1,"method":"session.ping"}`)
	if m := read(t, r); m.Result == nil {
		t.Fatal(m)
	}
	write(t, c, `{"type":"hello","hello":{"plugin_id":"alpha"}}`)
	m := read(t, r)
	var p protocol.ProtocolErrorParams
	if m.Method != protocol.MethodProtocolError || m.Params == nil {
		t.Fatalf("got %+v, want protocol.error", m)
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Code != protocol.CodeInvalidMessage {
		t.Fatalf("params %+v, %v; want invalid_message", p, err)
	}
	write(t, c, `{"id":2,"method":"sample.go"}`) // the connection is still open
	if m := read(t, r); m.Error != nil || m.Result == nil {
		t.Fatalf("connection did not stay open: %+v", m)
	}
	select {
	case id := <-legacy.calls:
		t.Fatalf("FailLegacy(%q) for a later-line hello", id)
	default:
	}
}

func TestFirstLineRequestsAndBadLinesStillWork(t *testing.T) {
	for _, tc := range []struct{ name, first string }{
		{"handler request", `{"id":1,"method":"sample.go"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, legacy, _, handled := legacyServer(t)
			start(t, s)
			c, r := dial(t, s)
			write(t, c, tc.first)
			if m := read(t, r); m.Error != nil || m.Result == nil {
				t.Fatalf("first-line request failed: %+v", m)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if *handled != 1 || len(legacy.calls) != 0 {
				t.Fatalf("handled %d, FailLegacy calls %d", *handled, len(legacy.calls))
			}
		})
	}
	t.Run("blank lines before the hello", func(t *testing.T) {
		s, legacy, _, _ := legacyServer(t)
		start(t, s)
		c, r := dial(t, s)
		write(t, c, "")
		write(t, c, `{"type":"hello","hello":{"plugin_id":"alpha"}}`)
		if got, err := r.ReadString('\n'); err != nil || !strings.Contains(got, `"protocol_error"`) {
			t.Fatalf("%q, %v", got, err)
		}
		if id := testclock.Receive(t, legacy.calls); id != "alpha" {
			t.Fatal(id)
		}
	})
	t.Run("garbage first line, hello second", func(t *testing.T) {
		s, legacy, _, _ := legacyServer(t)
		start(t, s)
		c, r := dial(t, s)
		write(t, c, `not json`)
		if m := read(t, r); m.Method != protocol.MethodProtocolError {
			t.Fatal(m)
		}
		write(t, c, `{"type":"hello","hello":{"plugin_id":"alpha"}}`)
		if m := read(t, r); m.Method != protocol.MethodProtocolError {
			t.Fatalf("later hello was not invalid_message: %+v", m)
		}
		if len(legacy.calls) != 0 {
			t.Fatal("FailLegacy for a hello that was not the first line")
		}
	})
}
