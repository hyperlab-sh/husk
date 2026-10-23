package host

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestFrontendWritersRefuseShutdownSpawn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(*Root, func(error))
	}{
		{"SetSetting", func(r *Root, reply func(error)) {
			r.SetSetting("missing.key", true, func(_ contracts.SettingSetResult, err error) { reply(err) })
		}},
		{"UseProfile", func(r *Root, reply func(error)) {
			r.UseProfile("missing", func(_ contracts.ProfileUseResult, err error) { reply(err) })
		}},
		{"RemovePlugin", func(r *Root, reply func(error)) {
			r.RemovePlugin("missing", func(_ contracts.PluginRemoval, err error) { reply(err) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := boot(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			var replies atomic.Int32
			h.loop(func() {
				// Model the shutdown mark without closing the harness's executor.
				h.root.mu.Lock()
				h.root.closing = true
				h.root.mu.Unlock()
				h.root.endClosingCtx()
				tc.start(h.root, func(error) { replies.Add(1) })
			})
			h.root.wg.Wait()
			h.loop(func() {})
			if replies.Load() != 0 {
				t.Fatalf("refused spawn delivered %d replies", replies.Load())
			}
		})
	}
}

func checkFrontendCancelledSuccess[T any](t *testing.T, success T) {
	t.Helper()
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := awaitFrontend(t, h, func(reply func(T, error)) {
		frontendWrite(h.root, func(ctx context.Context) (T, error) {
			// An admitted worker can finish successfully just as shutdown cancels it.
			h.root.endClosingCtx()
			<-ctx.Done()
			return success, nil
		}, reply)
	})
	data, ok := err.(contracts.DataError)
	if contracts.CodeOf(err) != string(protocol.CodeCancelled) || !ok || data.Data()["reason"] != "shutdown" {
		t.Fatalf("reply error = %v; want cancelled/shutdown", err)
	}
	if !reflect.ValueOf(result).IsZero() {
		t.Fatalf("cancelled reply retained result: %+v", result)
	}
}

func TestFrontendWriteShutdownZerosSuccessfulResult(t *testing.T) {
	t.Run("setting", func(t *testing.T) {
		checkFrontendCancelledSuccess(t, contracts.SettingSetResult{Key: "theme.name", Value: "selected", Source: "user"})
	})
	t.Run("profile", func(t *testing.T) { checkFrontendCancelledSuccess(t, contracts.ProfileUseResult{Active: "work"}) })
	t.Run("removal", func(t *testing.T) {
		checkFrontendCancelledSuccess(t, contracts.PluginRemoval{Plugin: "alpha", Status: contracts.PodExited})
	})
}

func TestFrontendWriteDropsReplyAtClosedPostGate(t *testing.T) {
	var closed atomic.Bool
	var refused atomic.Int32
	h, err := boot(t, func(c *contracts.Config) {
		post := c.Post
		c.Post = func(msg any) bool {
			if closed.Load() {
				refused.Add(1)
				return false
			}
			return post(msg)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closed.Store(false)
	entered, release := make(chan struct{}), make(chan struct{})
	var replies atomic.Int32
	h.loop(func() {
		frontendWrite(h.root, func(context.Context) (string, error) {
			close(entered)
			<-release
			return "completed", nil
		}, func(string, error) { replies.Add(1) })
	})
	<-entered
	h.root.endClosingCtx()
	closed.Store(true)
	close(release)
	h.root.wg.Wait()
	closed.Store(false)
	h.loop(func() {})
	if replies.Load() != 0 || refused.Load() != 1 {
		t.Fatalf("replies=%d refused posts=%d", replies.Load(), refused.Load())
	}
}
