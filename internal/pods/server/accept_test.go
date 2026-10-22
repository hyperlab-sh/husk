//go:build linux || darwin

package server

import (
	"net"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
)

// A failure of the listener itself, not a Close, ends the accept loop. The
// server must say so: after that nothing can call in, and a silent exit would
// look like a healthy idle host.
func TestAcceptFailureIsReportedAsWarnNotice(t *testing.T) {
	s := inertServer(t)
	notices := make(chan Notice, 4)
	s.cfg.Post = func(v any) bool { notices <- v.(Notice); return true }
	start(t, s)
	// Break the listener out from under the accept loop; the server was not
	// closed, so this is a failure, not a shutdown.
	if err := s.listener.Close(); err != nil {
		t.Fatal(err)
	}
	n := testclock.Receive(t, notices)
	if n.Level != "warn" || n.ConnID != 0 || n.Count != 0 {
		t.Fatalf("notice = %+v", n)
	}
	for _, want := range []string{"accept failed: ", net.ErrClosed.Error(), "can no longer call in"} {
		if !strings.Contains(n.Message, want) {
			t.Fatalf("message %q lacks %q", n.Message, want)
		}
	}
	// The loop is over: exactly one notice, and Close still completes.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-notices:
		t.Fatalf("second notice %+v", extra)
	default:
	}
}

// An accept error caused by Close is the normal end of the loop and must not
// surface as a failure.
func TestAcceptErrorDuringCloseIsSilent(t *testing.T) {
	s := inertServer(t)
	notices := make(chan Notice, 4)
	s.cfg.Post = func(v any) bool { notices <- v.(Notice); return true }
	start(t, s)
	// Close waits for the accept goroutine, so when it returns the loop has
	// already seen its error and decided whether to post.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notices:
		t.Fatalf("shutdown posted %+v", n)
	default:
	}
}
