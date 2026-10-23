package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestOverlayPlacement(t *testing.T) {
	const marker = "OVERLAY_TOO_SMALL_MARK"
	for _, mode := range []contracts.RailPlacement{"", contracts.PlacementFullCanvas, contracts.PlacementStage} {
		for _, size := range [][2]int{{144, 40}, {84, 24}, {200, 60}} {
			for _, occupied := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%dx%d/occupied=%v", mode, size[0], size[1], occupied), func(t *testing.T) {
					m := goldenModel()
					m.width, m.height = size[0], size[1]
					m.activePlacement = mode
					if occupied {
						addPlugin(m, "annotation", map[protocol.SurfaceID][]string{protocol.SurfaceAppOverlay: {marker}})
					}
					m.catalog.Frames["annotation"] = map[protocol.SurfaceID]protocol.FrameSpec{protocol.SurfaceAppOverlay: {Border: true, Label: "ignored", Close: true}}
					for _, demo := range []bool{false, true} {
						m.demoFrame = demo
						m.recomputeLayout()
						if got := m.frameFor(protocol.SurfaceAppOverlay); got != (frame.Spec{}) {
							t.Fatalf("frame = %+v", got)
						}
						want := SlotRect{Width: size[0], Height: size[1], InnerW: size[0], InnerH: size[1]}
						if got := m.slotRects[protocol.SurfaceAppOverlay]; got != want {
							t.Fatalf("rect = %+v, want %+v", got, want)
						}
						var geo struct {
							Rects map[protocol.SurfaceID]geoRect `json:"rects"`
						}
						if err := json.Unmarshal(m.geometryJSON(), &geo); err != nil {
							t.Fatal(err)
						}
						if geo.Rects[protocol.SurfaceAppOverlay] != toGeo(want) {
							t.Fatal("overlay absent from geometry")
						}
						if !slices.Contains(m.surfaceView().Visible, protocol.SurfaceAppOverlay) {
							t.Fatal("overlay absent from visible")
						}
						if occupied && !strings.Contains(ansi.Strip(m.View().Content), marker) {
							t.Fatal("overlay marker missing before shrinking")
						}
						m.width, m.height = 40, 10
						m.recomputeLayout()
						if m.slotRects[protocol.SurfaceAppOverlay] != want {
							t.Fatal("small terminal changed frozen overlay rect")
						}
						if strings.Contains(ansi.Strip(m.View().Content), marker) {
							t.Fatal("overlay drawn on too-small screen")
						}
						m.width, m.height = size[0], size[1]
					}
				})
			}
		}
	}
}

func TestOverlayZOrder(t *testing.T) {
	m := goldenModel()
	addPlugin(m, "stage", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"stage"}, protocol.SurfaceModal: {"modal"}})
	m.modal = modalState{PluginID: "stage", RailID: "stage.modal"}
	m.recomputeLayout()
	rows := make([]string, m.height)
	for y := range rows {
		rows[y] = strings.Repeat("O", m.width)
	}
	addPlugin(m, "annotation", map[protocol.SurfaceID][]string{protocol.SurfaceAppOverlay: rows})
	overlay := frameCells(m.View().Content)
	for y := range overlay {
		for x := range overlay[y] {
			if overlay[y][x].Content != "O" {
				t.Fatalf("overlay below modal/shadow/bar at %d,%d", x, y)
			}
		}
	}
	// Compare each host popup against its rendering without the plugin layer:
	// popup glyphs must still own their cells while every surrounding cell is O.
	m.activeToast = &contracts.Toast{Title: "NOTICE", Message: "topmost toast"}
	m.showPalette = true
	m.secretPrompt = &secretPromptState{prompt: secrets.Prompt{Plugin: "stage", Name: "token", Purpose: "test confirmation"}}
	got := frameCells(m.View().Content)
	delete(m.catalog.Occupants, protocol.SurfaceAppOverlay)
	base := frameCells(m.View().Content)
	for _, text := range []string{"NOTICE", "COMMAND PALETTE", "SECRET ACCESS REQUEST"} {
		found := false
		for y := range base {
			var row strings.Builder
			for _, c := range base[y] {
				row.WriteString(c.Content)
			}
			x := strings.Index(row.String(), text)
			if x < 0 {
				continue
			}
			found = true
			for i := range text {
				if !got[y][x+i].Equal(&base[y][x+i]) {
					t.Fatalf("popup %s covered at %d,%d", text, x+i, y)
				}
			}
		}
		if !found {
			t.Fatalf("missing popup %s", text)
		}
	}
}

