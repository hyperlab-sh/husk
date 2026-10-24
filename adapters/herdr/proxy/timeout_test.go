package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

type manualTimer struct {
	mu     sync.Mutex
	active bool
	d      time.Duration
	f      func()
}

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	active := t.active
	t.active = false
	return active
}
func (t *manualTimer) fire() {
	if t.Stop() {
		t.f()
	}
}

type manualClock struct{ timers chan *manualTimer }

func (c *manualClock) AfterFunc(d time.Duration, f func()) upstream.Timer {
	timer := &manualTimer{active: true, d: d, f: f}
	c.timers <- timer
	return timer
}

func TestInterceptTimeoutPreservesPipelinedDispatchOrder(t *testing.T) {
	clock := &manualClock{timers: make(chan *manualTimer, 8)}
	entered := make(chan string, 2)
	s, err := Start(context.Background(), Options{
		Clock: clock,
		Dial:  func(context.Context) (net.Conn, error) { return nil, errors.New("absent") },
		Intercept: func(ctx context.Context, req Request) (any, *Error, bool) {
			entered <- req.ID
			if req.ID == "slow" {
				<-ctx.Done()
				return nil, nil, true
			}
			return map[string]any{"type": "ok"}, nil, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("{\"id\":\"slow\",\"method\":\"relay\",\"params\":{}}\n{\"id\":\"next\",\"method\":\"relay\",\"params\":{}}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-entered:
		if id != "slow" {
			t.Fatal("requests reordered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interceptor did not start")
	}
	select {
	case <-entered:
		t.Fatal("second interceptor overtook the stalled request")
	default:
	}
	select {
	case timer := <-clock.timers:
		if timer.d != 10*time.Second {
			t.Fatalf("intercept bound=%v", timer.d)
		}
		timer.fire()
	case <-time.After(5 * time.Second):
		t.Fatal("interceptor has no bounded timeout")
	}
	decoder := json.NewDecoder(c)
	for _, id := range []string{"slow", "next"} {
		var reply struct {
			ID     string
			Error  *Error
			Result map[string]any
		}
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != id {
			t.Fatalf("reply id=%s want %s", reply.ID, id)
		}
		if id == "slow" && (reply.Error == nil || reply.Error.Code != "timeout") {
			t.Fatal("stalled relay did not return a timeout error")
		}
		if id == "next" && (reply.Error != nil || reply.Result["type"] != "ok") {
			t.Fatal("pipelined request did not resume after the timeout")
		}
	}
}
