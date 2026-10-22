package main

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hostStep is a message the fake host runs on the program loop, the way real
// Pod notices and surface updates reach the model.
type hostStep func()

// stepHost is a host.Looped that runs hostStep messages and does nothing else.
type stepHost struct {
	contracts.Host
	fe contracts.Frontend
}

func (h stepHost) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (h stepHost) Close() error                  { return nil }
func (h stepHost) Frontend() contracts.Frontend  { return h.fe }
func (h stepHost) Tick()                         {}
func (h stepHost) Planned() int                  { return 0 }
func (h stepHost) Update(msg any) bool {
	st, ok := msg.(hostStep)
	if ok {
		st()
	}
	return ok
}

// dumpModel is a hermetic workbench for the --dump-frame driver tests, wired
// to a stepHost and the program reference runDumpFrame fills in.
func dumpModel() (*WorkbenchModel, *programRef) {
	m := goldenModel()
	fe := withFE(m)
	fe.pods, fe.occ = map[string]string{}, map[protocol.SurfaceID]string{}
	m.attach(stepHost{fe: fe}, nil)
	return m, newProgramRef()
}

// starting declares a Pod that has not registered yet.
func starting(m *WorkbenchModel, id string) hostStep {
	fe := withFE(m)
	return func() {
		fe.pods[id] = "starting"
		fe.publish()
	}
}

// register brings a plugin live with its surfaces and optional rail items.
func register(m *WorkbenchModel, id string, slots []protocol.SurfaceID, rail ...contracts.RailItem) hostStep {
	fe := withFE(m)
	return func() {
		fe.pods[id] = "live"
		for _, s := range slots {
			fe.occ[s] = id
		}
		fe.Rails = append(fe.Rails, rail...)
		fe.publish()
	}
}

func slotMsg(m *WorkbenchModel, id string, slot protocol.SurfaceID, line string) hostStep {
	return func() {
		m.content(id, protocol.SurfaceUpdateParams{Surface: slot, Title: string(slot), Lines: []string{line}})
	}
}

// feed delivers steps to the running program from a goroutine, in order.
func feed(prog *programRef, steps ...hostStep) {
	go func() {
		for _, st := range steps {
			for !prog.post(st) {
				runtime.Gosched()
			}
		}
	}()
}

func TestDumpFrameSettlesOnPublishedContentWithoutDeadline(t *testing.T) {
	m, prog := dumpModel()
	feed(prog,
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "ALPHA-STAGE-LINE"),
	)
	start := time.Now()
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 1, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("settled run took %s; it must return as soon as the content arrives", d)
	}
	if !strings.Contains(ansi.Strip(frame), "ALPHA-STAGE-LINE") {
		t.Fatalf("frame lacks the published content:\n%s", ansi.Strip(frame))
	}
	if got := len(strings.Split(frame, "\n")); got != goldenH {
		t.Fatalf("frame has %d rows, want %d", got, goldenH)
	}
}

func TestDumpFrameWaitsForEveryLaunchedPlugin(t *testing.T) {
	m, prog := dumpModel()
	// The second plugin registers after the first has already published; the
	// frame must still include the second plugin's slot.
	feed(prog,
		starting(m, "alpha"), starting(m, "beta"),
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "FROM-ALPHA"),
		register(m, "beta", []protocol.SurfaceID{protocol.SurfaceSidebarList}),
		slotMsg(m, "beta", protocol.SurfaceSidebarList, "FROM-BETA"),
	)
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 2, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	if len(m.plugins) != 2 {
		t.Fatalf("registered plugins = %d, want 2", len(m.plugins))
	}
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "FROM-ALPHA") {
		t.Fatalf("frame lacks FROM-ALPHA:\n%s", plain)
	}
	if !strings.Contains(plain, "FROM-BETA") {
		t.Fatalf("frame lacks FROM-BETA:\n%s", plain)
	}
}

func TestDumpFrameNoPluginsReturnsImmediately(t *testing.T) {
	m, prog := dumpModel()
	start := time.Now()
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("empty workbench took %s", d)
	}
	if got := len(strings.Split(frame, "\n")); got != goldenH {
		t.Fatalf("frame has %d rows, want %d", got, goldenH)
	}
}

func TestDumpFrameTimeoutReportsWhatIsMissing(t *testing.T) {
	m, prog := dumpModel()
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 2, Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout error when no plugin ever registers")
	}
	for _, want := range []string{"timed out", "pods map[]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if got := len(strings.Split(frame, "\n")); got != goldenH {
		t.Fatalf("timed-out run must still return a full frame, got %d rows", got)
	}
}

