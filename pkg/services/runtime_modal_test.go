package services

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestRuntimeModalManifestScopeFollowsOwner(t *testing.T) {
	kb := focusKeys(t,
		bind(`{"key":"r","focus":"modal-surface","action":"a.run"}`, `{"key":"d","focus":"a.popup","action":"a.run"}`),
		bind(`{"key":"r","focus":"modal-surface","action":"b.run"}`))
	for _, tc := range []struct {
		name, surface, owner, want, exact string
		modal                             bool
	}{
		{"a_runtime", "a.popup", "a", "a.run", "a.run", true},
		{"a_other_runtime", "a.second", "a", "a.run", "<none>", true},
		{"b_runtime", "b.popup", "b", "b.run", "<none>", true},
		{"a_rail", "modal-surface", "a", "a.run", "<none>", true},
		{"foreign_owner", "c.popup", "c", "<none>", "<none>", true},
		{"no_owner", "c.popup", "", "<none>", "<none>", true},
		{"non_modal", "main-stage", "a", "<none>", "<none>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := keybinding.KeyContext{FocusSurface: tc.surface, FocusOwner: tc.owner, ModalOpen: tc.modal}
			if got := resolved(kb, ctx, "r"); got != tc.want {
				t.Fatalf("modal-scoped manifest binding=%s want %s", got, tc.want)
			}
			if got := resolved(kb, ctx, "d"); got != tc.exact {
				t.Fatalf("dynamic modal scope=%s want %s", got, tc.exact)
			}
			if tc.modal {
				if got := resolved(kb, ctx, "esc"); got != "husk.modal.close" {
					t.Fatalf("open modal Escape=%s", got)
				}
				ctx.Capture = protocol.CaptureAll
				if got := resolved(kb, ctx, "r"); got != tc.want {
					t.Fatalf("captured modal scope=%s want %s", got, tc.want)
				}
			}
		})
	}
}

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
