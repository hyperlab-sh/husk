package guide

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type failedSend struct {
	methods []string
}

func (w *failedSend) Write(b []byte) (int, error) {
	var method string
	_ = protocol.ReadLoop(strings.NewReader(string(b)), func(m protocol.Message) { method = m.Method }, nil)
	w.methods = append(w.methods, method)
	return 0, io.ErrClosedPipe
}

func toggleRequest(n int64) protocol.Message {
	id, _ := protocol.NewIntID(n)
	return protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: raw(map[string]string{"action": toggleAction})}
}

func TestToggleOffRepliesBeforeSendError(t *testing.T) {
	var out strings.Builder
	socket := &failedSend{}
	p := &plugin{shown: true, dirty: true, socket: protocol.NewWriter(socket), stdout: protocol.NewWriter(&out), pending: make(map[string]pending)}
	err := p.fromHost(toggleRequest(1))
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("send error = %v", err)
	}
	if p.shown || p.dirty {
		t.Fatal("toggle-off did not finish local state")
	}
	var replies []protocol.Message
	_ = protocol.ReadLoop(strings.NewReader(out.String()), func(m protocol.Message) { replies = append(replies, m) }, nil)
	if len(replies) != 1 || string(replies[0].Result) != `{"shown":false}` {
		t.Fatalf("toggle-off reply missing before error: %s", out.String())
	}
	if got := strings.Join(socket.methods, ","); got != "surface.update,state.set" {
		t.Fatalf("send order = %s", got)
	}
}

func TestHostBatchFinishesAfterSendFailure(t *testing.T) {
	var out strings.Builder
	p := &plugin{shown: true, socket: protocol.NewWriter(&failedSend{}), stdout: protocol.NewWriter(&out), pending: make(map[string]pending), log: io.Discard}
	palette := testPalette()
	err := p.hostBatch([]protocol.Message{
		toggleRequest(1),
		{Method: protocol.MethodThemeChanged, Params: raw(protocol.ThemeChangedParams{Palette: palette})},
		{ID: toggleRequest(2).ID, Method: "unknown.method"},
	})
	if !errors.Is(err, io.ErrClosedPipe) || p.palette != palette || !p.dirty || p.shown {
		t.Fatalf("batch did not finish: err=%v palette=%+v dirty=%t shown=%t", err, p.palette, p.dirty, p.shown)
	}
	var replies []protocol.Message
	_ = protocol.ReadLoop(strings.NewReader(out.String()), func(m protocol.Message) { replies = append(replies, m) }, nil)
	if len(replies) != 2 || replies[1].ID.String() != "2" || replies[1].Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("batch lost replies: %s", out.String())
	}
}

func TestFlushRepliesAfterStateSendFailure(t *testing.T) {
	var out strings.Builder
	req := toggleRequest(1)
	p := &plugin{shown: true, baseline: true, keysReady: true, layout: fixture(), toggle: &req, socket: protocol.NewWriter(&failedSend{}), stdout: protocol.NewWriter(&out), pending: make(map[string]pending)}
	err := p.flush()
	if !errors.Is(err, io.ErrClosedPipe) || p.toggle != nil || !strings.Contains(out.String(), `"shown":true`) {
		t.Fatalf("pending toggle reply lost: err=%v reply=%s", err, out.String())
	}
}

// scriptedSocket decouples writes from reads without network or real timers.
type scriptedSocket struct {
	*io.PipeReader
	writes chan protocol.Message
}

func (s *scriptedSocket) Write(b []byte) (int, error) {
	_ = protocol.ReadLoop(strings.NewReader(string(b)), func(m protocol.Message) { s.writes <- m }, nil)
	return len(b), nil
}

func TestReconnectBackoffAndReset(t *testing.T) {
	for _, mode := range []string{"refused", "malformed", "invalid-slice", "dial-error", "varying-errors"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			in, input := io.Pipe()
			dials := make(chan io.ReadWriteCloser)
			waits := make(chan time.Duration)
			advance := make(chan struct{})
			done := make(chan error, 1)
			logs := &lockedLog{}
			failedDials := 0
			cfg := config{token: "test-token", dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
				if mode == "dial-error" && failedDials < 8 {
					failedDials++
					return nil, errors.New("injected dial failure")
				}
				select {
				case c := <-dials:
					return c, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}, retry: func(ctx context.Context, delay time.Duration) error {
				select {
				case waits <- delay:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case <-advance:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			go func() { done <- run(ctx, cfg, in, io.Discard, logs) }()
			t.Cleanup(func() { cancel(); _ = input.Close(); receive(t, done) })
			connect := func() (*io.PipeWriter, protocol.Message, *scriptedSocket) {
				r, w := io.Pipe()
				s := &scriptedSocket{r, make(chan protocol.Message, 16)}
				select {
				case dials <- s:
				case <-time.After(5 * time.Second):
					t.Fatal("dial did not follow injected retry")
				}
				watch := receive(t, s.writes)
				if watch.Method != protocol.MethodStateWatch || receive(t, s.writes).Method != protocol.MethodKeysList {
					t.Fatal("missing watch and keys requests")
				}
				return w, watch, s
			}
			delays := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 5 * time.Second, 5 * time.Second}
			if mode == "varying-errors" {
				for len(delays) < 20 {
					delays = append(delays, 5*time.Second)
				}
			}
			for i, expected := range delays {
				if mode != "dial-error" {
					w, watch, _ := connect()
					reply := protocol.Message{ID: watch.ID}
					switch mode {
					case "refused":
						reply.Error = &protocol.Error{Code: protocol.CodeDenied, Message: "injected watch refusal"}
					case "malformed":
						reply.Result = raw([]int{1})
					case "invalid-slice":
						reply.Result = raw(map[string]any{"slices": []sliceValue{{"husk.geometry", raw("invalid geometry")}}})
					case "varying-errors":
						reply.Error = &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("injected refusal %d", i)}
					}
					if err := protocol.NewWriter(w).Send(reply); err != nil {
						t.Fatal(err)
					}
				}
				if got := receive(t, waits); got != expected {
					t.Fatalf("retry = %s, want %s", got, expected)
				}
				advance <- struct{}{}
			}
			wantLogs := 1
			if mode == "varying-errors" {
				wantLogs = 17 // Sixteen distinct errors and one suppression notice.
				if strings.Count(logs.String(), "further errors suppressed") != 1 {
					t.Fatalf("missing suppression notice: %s", logs.String())
				}
			}
			if count := strings.Count(logs.String(), "guide:"); count != wantLogs {
				t.Fatalf("persistent failure logged %d times: %s", count, logs.String())
			}
			if mode == "dial-error" && strings.Count(logs.String(), "connect:") != 1 {
				t.Fatalf("dial failure not deduplicated: %s", logs.String())
			}
			w, watch, s := connect()
			// The following pods change is a barrier proving the baseline applied.
			batch := string(raw(protocol.Message{ID: watch.ID, Result: raw(map[string]any{"slices": []sliceValue{}})})) + "\n" + string(raw(changed("husk.pods", map[string]any{}))) + "\n"
			if _, err := io.WriteString(w, batch); err != nil {
				t.Fatal(err)
			}
			if receive(t, s.writes).Method != protocol.MethodKeysList {
				t.Fatal("baseline not applied")
			}
			_ = w.Close()
			if got := receive(t, waits); got != 100*time.Millisecond {
				t.Fatalf("retry after valid baseline = %s", got)
			}
			if strings.Count(logs.String(), "connection recovered") != 1 {
				t.Fatalf("missing recovery diagnostic: %s", logs.String())
			}
		})
	}
}
