//go:build linux || darwin

package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestCloseCancelsWithoutWaitingForOwnerCooperation(t *testing.T) {
	s := inertServer(t)
	clk := s.cfg.Clock.(*testclock.Clock)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, _ protocol.Message, _ Notifier) protocol.Message {
		entered <- ctx
		testclock.Receive(t, release)
		defer close(finished)
		return protocol.Message{Result: json.RawMessage(`{}`)}
	}))
	start(t, s)
	c, _ := dial(t, s)
	write(t, c, `{"id":1,"method":"sample.wait"}`)
	ctx := testclock.Receive(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	// A handler that never returns holds Close only for the drain bound.
	clk.Await(t, WriteTimeout)
	clk.Advance(WriteTimeout)
	if err := testclock.Receive(t, closed); err != nil {
		t.Fatal(err)
	}
	testclock.Receive(t, ctx.Done())
	close(release)
	testclock.Receive(t, finished)
}

// A reply produced by a handler that is still running when Close starts is
// flushed before the stream closes: this is how `husk action invoke
// husk.quit` gets its reply.
func TestCloseFlushesReplyOfRunningHandler(t *testing.T) {
	s := inertServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	s.Handle("sample", handlerFunc(func(context.Context, Caller, protocol.Message, Notifier) protocol.Message {
		close(entered)
		testclock.Receive(t, release)
		return protocol.Message{Result: json.RawMessage(`{"ok":true}`)}
	}))
	start(t, s)
	c, r := dial(t, s)
	write(t, c, `{"id":1,"method":"sample.wait"}`)
	testclock.Receive(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	close(release)
	if err := testclock.Receive(t, closed); err != nil {
		t.Fatal(err)
	}
	m := read(t, r)
	if m.ID.String() != "1" || m.Error != nil || string(m.Result) != `{"ok":true}` {
		t.Fatalf("reply = %+v", m)
	}
}

func TestConnectionCapRejectsNewestBeforeReading(t *testing.T) {
	s := inertServer(t)
	// Model occupied admission slots without opening hundreds of idle FDs.
	occupied := make([]*connection, MaxConnections)
	for i := range occupied {
		occupied[i] = &connection{}
		s.conns[occupied[i]] = struct{}{}
	}
	t.Cleanup(func() {
		s.mu.Lock()
		for _, c := range occupied {
			delete(s.conns, c)
		}
		s.mu.Unlock()
	})
	start(t, s)
	_, r := dial(t, s)
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("over-cap connection was served")
	}
}

func TestNonSocketClaimAndUnsafeSymlinkAreNeverRemoved(t *testing.T) {
	s := inertServer(t)
	if err := os.WriteFile(s.SocketPath(), []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("regular file was replaced")
	}
	data, err := os.ReadFile(s.SocketPath())
	if err != nil || string(data) != "user file" {
		t.Fatal("foreign claim removed", err)
	}
	symlink := inertServer(t)
	path := symlink.cfg.RuntimeDir + "-link"
	if err := os.Symlink(symlink.cfg.RuntimeDir, path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	symlink.cfg.RuntimeDir = path
	if err := symlink.Start(); err == nil {
		t.Fatal("symlink runtime root accepted")
	}
}

func TestCloseDoesNotRemoveReplacementSocketOrEqualMetadata(t *testing.T) {
	s := inertServer(t)
	start(t, s)
	if err := os.Rename(s.SocketPath(), s.SocketPath()+"-previous"); err != nil {
		t.Fatal(err)
	}
	replacement := New(s.cfg)
	t.Cleanup(func() {
		if err := replacement.Close(); err != nil {
			t.Error(err)
		}
	})
	start(t, replacement)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	c, r := dial(t, replacement)
	write(t, c, `{"id":1,"method":"session.ping"}`)
	if m := read(t, r); m.ID.String() != "1" {
		t.Fatal("replacement socket lost")
	}
	entries, err := instance.List(s.cfg.RuntimeDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("replacement metadata removed", entries, err)
	}
}

func TestInvalidHandlerReplyBecomesOneInternalReply(t *testing.T) {
	for _, mode := range []string{"absent", "both", "oversize", "wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			s := inertServer(t)
			s.Handle("sample", handlerFunc(func(context.Context, Caller, protocol.Message, Notifier) protocol.Message {
				switch mode {
				case "absent":
					return protocol.Message{}
				case "both":
					return protocol.Message{Result: json.RawMessage(`{}`), Error: &protocol.Error{Code: protocol.CodeFailed, Message: "x"}}
				case "oversize":
					return protocol.Message{Result: json.RawMessage(`"` + strings.Repeat("x", protocol.MaxLineBytes) + `"`)}
				}
				id := protocol.NewStringID("wrong")
				return protocol.Message{ID: &id, Result: json.RawMessage(`{}`)}
			}))
			start(t, s)
			c, r := dial(t, s)
			write(t, c, `{"id":7,"method":"sample.echo"}`)
			m := read(t, r)
			if m.ID.String() != "7" {
				t.Fatal("handler spoofed the reply id")
			}
			if mode != "wrong-id" && (m.Error == nil || m.Error.Code != protocol.CodeInternal) {
				t.Fatal(m)
			}
			write(t, c, `{"id":8,"method":"session.ping"}`)
			if m := read(t, r); m.ID.String() != "8" {
				t.Fatal("more than one reply", m)
			}
		})
	}
}
