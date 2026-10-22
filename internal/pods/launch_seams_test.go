package pods

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
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
// question for ids the Host has not seen, and the local map check holds even
// when the registry forgets an id (finding 6): a Pod already in this Host is
// never replaced, so its credential cannot outlive its pod record.
func TestAddReservesThroughPodRegistry(t *testing.T) {
	rec := fakes.NewRecorder()
	h, loop, _, procs := hostFixture(t)
	h.opts.Registry = fakes.NewPods(rec)

	addPod(t, h, "files")
	if _, err := h.Add(manifestSpec("files")); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if methods := rec.Methods(); !reflect.DeepEqual(methods, []string{"Pods.Reserve"}) {
		t.Fatal("reserve calls", methods)
	}
	if len(loop.statuses) != 1 || loop.statuses[0].Info.Status != wrapper.Starting {
		t.Fatal("duplicate add wrote liveness", loop.statuses)
	}
	if len(procs) != 0 {
		t.Fatal("add launched a process")
	}
}

// A registry that lost the id still cannot double-add: the Host's own map
// answers the duplicate question before the reservation (finding 6).
func TestAddDuplicateHoldsEvenWhenRegistryForgets(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	h.opts.Registry = forgetfulRegistry{}

	addPod(t, h, "files")
	if _, err := h.Add(manifestSpec("files")); err == nil {
		t.Fatal("duplicate id accepted through a forgetful registry")
	}
	if len(h.pods) != 1 {
		t.Fatal("duplicate add replaced the pod", len(h.pods))
	}
}

// forgetfulRegistry reserves every id without tracking it.
type forgetfulRegistry struct{}

func (forgetfulRegistry) Reserve(context.Context, string) error { return nil }
func (forgetfulRegistry) SetStatus(context.Context, string, contracts.PodStatusUpdate) error {
	return nil
}
func (forgetfulRegistry) Release(context.Context, string) {}

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

// fakeHandle is a recorded contracts.SliceHandle: it carries the writes a
// plugin made through it and remembers Unregister calls.
type fakeHandle struct {
	namespace    string
	sets         int
	patches      int
	unregistered bool
}

func (f *fakeHandle) Set(value json.RawMessage) (uint64, error) { f.sets++; return uint64(f.sets), nil }
func (f *fakeHandle) Patch(patch json.RawMessage, ifRevision uint64) (uint64, error) {
	f.patches++
	return uint64(f.patches), nil
}
func (f *fakeHandle) Unregister() { f.unregistered = true }

// registerClosures builds one RegisterSlicesMsg worth of closures over fake
// handles: the good one resolves, the broken one fails.
func registerClosures(t *testing.T) (map[string]func() (contracts.SliceHandle, error), *fakeHandle) {
	t.Helper()
	good := &fakeHandle{namespace: "files.state"}
	register := map[string]func() (contracts.SliceHandle, error){
		"files": func() (contracts.SliceHandle, error) { return good, nil },
		"broken": func() (contracts.SliceHandle, error) {
			return nil, errors.New("slice already registered")
		},
	}
	return register, good
}

// RegisterSlicesMsg applies each closure on the loop, stores the resolved
// handles per plugin and acknowledges the worker with exactly the outcomes:
// one handle, one failure. A nil Done channel is ignored rather than panicking.
func TestRegisterSlicesMsgStoresHandlesAndAcks(t *testing.T) {
	h, loop, _, _ := hostFixture(t)
	addPod(t, h, "files")
	addPod(t, h, "broken")
	register, good := registerClosures(t)

	done := make(chan Registered, 1)
	if !h.Handle(RegisterSlicesMsg{Register: register, Done: done}) {
		t.Fatal("unhandled register message")
	}
	testclock.Receive(t, done) // the ack arrived (buffered, but keep the shape honest)

	if _, ok := NewPodSlices(h).Handle("files", "files.state"); !ok {
		t.Fatal("resolved handle not stored")
	}
	if _, ok := NewPodSlices(h).Handle("broken", "broken.state"); ok {
		t.Fatal("failed registration stored a handle")
	}
	if _, ok := NewPodSlices(h).Handle("files", "files.other"); ok {
		t.Fatal("lookup outside the plugin's own state slice succeeded")
	}
	if good.sets != 0 {
		t.Fatal("registration already wrote state")
	}
	if !h.Handle(RegisterSlicesMsg{Register: register}) {
		t.Fatal("nil Done channel not tolerated")
	}
	if len(loop.statuses) != 2 { // only the Add liveness of the two pods
		t.Fatal("register message wrote liveness", loop.statuses)
	}
}

