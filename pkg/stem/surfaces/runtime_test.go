package surfaces

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestRuntimeOpenAdmissions(t *testing.T) {
	st, e, _ := newEngine(t, Options{})
	for _, placement := range []protocol.SurfacePlacement{protocol.SurfacePlacementModal, protocol.SurfacePlacementCanvas} {
		name := string(placement)
		got, err := e.SurfaceOpen("example.files", protocol.SurfaceOpenParams{Placement: placement, Name: name, Title: "A title", Frame: &protocol.FrameSpec{Border: true, Label: "label", Close: true}})
		if err != nil {
			t.Fatal(err)
		}
		id := protocol.SurfaceID("example.files." + name)
		r := resolution(t, st)
		if got.Surface != id || r.Runtime[id].Plugin != "example.files" || r.Occupants[id].Plugin != "example.files" {
			t.Fatalf("runtime occupancy not committed: %+v", r)
		}
	}
	for _, plugin := range []string{"example.files", "other"} {
		_, err := e.SurfaceOpen(plugin, protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementModal, Name: "another"})
		if codeOf(t, err) != protocol.CodeBusy || err.(*Error).Data["reason"] != "modal_open" {
			t.Fatal("modal singleton lost")
		}
	}
	_, err := e.SurfaceOpen("example.files", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: "canvas"})
	if codeOf(t, err) != protocol.CodeConflict {
		t.Fatal("duplicate name accepted")
	}
	for i := 2; i < 8; i++ {
		if _, err = e.SurfaceOpen("example.files", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = e.SurfaceOpen("example.files", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: "ninth"})
	if codeOf(t, err) != protocol.CodeBusy || err.(*Error).Data["reason"] != "too_many_surfaces" || len(resolution(t, st).Runtime) != 8 {
		t.Fatal("ninth open did not preserve limit")
	}
	// Detached seam values cannot change later frames or dimension requests.
	rows := e.RuntimeSurfaces()
	rows[0].Frame.Label = "mutated"
	if e.RuntimeSurfaces()[0].Frame.Label != "label" {
		t.Fatal("runtime frame leaked mutable data")
	}
}

func TestRuntimeInvalidOpenIsAtomic(t *testing.T) {
	st, e, _ := newEngine(t, Options{})
	for _, p := range []protocol.SurfaceOpenParams{
		{Placement: "stage", Name: "n"}, {Placement: "panel", Name: "n"},
		{Placement: protocol.SurfacePlacementCanvas, Name: strings.Repeat("x", 33)},
		{Placement: protocol.SurfacePlacementCanvas, Name: "n", Title: strings.Repeat("界", 81)},
	} {
		if _, err := e.SurfaceOpen("p", p); codeOf(t, err) != protocol.CodeInvalidParams {
			t.Fatal("invalid open accepted")
		}
	}
	if len(resolution(t, st).Runtime) != 0 || len(e.RuntimeSurfaces()) != 0 {
		t.Fatal("invalid opens changed runtime")
	}
}

func TestRuntimeClosePathsAndYield(t *testing.T) {
	for _, path := range []string{"surface.close", "host_close", "plugin_exit", "plugin_remove"} {
		t.Run(path, func(t *testing.T) {
			live := liveness{dead: map[string]bool{}}
			st, e, pods := newEngine(t, Options{Liveness: live})
			pods.launch("p")
			out, err := e.SurfaceOpen("p", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementModal, Name: "popup"})
			if err != nil {
				t.Fatal(err)
			}
			id := out.Surface
			notify := &sink{}
			e.WatchPlugin("p", notify)
			if err = e.SurfaceUpdate("p", protocol.SurfaceUpdateParams{Surface: id, Lines: []string{"hello"}, Capture: protocol.CaptureValueAll.Ptr()}); err != nil {
				t.Fatal(err)
			}
			if err = e.SurfaceClose("p", protocol.SurfaceCloseParams{Surface: id, Yield: true}); codeOf(t, err) != protocol.CodeInvalidParams {
				t.Fatal("runtime yielded")
			}
			if resolution(t, st).Runtime[id].Plugin != "p" {
				t.Fatal("yield cleared runtime")
			}
			err = nil // The yield refusal above is not this close's outcome.
			switch path {
			case "surface.close":
				err = e.SurfaceClose("p", protocol.SurfaceCloseParams{Surface: id})
			case "host_close":
				err = e.CloseSurface(id)
			case "plugin_exit":
				live.dead["p"] = true
				e.LivenessChanged()
			case "plugin_remove":
				live.dead["p"] = true
				pods.teardown("p")
				e.LivenessChanged()
			}
			if err != nil {
				t.Fatal(err)
			}
			settle(st, e)
			res := resolution(t, st)
			if len(res.Runtime) != 0 || res.Occupants[id].Plugin != "" || res.Capture[id] != protocol.CaptureNone || len(e.RuntimeSurfaces()) != 0 {
				t.Fatalf("%s left runtime state: %+v", path, res)
			}
			notices := notify.resizedMsgs(t)
			if len(notices) != 1 || len(notices[0].Surfaces) != 1 || notices[0].Surfaces[0].Surface != id || notices[0].Surfaces[0].Visible {
				t.Fatalf("final hidden resize missing: %+v", notices)
			}
			if err = e.UpdateView(func(v *contracts.SurfaceView) { v.Visible = []protocol.SurfaceID{id} }); codeOf(t, err) != protocol.CodeInvalidParams {
				t.Fatal("closed runtime id remained drawable")
			}
		})
	}
}
