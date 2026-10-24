package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestRuntimeModalKeyScopeAndCapture(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{store.LayerUser: bind(`{"key":"r","focus":"a.popup","action":"a.run"}`)}, "")
	ctx := keybinding.KeyContext{FocusSurface: "a.popup", FocusOwner: "a", ModalOpen: true}
	if got := resolved(kb, ctx, "esc"); got != "husk.modal.close" {
		t.Fatalf("runtime modal Escape=%s", got)
	}
	if got := resolved(kb, ctx, "r"); got != "a.run" {
		t.Fatalf("runtime-specific key=%s", got)
	}
	ctx.Capture = protocol.CaptureAll
	if got := resolved(kb, ctx, "esc"); got != "<none>" {
		t.Fatalf("full capture escaped modal: %s", got)
	}
	if got := resolved(kb, ctx, "ctrl+]"); got != "husk.capture.release" {
		t.Fatalf("release=%s", got)
	}
}
