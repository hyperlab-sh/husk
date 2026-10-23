package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestFullCaptureModalCandidates(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{
		store.LayerDistribution: bind(`{"key":"d","action":"husk.modal.close"}`, `{"key":"s","focus":"modal-surface","action":"d.keep"}`),
		store.LayerUser:         bind(`{"key":"u","action":"husk.modal.close"}`, `{"key":"x","focus":"modal-surface","action":"husk.modal.close"}`),
		store.LayerProfile:      bind(`{"key":"p","focus":"modal-surface","action":"husk.modal.close"}`),
	}, bind(`{"key":"l","focus":"modal-surface","action":"husk.modal.close"}`))
	ctx := keybinding.KeyContext{FocusSurface: "modal-surface", FocusOwner: "a", ModalOpen: true, Capture: protocol.CaptureAll}
	for _, key := range []string{"esc", "d", "u", "x", "p", "l", "q", "ctrl+c", "tab", "alt+1"} {
		if got := resolved(kb, ctx, key); got != "<none>" {
			t.Errorf("full capture %q = %s", key, got)
		}
	}
	if got := resolved(kb, ctx, "ctrl+]"); got != "husk.capture.release" {
		t.Fatalf("modal release = %s", got)
	}
	if got := resolved(kb, ctx, "s"); got != "d.keep" {
		t.Fatalf("scoped candidate = %s", got)
	}
	ctx.Capture = protocol.CaptureNone
	if got := resolved(kb, ctx, "esc"); got != "husk.modal.close" {
		t.Fatalf("released Escape = %s", got)
	}
}
