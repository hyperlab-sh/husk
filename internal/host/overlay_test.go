package host

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestOverlayResizeSnapshot(t *testing.T) {
	dir := writeFrontendPlugin(t, "annotation", []string{string(protocol.SurfaceAppOverlay)}, nil)
	h, _, fe := bootFrontendWith(t, nil, dir)
	var n recNotifier
	var handles contracts.FrontendSlices
	h.loop(func() {
		var err error
		handles, err = fe.OwnSlices()
		if err != nil {
			t.Fatal(err)
		}
		h.root.engine.WatchPlugin("annotation", &n)
		if _, err = handles.Geometry.Set(json.RawMessage(`{"terminal":{"width":144,"height":40},"too_small":false,"rects":{"app-overlay":{"x":0,"y":0,"width":144,"height":40,"inner":{"x":0,"y":0,"width":144,"height":40}}}}`)); err != nil {
			t.Fatal(err)
		}
		if _, err = handles.Focus.Set(json.RawMessage(`{"surface":"main-stage"}`)); err != nil {
			t.Fatal(err)
		}
		if err = fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { v.Visible = []protocol.SurfaceID{protocol.SurfaceAppOverlay} }); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {})
	got := n.resized(t)
	want := protocol.SurfaceSize{Surface: protocol.SurfaceAppOverlay, Width: 144, Height: 40, Visible: true}
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != want {
		t.Fatalf("resized=%+v, want %+v", got, want)
	}
	h.loop(func() {
		if _, err := handles.Geometry.Set(json.RawMessage(`{"terminal":{"width":40,"height":10},"too_small":true,"rects":{"app-overlay":{"x":0,"y":0,"width":144,"height":40,"inner":{"x":0,"y":0,"width":144,"height":40}}}}`)); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {})
	if len(n.resized(t)) != 1 {
		t.Fatal("small terminal pushed overlay resize")
	}
}
