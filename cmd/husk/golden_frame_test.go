package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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
	m := newWorkbenchModel()
	m.logOut = nil
	m.pollMods = func() (protocol.ModifierState, bool) { return protocol.ModifierState{}, false }
	m.width, m.height = goldenW, goldenH
	m.recomputeLayout()
	return m
}

// goldenPlugin registers a plugin occupying the given slots with one line of
// content each, through the same content sink a real host feeds.
func goldenPlugin(m *WorkbenchModel, id string, slots map[protocol.SurfaceID][]string) {
	addPlugin(m, id, slots)
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

// goldenFrames builds each golden scenario's final frame. The golden tests
// and TestGoldenFramesCellEquivalent share it so they render the same models.
var goldenFrames = map[string]func(t *testing.T) string{
	"TestGoldenFrameEmptyWorkbench": func(t *testing.T) string { return runGolden(t, goldenModel()) },
	"TestGoldenFrameOnePlugin": func(t *testing.T) string {
		m := goldenModel()
		goldenPlugin(m, "p.one", map[protocol.SurfaceID][]string{
			protocol.SurfaceMainStage: {"stage line one", "stage line two"},
		})
		return runGolden(t, m)
	},
	"TestGoldenFramePaletteOpen": func(t *testing.T) string {
		m := goldenModel()
		goldenPlugin(m, "p.one", map[protocol.SurfaceID][]string{
			protocol.SurfaceMainStage: {"stage line one"},
		})
		return runGolden(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	},
	"TestGoldenFrameFullSlots": func(t *testing.T) string { return runGolden(t, fullSlotsModel()) },
	"TestGoldenFrameSettingsTheme": func(t *testing.T) string {
		m, _ := settingsFixture(t, "theme")
		return runGolden(t, m)
	},
	"TestGoldenFrameSettingsThemeError": func(t *testing.T) string {
		m, fe := settingsFixture(t, "theme")
		m.settings.cursor = 2
		m.selectSettingsTheme()
		fe.replySetting(0, contracts.SettingSetResult{Reports: []contracts.ReloadReport{{Service: "theme", Problems: []protocol.Problem{{Severity: "error", Code: "low_contrast", Message: "ink on surface is 2.10:1, need 4.5:1"}}}}}, nil)
		return runGolden(t, m)
	},
	"TestGoldenFrameSettingsProfileSelect": func(t *testing.T) string {
		m, _ := settingsFixture(t, "theme")
		m.settings.focus = settingsProfileFocus
		m.openSettingsProfiles()
		return runGolden(t, m)
	},
}

func fullSlotsModel() *WorkbenchModel {
	m := goldenModel()
	goldenPlugin(m, "p.full", map[protocol.SurfaceID][]string{
		protocol.SurfaceSidebarNavigation: {"nav"},
		protocol.SurfaceSidebarList:       {"list"},
		protocol.SurfaceSidebarFooter:     {"foot"},
		protocol.SurfaceMainHeader:        {strings.Repeat("HEADER ", 30)},
		protocol.SurfaceMainStage:         {"stage"},
		protocol.SurfaceSidePanel:         {"SIDEPANEL"},
	})
	return m
}

func TestGoldenFrameSettingsTheme(t *testing.T) {
	frame := goldenFrames[t.Name()](t)
	requireFrameGeometry(t, frame)
	golden.RequireEqual(t, frame)
}

func TestGoldenFrameSettingsThemeError(t *testing.T) {
	frame := goldenFrames[t.Name()](t)
	requireFrameGeometry(t, frame)
	golden.RequireEqual(t, frame)
}

func TestGoldenFrameSettingsProfileSelect(t *testing.T) {
	frame := goldenFrames[t.Name()](t)
	requireFrameGeometry(t, frame)
	golden.RequireEqual(t, frame)
}

func TestGoldenFrameEmptyWorkbench(t *testing.T) {
	frame := goldenFrames[t.Name()](t)
	requireFrameGeometry(t, frame)
	golden.RequireEqual(t, frame)
}

func TestGoldenFrameOnePlugin(t *testing.T) {
	frame := goldenFrames[t.Name()](t)
	requireFrameGeometry(t, frame)
	if !strings.Contains(ansi.Strip(frame), "stage line one") {
		t.Error("plugin content missing from frame")
	}
	golden.RequireEqual(t, frame)
}

func TestGoldenFramePaletteOpen(t *testing.T) {
	frame := goldenFrames[t.Name()](t)
	requireFrameGeometry(t, frame)
	plain := ansi.Strip(frame)
	for _, want := range []string{"COMMAND PALETTE (app-overlay)", "❯ █"} {
		if !strings.Contains(plain, want) {
			t.Errorf("palette frame missing %q", want)
		}
	}
	closed := goldenModel()
	goldenPlugin(closed, "p.one", map[protocol.SurfaceID][]string{
		protocol.SurfaceMainStage: {"stage line one"},
	})
	if frame == runGolden(t, closed) {
		t.Error("ctrl+p did not change the frame: palette not drawn")
	}
	golden.RequireEqual(t, frame)
}

// With every region occupied, the header, stage and side panel must not run
// into each other: a gutter column separates adjacent surfaces.
func TestGoldenFrameFullSlots(t *testing.T) {
	m := fullSlotsModel()
	frame := runGolden(t, m)
	requireFrameGeometry(t, frame)

	hdr, side := m.slotRects[protocol.SurfaceMainHeader], m.slotRects[protocol.SurfaceSidePanel]
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
