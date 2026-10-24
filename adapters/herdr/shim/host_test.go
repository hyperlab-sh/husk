package shim

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Hide cancelCtx's private Value so context.AfterFunc uses this observable
// registration seam. Assertions need no scheduler-dependent cancellation wait.
type trackedContext struct {
	context.Context
	mu     sync.Mutex
	active int
}

func (*trackedContext) Value(any) any { return nil }
func (c *trackedContext) AfterFunc(f func()) func() bool {
	c.mu.Lock()
	c.active++
	c.mu.Unlock()
	stop := context.AfterFunc(c.Context, f)
	return func() bool {
		if !stop() {
			return false
		}
		c.mu.Lock()
		c.active--
		c.mu.Unlock()
		return true
	}
}

func TestNotificationConnectionOutlivesRequestContext(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hn-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "h.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	h := &SocketHost{Path: listener.Addr().String(), Token: "fixture"}
	defer h.Close()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &trackedContext{Context: base}
	if err := h.Notify(ctx, protocol.MethodToastShow, map[string]any{"message": "first"}); err != nil {
		t.Fatal(err)
	}
	ctx.mu.Lock()
	active := ctx.active
	ctx.mu.Unlock()
	if active != 0 {
		t.Fatal("completed notification retained a caller-context cancellation callback")
	}
	original := h.notifications
	cancel()
	server := take(t, accepted)
	defer server.Close()
	// Delivery is deliberately delayed until after the caller cancels.
	decoded := make(chan protocol.Message, 2)
	go func() {
		decoder := json.NewDecoder(server)
		for range 2 {
			var m protocol.Message
			if decoder.Decode(&m) != nil {
				return
			}
			decoded <- m
		}
	}()
	if err := h.Notify(context.Background(), protocol.MethodToastShow, map[string]any{"message": "second"}); err != nil {
		t.Fatal(err)
	}
	if h.notifications != original {
		t.Fatal("cancellation replaced the shared notification connection")
	}
	for _, want := range []string{"first", "second"} {
		m := take(t, decoded)
		var params struct{ Message string }
		_ = json.Unmarshal(m.Params, &params)
		if m.Method != protocol.MethodToastShow || params.Message != want || !m.HasToken() {
			t.Fatal("notification was not delivered in order on the original connection")
		}
	}
}
