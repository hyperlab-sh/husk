package pods

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// starterFunc adapts a function to the WrapperStarter seam for tests that need
// per-plugin outcomes.
type starterFunc func(pluginID string, opening []protocol.Message) error

func (f starterFunc) Start(pluginID string, opening []protocol.Message) error {
	return f(pluginID, opening)
}

// Launch step 1 through the PodRegistry seam: Reserve answers the duplicate
// question and its conflict is Add's duplicate error.
func TestAddReservesThroughPodRegistry(t *testing.T) {
	rec := fakes.NewRecorder()
	h, loop, _, procs := hostFixture(t)
	h.opts.Registry = fakes.NewPods(rec)

	addPod(t, h, "files")
	if _, err := h.Add(manifestSpec("files")); err == nil {
		t.Fatal("duplicate id accepted")
	} else if contracts.CodeOf(err) != string(protocol.CodeConflict) {
		t.Fatalf("duplicate error code %q, want conflict", contracts.CodeOf(err))
	}
	if methods := rec.Methods(); !reflect.DeepEqual(methods, []string{"Pods.Reserve", "Pods.Reserve"}) {
		t.Fatal("reserve calls", methods)
	}
	if len(loop.statuses) != 1 || loop.statuses[0].Info.Status != wrapper.Starting {
		t.Fatal("duplicate add wrote liveness", loop.statuses)
	}
	if len(procs) != 0 {
		t.Fatal("add launched a process")
	}
}

// A spec that fails validation never claims a registry reservation.
func TestAddValidatesBeforeReserve(t *testing.T) {
	rec := fakes.NewRecorder()
	h, _, _, _ := hostFixture(t)
	h.opts.Registry = fakes.NewPods(rec)

	bad := manifestSpec("files")
	bad.Protocol = 0
	if _, err := h.Add(bad); err == nil {
		t.Fatal("invalid spec accepted")
	}
	if methods := rec.Methods(); len(methods) != 0 {
		t.Fatal("invalid spec reserved", methods)
	}
}

// Launch step 6 through the WrapperStarter seam: the injected starter receives
// the opening queue and nil means started, so the Host writes live itself and
// the Factory path stays untouched.
func TestGateStartsWrapperThroughStarterSeam(t *testing.T) {
	rec := fakes.NewRecorder()
	h, loop, _, procs := hostFixture(t)
	h.opts.Starter = fakes.NewWrapper(rec)

	addPod(t, h, "b")
	addPod(t, h, "a")
	prepareHost(t, h)

	calls := rec.Calls()
	var starts int
	for _, c := range calls {
		if c.Method != "Wrapper.Start" {
			t.Fatalf("unexpected seam call %v", c.Method)
		}
		if len(c.Args) != 2 || c.Args[1] != 3 { // fakeLoop.Opening queues three messages
			t.Fatalf("start without the opening queue: %v", c.Args)
		}
		starts++
	}
	if starts != 2 {
		t.Fatal("starter calls", starts)
	}
	if len(procs) != 0 {
		t.Fatal("injected starter still built a wrapper process")
	}
	var live int
	for _, s := range loop.statuses {
		if s.Info.Status == wrapper.Live {
			live++
		}
	}
	if live != 2 {
		t.Fatal("live writes", live)
	}
}

// A starter error is that plugin's launch failure: failed liveness, no retry,
// and the rest of the batch still starts.
func TestStarterErrorFailsOnlyThatPlugin(t *testing.T) {
	fail := map[string]bool{"b": true}
	h, loop, _, _ := hostFixture(t)
	h.opts.Starter = starterFunc(func(id string, _ []protocol.Message) error {
		if fail[id] {
			return errors.New("spawn refused")
		}
		return nil
	})

	addPod(t, h, "b")
	addPod(t, h, "a")
	prepareHost(t, h)

	status := map[string]wrapper.Liveness{}
	for _, s := range loop.statuses {
		if s.Info.Status == wrapper.Starting {
			continue
		}
		status[s.Info.PluginID] = s.Info.Status
	}
	if status["a"] != wrapper.Live || status["b"] != wrapper.Failed {
		t.Fatal("starter error did not fail only that plugin", status)
	}
}

// Without an injected starter the Host itself is the WrapperStarter: the
// Factory/Wrapper path starts, registers a token before Start and reports live.
func TestHostStartIsTheBuiltinWrapperStarter(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	addPod(t, h, "a")
	if err := h.Start("missing", nil); err == nil {
		t.Fatal("unknown plugin started")
	}
	prepareHost(t, h)
	p := procs["a"]
	if p == nil {
		t.Fatal("factory never built the process")
	}
	if len(loop.statuses) != 2 || loop.statuses[1].Info.Status != wrapper.Live {
		t.Fatal("live not reported", loop.statuses)
	}
}

func manifestSpec(id string) manifest.Spec {
	return manifest.Spec{ID: id, Protocol: protocol.Major, Command: []string{"plugin"}}
}