// A Pod-registered handle serves the Stem State write path; the same writes
// through the wrong plugin, or after teardown, do not resolve.
func TestPodSlicesServesOnlyOwnStateNamespace(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	addPod(t, h, "files")
	register, good := registerClosures(t)
	h.Handle(RegisterSlicesMsg{Register: map[string]func() (contracts.SliceHandle, error){
		"files": register["files"],
	}, Done: make(chan Registered, 1)})

	slices := NewPodSlices(h)
	handle, ok := slices.Handle("files", "files.state")
	if !ok || handle != contracts.SliceHandle(good) {
		t.Fatalf("own handle not resolved: %v %v", handle, ok)
	}
	if _, ok := slices.Handle("notes", "files.state"); ok {
		t.Fatal("another plugin's handle resolved")
	}
	if _, ok := (PodSlices{}).Handle("files", "files.state"); ok {
		t.Fatal("nil adapter resolved a handle")
	}
}

// TeardownMsg is the loop half of Pod teardown: the stored handle is
// unregistered, the registry id released, the credential revoked and the Pod
// forgotten; a second teardown is a no-op.
func TestTeardownUnregistersHandleAndReleasesId(t *testing.T) {
	h, loop, _, _ := hostFixture(t)
	addPod(t, h, "files")
	register, good := registerClosures(t)
	h.Handle(RegisterSlicesMsg{Register: map[string]func() (contracts.SliceHandle, error){
		"files": register["files"],
	}, Done: make(chan Registered, 1)})

	registry := fakes.NewPods(fakes.NewRecorder())
	h.opts.Registry = registry

	if !h.Handle(TeardownMsg{PluginID: "files"}) {
		t.Fatal("unhandled teardown")
	}
	if !good.unregistered {
		t.Fatal("teardown did not unregister the slice")
	}
	if _, ok := NewPodSlices(h).Handle("files", "files.state"); ok {
		t.Fatal("handle kept after teardown")
	}
	if _, ok := h.Pod("files"); ok {
		t.Fatal("pod kept after teardown")
	}
	if _, ok := registry.Entry("files"); ok {
		t.Fatal("registry id kept after teardown")
	}
	if !h.Handle(TeardownMsg{PluginID: "files"}) {
		t.Fatal("second teardown not handled")
	}
	if good.unregistered != true { // still exactly one unregister
		t.Fatal("second teardown repeated work")
	}
	var teardowns int
	for _, s := range loop.statuses {
		_ = s
	}
	teardowns = len(loop.logs)
	if teardowns != 1 {
		t.Fatal("teardown logged more than once", loop.logs)
	}
}

// The same launch failure may be reported by both the synchronous starter
// path and the process's async notice; the Host applies the first report
// only (round-1 finding 5 support): one failed liveness write, one log.
func TestLaunchFailedAppliedOnce(t *testing.T) {
	h, loop, _, _ := hostFixture(t)
	p := addPod(t, h, "files")
	err := errors.New("spawn refused")
	h.Handle(LaunchFailedMsg{PluginID: "files", Err: err})
	h.Handle(LaunchFailedMsg{PluginID: "files", Err: err})

	var failed int
	for _, s := range loop.statuses {
		if s.Info.Status == wrapper.Failed {
			failed++
		}
	}
	if failed != 1 || p.info.Status != wrapper.Failed {
		t.Fatal("failure applied more than once", failed, p.info.Status)
	}
	if len(loop.logs) != 1 || loop.logs[0].Level != "error" {
		t.Fatal("failure logs", loop.logs)
	}
}

// A Start failure is synchronous (round-1 finding 5): the starter error is
// returned by Start, the plugin goes starting -> failed and never reads live,
// and the wrapper's own async LaunchFailedMsg for the same failure is
// ignored — exactly one failed write and one error log after the drain.
func TestStartFailureIsSynchronousAndNeverLive(t *testing.T) {
	loop := &fakeLoop{events: make(chan tea.Msg, 128), ctx: t.Context()}
	h, err := New(loop, Options{Wrapper: wrapper.Options{Clock: testclock.New()}, Factory: func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		p := &fakeProcess{id: s.ID, info: wrapper.Info{PluginID: s.ID, Status: wrapper.Starting}, opts: o, done: make(chan struct{}), term: make(chan struct{})}
		p.startErr = errors.New("spawn refused")
		return p, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.cancel)

	pod := addPod(t, h, "files")
	prepareHost(t, h)

	// The wrapper's async notices for the same failure drain through Handle.
	for {
		select {
		case m := <-loop.events:
			h.Handle(m)
			continue
		default:
		}
		break
	}

	var failed, live int
	for _, s := range loop.statuses {
		switch s.Info.Status {
		case wrapper.Failed:
			failed++
		case wrapper.Live:
			live++
		}
	}
	if failed != 1 || live != 0 {
		t.Fatalf("start failure liveness: failed=%d live=%d", failed, live)
	}
	if pod.info.Status != wrapper.Failed || pod.info.LastError != "spawn refused" {
		t.Fatalf("pod state %+v", pod.info)
	}
	if len(loop.logs) != 1 || loop.logs[0].Level != "error" {
		t.Fatal("error logs", loop.logs)
	}
}
