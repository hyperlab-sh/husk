package host

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestUnknownMethodNotificationIsSilentOnTheRealSocket drives the real
// handler chain (Mux, Admit) over the socket: a notification for an unknown
// method inside a registered prefix draws no line and one debug log; the same
// method as a request draws unknown_method.
func TestUnknownMethodNotificationIsSilentOnTheRealSocket(t *testing.T) {
	type entry struct{ level, msg string }
	logs := make(chan entry, 16)
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook), WithLogger(func(level, msg string) {
		select {
		case logs <- entry{level, msg}:
		default:
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	cancel, _ := runHost(t, ready, h)
	defer cancel()
	c, err := net.Dial("unix", instance.SocketPath(h.dir, h.id))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	send := func(line string) {
		t.Helper()
		if _, err := c.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	read := func() protocol.Message {
		t.Helper()
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m protocol.Message
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return m
	}

	send(`{"method":"state.nope"}`)
	// A protocol.error for the notification would be the next line on this
	// connection, so the ping reply and the later reply must each be the
	// first line read after their request.
	send(`{"id":1,"method":"session.ping"}`)
	if m := read(); m.ID == nil || m.ID.String() != "1" || m.Error != nil {
		t.Fatal("unexpected wire message:", m)
	}
	for {
		var e entry
		select {
		case e = <-logs:
		case <-t.Context().Done():
			t.Fatal("no debug log for the ignored notification")
		}
		if strings.Contains(e.msg, "state.nope") {
			if e.level != "debug" {
				t.Fatalf("level %q: %s", e.level, e.msg)
			}
			break
		}
	}

	send(`{"id":2,"method":"state.nope"}`)
	if m := read(); m.ID == nil || m.ID.String() != "2" || m.Error == nil || m.Error.Code != protocol.CodeUnknownMethod {
		t.Fatal(m)
	}
}
