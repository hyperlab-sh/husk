package main

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// Golden frame tests run the real WorkbenchModel under a tea.Program (via
// teatest), drive it with messages, and compare the final View() content
// against files in testdata/. Regenerate them after an intended visual change:
//
//	go test ./cmd/husk -run TestGoldenFrame -update
//
// Inspect the diff of the .golden files before committing: they hold the raw
// ANSI frame, so a palette or geometry change shows up as a byte change.
const (
	goldenW = 144
	goldenH = 40
)

// goldenModel is a hermetic workbench: no socket, no OS modifier polling, no
// log output, fixed size.
func goldenModel() *WorkbenchModel {
	m := newWorkbenchModel("", nil, "/demo")
	m.logOut = nil
	m.pollMods = func() (huskipc.ModifierState, bool) { return huskipc.ModifierState{}, false }
	m.width, m.height = goldenW, goldenH
	m.recomputeLayout()
	return m
}

// goldenPlugin registers a plugin occupying the given slots with one line of
// content each, through the same Update path real plugins use.
func goldenPlugin(m *WorkbenchModel, id string, slots map[huskipc.SlotID][]string) {
	var ids []huskipc.SlotID
	for s := range slots {
		ids = append(ids, s)
	}
	m.Update(pluginRegisteredMsg{&ConnectedPlugin{
		Info:   huskipc.HelloPayload{PluginID: id, Name: id, Version: "0.1.0", Language: "go", Slots: ids},
		Writer: huskipc.NewConnWriter(io.Discard),
	}})
	for slot, lines := range slots {
		m.Update(ipcEventMsg{pluginID: id, env: huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID: id, Slot: slot, Title: string(slot), Lines: lines,
		}}})
	}
}

// runGolden runs m under teatest, sends msgs in order, quits, and returns the
// frame the program would show last. Messages and the quit travel the same
// queue, so ordering is deterministic without sleeps.
func runGolden(t *testing.T, m *WorkbenchModel, msgs ...tea.Msg) string {
	t.Helper()
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(goldenW, goldenH))
	for _, msg := range msgs {
		tm.Send(msg)
	}
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final, ok := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*WorkbenchModel)
	if !ok {
		t.Fatal("final model is not *WorkbenchModel")
	}
	return final.View().Content
}

func requireFrameGeometry(t *testing.T, frame string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != goldenH {
		t.Errorf("frame has %d lines, want %d", len(lines), goldenH)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != goldenW {
			t.Errorf("line %d is %d cells wide, want %d", i, w, goldenW)
		}
	}
}

func TestGoldenFrameEmptyWorkbench(t *testing.T) {
	frame := runGolden(t, goldenModel())
	requireFrameGeometry(t, frame)
	golden.RequireEqual(t, frame)
}

func TestGoldenFrameOnePlugin(t *testing.T) {
	m := goldenModel()
	goldenPlugin(m, "p.one", map[huskipc.SlotID][]string{
		huskipc.SlotMainStage: {"stage line one", "stage line two"},
	})
	frame := runGolden(t, m)
	requireFrameGeometry(t, frame)
	if !strings.Contains(ansi.Strip(frame), "stage line one") {
		t.Error("plugin content missing from frame")
	}
	golden.RequireEqual(t, frame)
}

func TestGoldenFramePaletteOpen(t *testing.T) {
	m := goldenModel()
	goldenPlugin(m, "p.one", map[huskipc.SlotID][]string{
		huskipc.SlotMainStage: {"stage line one"},
	})
	frame := runGolden(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	requireFrameGeometry(t, frame)
	plain := ansi.Strip(frame)
	for _, want := range []string{"COMMAND PALETTE (app-overlay)", "❯ █"} {
		if !strings.Contains(plain, want) {
			t.Errorf("palette frame missing %q", want)
		}
	}
	closed := goldenModel()
	goldenPlugin(closed, "p.one", map[huskipc.SlotID][]string{
		huskipc.SlotMainStage: {"stage line one"},
	})
	if frame == runGolden(t, closed) {
		t.Error("ctrl+p did not change the frame: palette not drawn")
	}
	golden.RequireEqual(t, frame)
}

// With every region occupied, the header, stage and side panel must not run
// into each other: a gutter column separates adjacent surfaces.
func TestGoldenFrameFullSlots(t *testing.T) {
	m := goldenModel()
	goldenPlugin(m, "p.full", map[huskipc.SlotID][]string{
		huskipc.SlotSidebarNavigation: {"nav"},
		huskipc.SlotSidebarList:       {"list"},
		huskipc.SlotSidebarFooter:     {"foot"},
		huskipc.SlotMainHeader:        {strings.Repeat("HEADER ", 30)},
		huskipc.SlotMainStage:         {"stage"},
		huskipc.SlotSidePanel:         {"SIDEPANEL"},
	})
	frame := runGolden(t, m)
	requireFrameGeometry(t, frame)

	hdr, side := m.slotRects[huskipc.SlotMainHeader], m.slotRects[huskipc.SlotSidePanel]
	if side.Width == 0 {
		t.Fatal("side panel not laid out at 144x40")
	}
	if gap := side.X - (hdr.X + hdr.Width); gap < 1 {
		t.Errorf("header ends at col %d, side panel starts at %d: no gutter", hdr.X+hdr.Width, side.X)
	}
	// Long header text is clipped before the gutter: the gutter cell on every
	// header row is blank and the panel's own text starts at its first column.
	lines := strings.Split(frame, "\n")
	for y := hdr.InnerY; y < hdr.InnerY+hdr.InnerH; y++ {
		gutter := ansi.Strip(ansi.Cut(lines[y], hdr.X+hdr.Width, side.X))
		if strings.TrimSpace(gutter) != "" || ansi.StringWidth(gutter) != side.X-(hdr.X+hdr.Width) {
			t.Errorf("row %d: gutter cells = %q, want blank", y, gutter)
		}
		if strings.Contains(ansi.Strip(ansi.Cut(lines[y], side.X, goldenW)), "HEADER") {
			t.Errorf("row %d: header text runs into the side panel", y)
		}
	}
	if got := ansi.Strip(ansi.Cut(lines[side.InnerY], side.InnerX, side.InnerX+len("SIDEPANEL"))); got != "SIDEPANEL" {
		t.Errorf("side panel first row at col %d = %q, want SIDEPANEL", side.InnerX, got)
	}
	golden.RequireEqual(t, frame)
}
