package guide

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestEffectiveKeys(t *testing.T) {
	global := binding("action.target", "g")
	scoped := binding("action.target", "s")
	scoped.Focus = protocol.SurfaceModal
	scoped.Source.Layer = "profile"
	higher := global
	higher.Keys = []string{"ctrl+x", "ctrl+y"}
	higher.Source.Layer = "user"
	conditioned := higher
	conditioned.Keys = []string{"c"}
	conditioned.Plugin = "example"
	conditioned.Source.Order = 9
	earlier := conditioned
	earlier.Keys = []string{"e"}
	earlier.Source.Order = 1
	fromA, fromB := higher, higher
	fromA.Keys, fromB.Keys = []string{"a"}, []string{"b"}
	fromA.Source.Plugin, fromB.Source.Plugin = "example.a", "example.b"
	fromA.Source.Order, fromB.Source.Order = 9, 0
	other := scoped
	other.Focus = protocol.SurfaceSidebarList
	disabled := higher
	disabled.State = "disabled"
	shadowed := higher
	shadowed.State = "shadowed"
	for _, tt := range []struct {
		name    string
		entries []keyEntry
		want    string
	}{
		{"global before scoped", []keyEntry{scoped, global}, "g"},
		{"layer", []keyEntry{global, higher}, "ctrl+x ctrl+y"},
		{"condition does not reorder", []keyEntry{higher, conditioned}, "ctrl+x ctrl+y"},
		{"source plugin before order", []keyEntry{fromB, fromA}, "a"},
		{"source order", []keyEntry{conditioned, earlier}, "e"},
		{"wrong focus", []keyEntry{other}, "unbound"},
		{"disabled and shadowed", []keyEntry{disabled, shadowed}, "unbound"},
		{"absent", nil, "unbound"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (keyListing{Entries: tt.entries}).effective("action.target", protocol.SurfaceModal, "unbound")
			if got != tt.want {
				t.Fatalf("key %q, want %q", got, tt.want)
			}
		})
	}
	l := fixture()
	k := fixtureKeys()
	for _, tt := range []struct {
		id   protocol.SurfaceID
		want string
	}{
		{protocol.SurfaceActivityRail, ""}, {protocol.SurfaceSidebarNavigation, "["}, {protocol.SurfaceSidebarList, "["},
		{protocol.SurfaceThreadRowStatus, "["}, {protocol.SurfaceSidebarFooter, "["}, {protocol.SurfaceMainHeader, "f1"},
		{protocol.SurfaceMainStage, "f1"}, {protocol.SurfaceSidePanel, "]"}, {protocol.SurfaceAppOverlay, "f2"},
		{protocol.SurfaceModal, "rail"}, {protocol.SurfaceFullCanvas, "rail"}, {protocol.SurfaceWorkspaceStage, "rail"}, {"future-surface", ""},
	} {
		t.Run(string(tt.id), func(t *testing.T) {
			if got := l.surfaceKey(k, tt.id); got != tt.want {
				t.Fatalf("surface key %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("visible modal without rail", func(t *testing.T) {
		modal := fixture()
		modal.Geometry.Rects[protocol.SurfaceModal] = rect{10, 5, 100, 20}
		close := binding("husk.modal.close", "esc")
		close.Focus = protocol.SurfaceModal
		if got := modal.surfaceKey(keyListing{Entries: []keyEntry{close}}, protocol.SurfaceModal); got != "esc" {
			t.Fatalf("modal key %q, want esc", got)
		}
	})
	l.Surfaces.Placement.RailItem = "example.rail"
	l.Surfaces.Overlays.Modal.RailItem = "example.modal"
	for _, tt := range []struct {
		id   protocol.SurfaceID
		want string
	}{{protocol.SurfaceModal, "palette, unbound"}, {protocol.SurfaceFullCanvas, "palette"}, {protocol.SurfaceWorkspaceStage, "palette, f1"}} {
		if got := l.surfaceKey(k, tt.id); got != tt.want {
			t.Errorf("%s: %s", tt.id, got)
		}
	}
	if (keyListing{Failed: true}).effective("missing", protocol.SurfaceMainStage, "unbound") != "?" {
		t.Fatal("failure fallback")
	}
}
