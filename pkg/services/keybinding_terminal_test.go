package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestTerminalFullCaptureWithoutModalAndRelease(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{
		store.LayerDistribution: bind(`{"key":"d","action":"d.global"}`, `{"key":"s","focus":"main-stage","action":"d.scoped"}`, `{"key":"z","focus":"main-stage","action":"d.scoped"}`),
		store.LayerUser:         bind(`{"key":"u","action":"u.global"}`, `{"key":"z","focus":"main-stage","disable":true}`),
		store.LayerProfile:      bind(`{"key":"p","action":"p.global"}`),
	}, bind(`{"key":"l","focus":"main-stage","action":"a.local"}`))
	ctx := keybinding.KeyContext{FocusSurface: "main-stage", FocusOwner: "a", Capture: protocol.CaptureAll}
	for _, key := range []string{"f1", "ctrl+p", "esc", "q", "ctrl+c", "tab", "d", "u", "p", "z"} {
		if got := resolved(kb, ctx, key); got != "<none>" {
			t.Errorf("captured %s = %s", key, got)
		}
	}
	for key, want := range map[string]string{"s": "d.scoped", "l": "a.local", "ctrl+]": "husk.capture.release"} {
		if got := resolved(kb, ctx, key); got != want {
			t.Fatalf("scoped/release %s = %s", key, got)
		}
	}
	for _, mode := range []protocol.CaptureMode{protocol.CaptureText, protocol.CaptureNone} {
		ctx.Capture = mode
		if got := resolved(kb, ctx, "ctrl+]"); got != "husk.capture.release" {
			t.Fatalf("release under %q = %s", mode, got)
		}
	}
}
