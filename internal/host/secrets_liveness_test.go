package host

import (
	"context"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The requesting context deliberately stays open. Only the husk.pods flush,
// not connection cancellation or a declaration reload, can drop this read.
func TestPodLeavingLiveDropsSecretDialog(t *testing.T) {
	alpha := writePlugin(t, "alpha", false)
	f := newFakes()
	shown := make(chan secrets.Prompt, 2)
	answers := make(chan secrets.Answer)
	closed := make(chan struct{}, 2)
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{alpha} },
		withHook(hook), WithFactory(f.factory),
		func(o *options) { o.keyring = &memRing{m: map[string]string{}} },
		WithPrompt(func(ctx context.Context, p secrets.Prompt) secrets.Answer {
			shown <- p
			select {
			case a := <-answers:
				return a
			case <-ctx.Done():
				closed <- struct{}{}
				return secrets.Dismissed
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	if got := h.podStatus("alpha"); got != "live" {
		t.Fatalf("initial Pod status = %q", got)
	}
	h.root.store.Declare(store.DeclaredPlugins, "alpha", map[string]any{
		"id": "alpha", "name": "Alpha",
		"secret_requests": []any{map[string]any{"name": "tok.en", "purpose": "Read token"}},
	})
	if _, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonPlugin); err != nil {
		t.Fatal(err)
	}
	var watcher recNotifier
	h.loop(func() {
		if _, err := h.root.bus.Subscribe(&watcher, "secrets.read"); err != nil {
			t.Error(err)
		}
	})
	read := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := h.root.rt.Secrets.Read(t.Context(), secrets.Caller{Kind: protocol.CallerPlugin, PluginID: "alpha"}, "tok.en")
			done <- err
		}()
		return done
	}
	done := read()
	<-shown
	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Failed, LastError: "crashed"}})
	h.loop(func() {}) // finish the liveness tick, with the declaration unchanged
	if err := <-done; failureOf(t, err) != "cancelled/plugin_exited" {
		t.Fatalf("departed Pod read = %v", err)
	}
	<-closed
	h.loop(func() {}) // any wrongly-published answer would have flushed by now
	watcher.mu.Lock()
	n := len(watcher.msgs)
	watcher.mu.Unlock()
	if n != 0 {
		t.Fatalf("dropped read published %d events", n)
	}
	// No remembered refusal: if the caller can read again, it must prompt.
	done = read()
	if p := <-shown; p.Plugin != "alpha" {
		t.Fatalf("next prompt = %+v", p)
	}
	answers <- secrets.Dismissed
	if err := <-done; failureOf(t, err) != "denied/user_denied" {
		t.Fatalf("next read = %v", err)
	}
}