func TestOverlayNotInteractive(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	addPlugin(m, "stage", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"stage"}})
	addPlugin(m, "annotation", map[protocol.SurfaceID][]string{protocol.SurfaceAppOverlay: {"overlay"}})
	m.catalog.Capture[protocol.SurfaceAppOverlay] = true
	m.catalog.Frames["annotation"] = map[protocol.SurfaceID]protocol.FrameSpec{protocol.SurfaceAppOverlay: {Border: true, Close: true}}
	addSlot(m, "annotation.region", "annotation", "region", &contracts.PublishedRect{Surface: protocol.SurfaceAppOverlay, X: 0, Y: 0, Width: m.width, Height: m.height})
	contribute(m, "annotation.region", contribution{PluginID: "annotation", ID: "click", Content: "region", OnClick: "annotation.click"})
	for _, hit := range m.hitOrder() {
		if hit.ID == protocol.SurfaceAppOverlay {
			t.Fatal("overlay is hit-tested")
		}
	}
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if m.onBorderRing(x, y) {
				t.Fatalf("overlay border hit at %d,%d", x, y)
			}
			if _, ok := m.closeHitAt(x, y); ok {
				t.Fatalf("overlay close hit at %d,%d", x, y)
			}
			// Rail cells can open host UI; begin every coordinate with the same
			// unobstructed stage so those popups do not consume later clicks.
			m.showPalette = false
			m.closeSettings()
			m.stageMode = ""
			before := len(fe.Mice)
			m.handleMouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			r := m.slotRects[protocol.SurfaceMainStage]
			if x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height {
				if len(fe.Mice) != before+1 || fe.Mice[len(fe.Mice)-1].Plugin != "stage" {
					t.Fatalf("stage missed click at %d,%d", x, y)
				}
			}
			m.focusNext()
			if m.focusedSlot == protocol.SurfaceAppOverlay {
				t.Fatal("overlay took focus")
			}
		}
	}
	m.closeSettings() // the final bottom-bar coordinate can open the center
	m.setFocus(protocol.SurfaceMainStage)
	press(m, "z")
	if len(fe.Keys) == 0 || fe.Keys[len(fe.Keys)-1].Plugin != "stage" {
		t.Fatal("key did not reach stage")
	}
	for _, call := range fe.Invoked {
		if call.Action == "annotation.click" {
			t.Fatal("overlay contribution clicked")
		}
	}
	for _, mouse := range fe.Mice {
		if mouse.Plugin == "annotation" {
			t.Fatal("overlay received mouse")
		}
	}
	for _, key := range fe.Keys {
		if key.Plugin == "annotation" {
			t.Fatal("overlay received key")
		}
	}
	if m.keyContext().Capture {
		t.Fatal("overlay capture affected stage")
	}
}

func TestOverlayClearedOnDeparture(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.pods = map[string]string{"annotation": "live"}
	fe.occ = map[protocol.SurfaceID]string{protocol.SurfaceAppOverlay: "annotation"}
	fe.publish()
	m.refresh()
	setContent(m, "annotation", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceAppOverlay, Lines: []string{"OVERLAY_MARK"}})
	if !strings.Contains(ansi.Strip(m.View().Content), "OVERLAY_MARK") {
		t.Fatal("live overlay missing")
	}
	fe.pods["annotation"] = "exited"
	delete(fe.occ, protocol.SurfaceAppOverlay)
	fe.publish()
	m.refresh()
	if m.slotUpdates[protocol.SurfaceAppOverlay] != nil {
		t.Fatal("departed frame retained")
	}
	if strings.Contains(ansi.Strip(m.View().Content), "OVERLAY_MARK") {
		t.Fatal("departed overlay rendered")
	}
}

func TestGoldenFrameOverlay(t *testing.T) {
	m := goldenModel()
	const stageContent = "stage content beneath annotation"
	goldenPlugin(m, "stage", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {stageContent}})
	stage := m.slotRects[protocol.SurfaceMainStage]
	x0, y0 := stage.InnerX, stage.InnerY
	rows := make([]string, m.height)
	style := lipgloss.NewStyle().Foreground(m.palette.Accent)
	// Paint the label row over the occupied stage's first content row.
	rows[y0] = strings.Repeat(" ", x0) + style.Render("┌ Overlay ───────┐")
	rows[y0+1] = strings.Repeat(" ", x0) + style.Render("│") + strings.Repeat(" ", 16) + style.Render("│")
	rows[y0+2] = strings.Repeat(" ", x0) + style.Render("└────────────────┘")
	goldenPlugin(m, "annotation", map[protocol.SurfaceID][]string{protocol.SurfaceAppOverlay: rows})
	// Keep plugin and traffic counters identical: only the layer's pixels change.
	delete(m.catalog.Occupants, protocol.SurfaceAppOverlay)
	before := runGolden(t, m)
	m.catalog.Occupants[protocol.SurfaceAppOverlay] = "annotation"
	after := runGolden(t, m)
	requireFrameGeometry(t, after)
	base, over := frameCells(before), frameCells(after)
	for i, ch := range stageContent {
		if base[y0][x0+i].Content != string(ch) {
			t.Fatalf("stage content missing beneath label at %d,%d", x0+i, y0)
		}
	}
	stageBg := m.palette.Slot
	scratch := newCanvas(m.width, m.height)
	defer scratch.release()
	scratch.blitLines(0, 0, m.width, m.height, rows)
	for y := range base {
		for x := range base[y] {
			cell := scratch.buf.CellAt(x, y)
			if cell.Equal(&uv.EmptyCell) {
				// Serializing a single cell pins bytes as well as decoded appearance.
				if (uv.Line{base[y][x]}).Render() != (uv.Line{over[y][x]}).Render() {
					t.Fatalf("transparent cell changed at %d,%d", x, y)
				}
			} else {
				if x < stage.InnerX || x >= stage.InnerX+stage.InnerW || y < stage.InnerY || y >= stage.InnerY+stage.InnerH {
					t.Fatalf("painted cell outside stage at %d,%d", x, y)
				}
				if over[y][x].Content != cell.Content || !sameColor(over[y][x].Style.Fg, cell.Style.Fg) {
					t.Fatalf("painted cell missing at %d,%d", x, y)
				}
				if !sameColor(base[y][x].Style.Bg, stageBg) || !sameColor(over[y][x].Style.Bg, stageBg) {
					t.Fatalf("painted cell lost stage background at %d,%d", x, y)
				}
			}
		}
	}
	golden.RequireEqual(t, after)
}
