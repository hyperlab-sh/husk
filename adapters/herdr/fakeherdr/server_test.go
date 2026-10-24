package fakeherdr

import (
	"errors"
	"net"
	"testing"
)

type failedWriter struct {
	net.Conn
	err    error
	writes int
}

func (w *failedWriter) Write([]byte) (int, error) { w.writes++; return 0, w.err }

func TestEmitAttemptsEverySubscriberAndJoinsErrors(t *testing.T) {
	firstErr, secondErr := errors.New("first"), errors.New("second")
	first := &failedWriter{err: firstErr}
	second := &failedWriter{err: secondErr}
	s := &Server{peers: map[*peer]bool{
		{conn: first, names: map[string]bool{"pane.focused": true}}:  true,
		{conn: second, names: map[string]bool{"pane.focused": true}}: true,
	}}
	err := s.Emit("pane_focused", map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"})
	if first.writes != 1 || second.writes != 1 || !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("writes=(%d,%d), joined error=%v", first.writes, second.writes, err)
	}
}
