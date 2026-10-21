package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// dumpModel is a hermetic workbench for the --dump-frame driver tests.
func dumpModel() *WorkbenchModel {
	m := goldenModel()
	return m
}

func registerMsg(id string, slots []huskipc.SlotID, rail ...huskipc.RailItemSpec) tea.Msg {
	return pluginRegisteredMsg{&ConnectedPlugin{
		Info: huskipc.HelloPayload{
			PluginID: id, Name: id, Version: "0.1.0", Language: "go",
			Slots: slots, RailItems: rail,
		},
		Writer: huskipc.NewConnWriter(io.Discard),
	}}
}

func slotMsg(id string, slot huskipc.SlotID, line string) tea.Msg {
	return ipcEventMsg{pluginID: id, env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{
		PluginID: id, Slot: slot, Title: string(slot), Lines: []string{line},
	}}}
}

// feed delivers msgs through the same channel transport goroutines use.
func feed(m *WorkbenchModel, msgs ...tea.Msg) {
	go func() {
		for _, msg := range msgs {
			m.subChan <- msg
		}
	}()
}

func TestDumpFrameSettlesOnPublishedContentWithoutDeadline(t *testing.T) {
	m := dumpModel()
	feed(m,
		registerMsg("alpha", []huskipc.SlotID{huskipc.SlotMainStage}),
		slotMsg("alpha", huskipc.SlotMainStage, "ALPHA-STAGE-LINE"),
	)
	start := time.Now()
	frame, err := runDumpFrame(m, dumpFrameOptions{Launched: 1, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
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
	m := dumpModel()
	// The second plugin registers after the first has already published; the
	// frame must still include the second plugin's slot.
	feed(m,
		registerMsg("alpha", []huskipc.SlotID{huskipc.SlotMainStage}),
		slotMsg("alpha", huskipc.SlotMainStage, "FROM-ALPHA"),
		registerMsg("beta", []huskipc.SlotID{huskipc.SlotSidebarList}),
		slotMsg("beta", huskipc.SlotSidebarList, "FROM-BETA"),
	)
	frame, err := runDumpFrame(m, dumpFrameOptions{Launched: 2, Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
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
}

func TestDumpFrameNoPluginsReturnsImmediately(t *testing.T) {
	m := dumpModel()
	start := time.Now()
	frame, err := runDumpFrame(m, dumpFrameOptions{Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
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
	m := dumpModel()
	frame, err := runDumpFrame(m, dumpFrameOptions{Launched: 2, Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout error when no plugin ever registers")
	}
	for _, want := range []string{"timed out", "2 plugin(s)", "registered 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if got := len(strings.Split(frame, "\n")); got != goldenH {
		t.Fatalf("timed-out run must still return a full frame, got %d rows", got)
	}
}

func TestDumpFrameRailModalWaitsForModalSurface(t *testing.T) {
	m := dumpModel()
	rail := huskipc.RailItemSpec{ID: "alpha.open", PluginID: "alpha", Icon: "A", Label: "Alpha", Placement: huskipc.PlacementModal}
	feed(m,
		registerMsg("alpha", []huskipc.SlotID{huskipc.SlotMainStage, huskipc.SlotModalSurface}, rail),
		slotMsg("alpha", huskipc.SlotMainStage, "BACKGROUND-LINE"),
		// The modal content arrives only after the stage content: the driver
		// must not settle on the stage update alone.
		slotMsg("alpha", huskipc.SlotModalSurface, "MODAL-BODY-LINE"),
	)
	frame, err := runDumpFrame(m, dumpFrameOptions{Launched: 1, RailMode: "modal", Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second})
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
	m := dumpModel()
	feed(m,
		registerMsg("alpha", []huskipc.SlotID{huskipc.SlotMainStage}),
		slotMsg("alpha", huskipc.SlotMainStage, "ONLY-STAGE"),
	)
	var stderr bytes.Buffer
	frame, err := runDumpFrame(m, dumpFrameOptions{Launched: 1, RailMode: "full-canvas", Quiet: 20 * time.Millisecond, Timeout: 30 * time.Second, Stderr: &stderr})
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
	m := dumpModel()
	feed(m,
		registerMsg("alpha", []huskipc.SlotID{huskipc.SlotMainStage}),
		slotMsg("alpha", huskipc.SlotMainStage, "FIRST-PAINT"),
	)
	// A live-data refinement arrives after the first update, well inside the
	// quiet window; the frame must show it, not the first paint.
	go func() {
		time.Sleep(50 * time.Millisecond)
		m.subChan <- slotMsg("alpha", huskipc.SlotMainStage, "LIVE-DATA")
	}()
	frame, err := runDumpFrame(m, dumpFrameOptions{Launched: 1, Quiet: 500 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("runDumpFrame: %v", err)
	}
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "LIVE-DATA") || strings.Contains(plain, "FIRST-PAINT") {
		t.Fatalf("frame must show the refined content only:\n%s", plain)
	}
}