func TestDumpFrameRailModalWaitsForModalSurface(t *testing.T) {
	m, prog := dumpModel()
	rail := contracts.RailItem{ID: "alpha.open", Plugin: "alpha", Icon: "A", Label: "Alpha", Placement: contracts.PlacementModal}
	feed(prog,
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceModal}, rail),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "BACKGROUND-LINE"),
		// The modal content arrives only after the stage content: the driver
		// must not settle on the stage update alone.
		slotMsg(m, "alpha", protocol.SurfaceModal, "MODAL-BODY-LINE"),
	)
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 1, RailMode: "modal", Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	if got := m.modalOwner().RailID; got != "alpha.open" {
		t.Fatalf("modal rail = %q, want alpha.open", got)
	}
	if !strings.Contains(ansi.Strip(frame), "MODAL-BODY-LINE") {
		t.Fatalf("frame lacks the modal body:\n%s", ansi.Strip(frame))
	}
}

func TestDumpFrameRailWithoutMatchingItemWarnsAndStillSettles(t *testing.T) {
	m, prog := dumpModel()
	feed(prog,
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "ONLY-STAGE"),
	)
	var stderr bytes.Buffer
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 1, RailMode: "full-canvas", Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second, Stderr: &stderr})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	if want := "--rail full-canvas: no loaded plugin declares a full-canvas rail item"; !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
	if !strings.Contains(ansi.Strip(frame), "ONLY-STAGE") {
		t.Fatalf("frame lacks stage content:\n%s", ansi.Strip(frame))
	}
}

func TestDumpFrameLateUpdateInsideQuietWindowIsIncluded(t *testing.T) {
	m, prog := dumpModel()
	feed(prog,
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "FIRST-PAINT"),
	)
	// A live-data refinement arrives after the first update, well inside the
	// quiet window; the frame must show it, not the first paint.
	go func() {
		time.Sleep(50 * time.Millisecond)
		prog.post(slotMsg(m, "alpha", protocol.SurfaceMainStage, "LIVE-DATA"))
	}()
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 1, Quiet: 500 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "LIVE-DATA") || strings.Contains(plain, "FIRST-PAINT") {
		t.Fatalf("frame must show the refined content only:\n%s", plain)
	}
}

func TestDumpFrameStreamingPluginSettlesBeforeDeadline(t *testing.T) {
	m, prog := dumpModel()
	feed(prog,
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "STREAM-0"),
	)
	// A plugin that never goes quiet: an update every quiet/2, like the real
	// Rust plugin's live data. The quiet window alone would never elapse.
	quiet := 100 * time.Millisecond
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(quiet / 2)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				prog.post(slotMsg(m, "alpha", protocol.SurfaceMainStage, "STREAM-N"))
			}
		}
	}()
	start := time.Now()
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog,
		Launched: 1, Quiet: quiet, MaxSettle: 400 * time.Millisecond, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("streaming run took %s; the settle cap must end it well before the deadline", d)
	}
	if !strings.Contains(ansi.Strip(frame), "STREAM-") {
		t.Fatalf("frame lacks streamed content:\n%s", ansi.Strip(frame))
	}
}

func TestDumpFramePanicIsNotReportedAsTimeout(t *testing.T) {
	m, prog := dumpModel()
	feed(prog, func() { panic("boom") })
	_, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 1, Timeout: 10 * time.Second})
	if err == nil || strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a panic error, not a timeout", err)
	}
}

// A broken plugin must not pass as an empty workbench: when every launched Pod
// ended without going live the run fails at once, naming them, with the frame.
func TestDumpFrameAllPodsFailedIsAnError(t *testing.T) {
	m, prog := dumpModel()
	fe := withFE(m)
	feed(prog,
		starting(m, "alpha"), starting(m, "beta"),
		func() { fe.pods["alpha"], fe.pods["beta"] = "exited", "failed"; fe.publish() },
	)
	start := time.Now()
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 2, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
	if err == nil {
		t.Fatal("a run with no live plugin succeeded")
	}
	for _, want := range []string{"plugin alpha exited", "plugin beta failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the failure waited for the deadline")
	}
	if got := len(strings.Split(frame, "\n")); got != goldenH {
		t.Fatalf("failed run must still return a full frame, got %d rows", got)
	}
}

// One live plugin among failed ones is a usable workbench.
func TestDumpFramePartialFailureStillSettles(t *testing.T) {
	m, prog := dumpModel()
	fe := withFE(m)
	feed(prog,
		starting(m, "alpha"), starting(m, "beta"),
		func() { fe.pods["beta"] = "failed"; fe.publish() },
		register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
		slotMsg(m, "alpha", protocol.SurfaceMainStage, "ALIVE"),
	)
	frame, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 2, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil || !strings.Contains(ansi.Strip(frame), "ALIVE") {
		t.Fatalf("err=%v frame has ALIVE=%v", err, strings.Contains(ansi.Strip(frame), "ALIVE"))
	}
}
