//go:build linux || darwin

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestOrderedNotificationsAreBoundedWithoutBlockingReader(t *testing.T) {
	s := inertServer(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	order := make(chan int, MaxNotificationQueue)
	s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, m protocol.Message, _ Notifier) protocol.Message {
		var p struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.N == 0 {
			entered <- struct{}{}
			select {
			case <-ctx.Done():
				return protocol.Message{}
			case <-release:
			}
		}
		order <- p.N
		return protocol.Message{}
	}))
	start(t, s)
	c, r := dial(t, s)
	for i := 0; i < MaxNotificationQueue; i++ {
		write(t, c, fmt.Sprintf(`{"method":"sample.notice","params":{"n":%d}}`, i))
	}
	testclock.Receive(t, entered)
	write(t, c, `{"method":"sample.notice","params":{"n":64}}`)
	m := read(t, r)
	var p protocol.ProtocolErrorParams
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Code != protocol.CodeBusy || string(p.Data) != `{"reason":"too_many_in_flight"}` {
		t.Fatal(m, p, err)
	}
	// A ping also proves the reader is not waiting for the notification worker.
	write(t, c, `{"id":9,"method":"session.ping"}`)
	if m := read(t, r); m.ID.String() != "9" {
		t.Fatal(m)
	}
	close(release)
	for i := 0; i < MaxNotificationQueue; i++ {
		if got := testclock.Receive(t, order); got != i {
			t.Fatalf("order=%d want %d", got, i)
		}
	}
}

func TestNotificationEOFDiscardsQueueAndCancelsActiveContext(t *testing.T) {
	s := inertServer(t)
	started := make(chan *connection, 1)
	cancelled := make(chan struct{}, 1)
	s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, _ protocol.Message, n Notifier) protocol.Message {
		started <- n.(*connection)
		<-ctx.Done()
		cancelled <- struct{}{}
		return protocol.Message{}
	}))
	start(t, s)
	c, _ := dial(t, s)
	for i := 0; i < MaxNotificationQueue; i++ {
		write(t, c, `{"method":"sample.notice"}`)
	}
	stream := testclock.Receive(t, started)
	c.Close()
	testclock.Receive(t, cancelled)
	testclock.Receive(t, stream.notifyDone)
	select {
	case <-started:
		t.Fatal("notification queue ran after EOF")
	default:
	}
}

func TestSlowSocketReaderDisconnectsOnInjectedTimeoutOrOverflow(t *testing.T) {
	for _, mode := range []string{"timeout", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			s := inertServer(t)
			watch := make(chan *connection, 1)
			s.Handle("sample", handlerFunc(func(ctx context.Context, _ Caller, _ protocol.Message, n Notifier) protocol.Message {
				watch <- n.(*connection)
				<-ctx.Done()
				return protocol.Message{Result: json.RawMessage(`{}`)}
			}))
			start(t, s)
			c, _ := dial(t, s)
			write(t, c, `{"id":1,"method":"sample.watch"}`)
			stream := testclock.Receive(t, watch)
			if err := stream.raw.SetWriteBuffer(1024); err != nil {
				t.Fatal(err)
			}
			large := protocol.Message{Method: protocol.MethodStateChanged, Params: json.RawMessage(`{"padding":"` + strings.Repeat("x", 1<<20) + `"}`)}
			if err := stream.Notify(large); err != nil {
				t.Fatal(err)
			}
			clock := s.cfg.Clock.(*testclock.Clock)
			clock.Await(t, WriteTimeout)
			if mode == "timeout" {
				clock.Advance(WriteTimeout)
			} else {
				var err error
				for i := 0; i <= QueueCapacity; i++ {
					err = stream.Notify(protocol.Message{Method: protocol.MethodStateChanged, Params: json.RawMessage(`{}`)})
					if err != nil {
						break
					}
				}
				if !errors.Is(err, protocol.ErrSendQueueFull) {
					t.Fatal("queue overflow not bounded", err)
				}
			}
			testclock.Receive(t, stream.ctx.Done())
			testclock.Receive(t, stream.writer.Done())
			if err := stream.Notify(protocol.Message{Method: protocol.MethodStateChanged}); err == nil {
				t.Fatal("closed watch still usable")
			}
		})
	}
}

func TestSocketProtocolErrorBudgetFlushesSuppressedRejections(t *testing.T) {
	s := inertServer(t)
	notices := make(chan Notice, 64)
	s.cfg.Post = func(v any) bool {
		select {
		case notices <- v.(Notice):
			return true
		case <-t.Context().Done():
			return false
		}
	}
	start(t, s)
	c, r := dial(t, s)
	for i := 0; i < 12; i++ {
		write(t, c, "not-json")
	}
	write(t, c, `{"id":9,"method":"session.ping"}`)
	for i := 0; i < protocol.MaxProtocolErrorsPerSecond; i++ {
		if m := read(t, r); m.Method != protocol.MethodProtocolError {
			t.Fatal(m)
		}
	}
	if m := read(t, r); m.ID.String() != "9" {
		t.Fatal("error budget exceeded", m)
	}
	c.Close()
	for {
		notice := testclock.Receive(t, notices)
		if strings.Contains(notice.Message, "2 rejections suppressed") {
			break
		}
	}
}
