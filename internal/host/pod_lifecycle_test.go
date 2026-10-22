package host

import (
	"errors"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// stageOccupant is the resolved occupant of main-stage.
func stageOccupant(h *harness) string {
	var occupant string
	h.loop(func() {
		res, _ := decodeSurfaces(h.root.st.Snapshot())
		occupant = res.Occupants["main-stage"].Plugin
	})
	return occupant
}

// D70: a plugin whose command does not exist never holds the stage; the next
// claimant does.
func TestFailedLaunchDoesNotHoldASurface(t *testing.T) {
	broken := writeFrontendPlugin(t, "alpha", []string{"main-stage"}, nil)
	good := writeFrontendPlugin(t, "beta", []string{"main-stage"}, nil)
	f := newFakes()
	factory := func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		if spec.ID == "alpha" {
			return nil, errors.New("exec: no such file or directory")
		}
		return f.factory(spec, o)
	}
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{broken, good} }, withHook(hook), WithFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started // beta; alpha never starts

	if got := h.podStatus("alpha"); got != "failed" {
		t.Fatalf("alpha status = %q, want failed", got)
	}
	if got := stageOccupant(h); got != "beta" {
		t.Fatalf("main-stage occupant = %q, want beta", got)
	}
}

// D70: when the occupant leaves live the waiting claimant takes the surface
// and is told its size.
func TestOccupantThatFailsHandsTheStageOverAndIsTold(t *testing.T) {
	alpha := writeFrontendPlugin(t, "alpha", []string{"main-stage"}, nil)
	beta := writeFrontendPlugin(t, "beta", []string{"main-stage"}, nil)
	h, _, fe := bootFrontendWith(t, nil, alpha, beta)
	var told recNotifier
	h.loop(func() {
		s, err := fe.OwnSlices()
		if err != nil {
			t.Error(err)
			return
		}
		h.root.engine.WatchPlugin("beta", &told)
		if _, err := s.Geometry.Set(geometryOf(map[string][2]int{"main-stage": {80, 20}})); err != nil {
			t.Error(err)
		}
	})
	if got := stageOccupant(h); got != "alpha" {
		t.Fatalf("precondition: main-stage occupant = %q, want alpha", got)
	}
	h.loop(func() {
		if got := told.resized(t); len(got) != 0 {
			t.Errorf("beta heard about a surface it does not hold: %+v", got)
		}
	})

	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Failed, LastError: "crashed (exit status 1)"}})
	h.loop(func() {
		res, _ := decodeSurfaces(fe.Snapshot())
		if got := res.Occupants["main-stage"].Plugin; got != "beta" {
			t.Errorf("main-stage occupant after alpha failed = %q, want beta", got)
		}
		got := told.resized(t)
		if len(got) != 1 || len(got[0]) != 1 || got[0][0].Surface != protocol.SurfaceID("main-stage") || got[0][0].Width != 80 || got[0][0].Height != 20 {
			t.Errorf("beta's surface.resized = %+v, want main-stage at 80x20", got)
		}
	})
}
