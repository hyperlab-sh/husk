package host

import (
	"bufio"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// A socket request whose closure is queued behind the loop step that closes
// the host (the husk.quit shape) must fail at once when shutdown starts: the
// blocked loop will never answer it, and the Server's drain must not wait the
// full write timeout for it.
func TestCloseFromLoopFailsQueuedSocketRequestsAtOnce(t *testing.T) {
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)

	conn, err := net.Dial("unix", h.root.srv.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Hold the loop, then queue the request's closure behind the holder.
	entered := make(chan struct{})
	proceed := make(chan struct{})
	closed := make(chan error, 1)
	h.post(fnMsg{fn: func() {
		close(entered)
		<-proceed
		closed <- h.root.Close()
	}})
	<-entered
	queued := len(h.in)
	if _, err := conn.Write([]byte(`{"id":1,"method":"action.list"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	for len(h.in) <= queued { // the handler has posted its closure behind the holder
		runtime.Gosched()
	}
	close(proceed)

	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second): // well under the 5 s drain bound
		t.Fatal("Close waited for a request the blocked loop cannot answer")
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err := m.UnmarshalJSON(line); err != nil || m.Error == nil || m.Error.Code != protocol.CodeCancelled || string(m.Error.Data) != `{"reason":"shutdown"}` {
		t.Fatalf("reply = %s (%v), want cancelled with reason shutdown", line, err)
	}
}
