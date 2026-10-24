package workbench

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestRailTileGeometry(t *testing.T) {
	for _, h := range []int{3, 4, 7, 8, 11, 14, 15, 22, 38} {
		t.Run(fmt.Sprint(h), func(t *testing.T) {
			m := goldenModel()
			m.slotRects[protocol.SurfaceActivityRail] = SlotRect{X: 0, Y: 1, Width: activityRailWidth, Height: h}
			m.modifiers.Alt = true
			if activityRailWidth != 7 {
				t.Fatalf("rail width %d, want 7", activityRailWidth)
			}
			btns := m.railButtons()
			rows := strings.Split(m.renderActivityRailBox(), "\n")
			c := newCanvas(7, h)
			defer c.release()
			c.blitBlock(0, 0, m.renderActivityRailBox())
			for i, b := range btns {
				start := 1 + 4*i
				if b.kind == "palette" && h >= start+6 {
					start = h - 4
				}
				if b.startY != start || b.endY != start+2 {
					t.Fatalf("button %d rows %d..%d, want %d..%d", i, b.startY, b.endY, start, start+2)
				}
				for y := max(0, b.startY); y < min(h, b.endY+1); y++ {
					for x := 0; x < 7; x++ {
						want := -1
						if b.endY < h {
							want = i
						}
						if got := m.detectRailHoverAt(x, y+1); got != want {
							t.Errorf("hit %d,%d = %d, want %d", x, y, got, want)
						}
						bg := m.palette.Surface
						if b.endY < h && x > 0 && x < 6 {
							bg = m.palette.SurfaceElevated
							if b.active {
								bg = m.palette.SurfaceSelected
							}
						}
						if !sameColor(c.buf.CellAt(x, y).Style.Bg, bg) {
							t.Errorf("wrong tile background %d,%d", x, y)
						}
					}
				}
			}
			for _, row := range rows {
				if ansi.StringWidth(row) != 7 || strings.ContainsAny(ansi.Strip(row), "▄▀") {
					t.Fatalf("not a 7-cell whole-cell row: %q", row)
				}
			}
			if m.detectRailHoverAt(0, 1) != -1 || m.detectRailHoverAt(7, 3) != -1 {
				t.Fatal("hit outside tile")
			}
		})
	}
}

func TestRailAltBadgeCentredAndClipped(t *testing.T) {
	for _, tc := range []struct{ key, icon, want string }{
		{"ctrl+p", "x", "  ^P "}, {"x", "x", "  x  "}, {"ctrl+shift+x", "x", "Ctrl…"}, {"", "界", "  界 "},
	} {
		t.Run(tc.key+tc.icon, func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			m.railItems = []contracts.RailItem{{ID: "p.open", Plugin: "p", Icon: tc.icon}}
			if tc.key != "" {
				fe.bind(tc.key, "p.open")
			}
			m.modifiers.Alt = true
			b := m.railButtons()[1]
			rows := strings.Split(m.renderActivityRailBox(), "\n")
			if got := ansi.Strip(rows[b.startY+1]); got != " "+tc.want+" " {
				t.Fatalf("badge %q, want %q", got, " "+tc.want+" ")
			}
			c := newCanvas(7, 38)
			defer c.release()
			c.blitBlock(0, 0, m.renderActivityRailBox())
			if !sameColor(c.buf.CellAt(3, b.startY+1).Style.Fg, m.palette.Ink) {
				t.Fatal("badge not ink coloured")
			}
		})
	}
}

func TestRailAltIconAndBadgeRoles(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, key := range []string{"", "k"} {
			t.Run(fmt.Sprintf("active=%t/key=%q", active, key), func(t *testing.T) {
				m := goldenModel()
				fe := withFE(m)
				m.railItems = []contracts.RailItem{{ID: "p.open", Plugin: "p", Icon: "x"}}
				if active {
					m.activeRailID = "p.open"
				}
				if key != "" {
					fe.bind(key, "p.open")
				}
				m.modifiers.Alt = true
				fg, bg, text := m.palette.Ink, m.palette.SurfaceElevated, "x"
				if active {
					bg = m.palette.SurfaceSelected
					if key == "" {
						fg = m.palette.Accent
					}
				}
				if key != "" {
					text = keyLabel(key)
				}
				b := m.railButtons()[1]
				rows := strings.Split(m.renderActivityRailBox(), "\n")
				requireChromeText(t, frameCells(rows[b.startY+1]), text, fg, true, bg)
			})
		}
	}
}

func TestRailIdleIconDoesNotShiftOnHover(t *testing.T) {
	for _, icon := range []string{"x", "界"} {
		t.Run(icon, func(t *testing.T) {
			m := goldenModel()
			m.railItems = []contracts.RailItem{{ID: "p.open", Plugin: "p", Icon: icon}}
			b := m.railButtons()[1]
			idle := strings.Split(m.renderActivityRailBox(), "\n")[b.startY+1]
			m.hoverRailIdx = 1
			hovered := strings.Split(m.renderActivityRailBox(), "\n")[b.startY+1]
			if ansi.Strip(idle) != ansi.Strip(hovered) || ansi.Strip(idle) != "   "+icon+strings.Repeat(" ", 4-ansi.StringWidth(icon)) {
				t.Fatalf("idle %q, hover %q", ansi.Strip(idle), ansi.Strip(hovered))
			}
			c := newCanvas(7, 38)
			defer c.release()
			c.blitBlock(0, 0, m.renderActivityRailBox())
			if !sameColor(c.buf.CellAt(3, b.startY+1).Style.Bg, m.palette.SurfaceElevated) || !sameColor(c.buf.CellAt(3, b.startY+1).Style.Fg, m.palette.Ink) {
				t.Fatal("hover colours")
			}
		})
	}
}

func TestBottomBarLabelHintsAndThemeFit(t *testing.T) {
	for _, w := range []int{84, 100, 144} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			m := goldenModel()
			m.width = w
			m.recomputeLayout()
			bar := ansi.Strip(m.renderBottomKeybindBar())
			label := " " + m.layoutLabel() + " "
			pill := " ✦ charm "
			if ansi.StringWidth(bar) != w || !strings.HasPrefix(bar, label) || !strings.HasSuffix(bar, pill) {
				t.Fatalf("bar %q", bar)
			}
			left := label
			dropped := false
			for _, h := range m.footerHints() {
				group := " │ " + h.key + " " + h.desc
				if dropped || ansi.StringWidth(left+group)+1+ansi.StringWidth(pill) > w {
					dropped = true
					continue
				}
				left += group
			}
			want := left + strings.Repeat(" ", w-ansi.StringWidth(left)-ansi.StringWidth(pill)) + pill
			if bar != want {
				t.Fatalf("got %q\nwant %q", bar, want)
			}
			if strings.TrimSpace(ansi.Strip(ansi.Cut(bar, w-ansi.StringWidth(pill)-1, w-ansi.StringWidth(pill)))) != "" {
				t.Fatal("no blank before pill")
			}
		})
	}
}

func TestThemePillName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"charm", "charm"}, {"evening", "evening"}, {"", "theme"}, {strings.Repeat("界", 15), strings.Repeat("界", 11) + "…"}, {strings.Repeat("a", 30), strings.Repeat("a", 23) + "…"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			m := goldenModel()
			m.palette.Name = tc.in
			if got := themePillName(tc.in); got != tc.want {
				t.Fatalf("name %q, want %q", got, tc.want)
			}
			pill := " ✦ " + tc.want + " "
			rect := themePillRect(m.width, m.height, tc.in)
			bar := m.renderBottomKeybindBar()
			if got := ansi.Strip(ansi.Cut(bar, rect.Min.X, rect.Max.X)); got != pill || rect.Dx() != ansi.StringWidth(pill) || rect.Min.Y != m.height-1 || rect.Dy() != 1 {
				t.Fatalf("pill %q at %v, want %q", got, rect, pill)
			}
		})
	}
}

func TestThemePillClickIsConsumed(t *testing.T) {
	for _, modal := range []bool{false, true} {
		for _, pos := range []string{"first", "middle", "last"} {
			t.Run(fmt.Sprintf("%t/%s", modal, pos), func(t *testing.T) {
				m := goldenModel()
				fe := withFE(m)
				addPlugin(m, "p", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"stage"}, protocol.SurfaceModal: {"modal"}})
				if modal {
					m.openModal("p", "p.open")
				}
				// Overlap a target deliberately to prove pill precedence rather than
				// relying on today's region layout stopping above the bottom bar.
				r := SlotRect{X: 0, Y: m.height - 1, Width: m.width, Height: 1, InnerX: 0, InnerY: m.height - 1, InnerW: m.width, InnerH: 1}
				m.slotRects[protocol.SurfaceMainStage] = r
				if modal {
					m.modalRect = r
				}
				pill := themePillRect(m.width, m.height, m.palette.Name)
				x := pill.Min.X
				if pos == "middle" {
					x += pill.Dx() / 2
				}
				if pos == "last" {
					x = pill.Max.X - 1
				}
				focus := m.focusedSlot
				fe.Mice = nil
				fe.Invoked = nil
				_, cmd := m.handleMouse(tea.MouseClickMsg{X: x, Y: m.height - 1, Button: tea.MouseLeft})
				if cmd != nil || len(fe.Mice) != 0 || len(fe.Invoked) != 1 || m.focusedSlot != focus || m.activeDrag != "" || m.activeToast != nil {
					t.Fatal("pill click did not exclusively invoke settings")
				}
				if call := fe.Invoked[0]; call.Action != "husk.settings.open" || call.Args["tab"] != "theme" {
					t.Fatalf("pill action = %+v", call)
				}
				// Close the host popup before probing the adjacent surface cell.
				m.closeSettings()
				m.handleMouse(tea.MouseClickMsg{X: pill.Min.X - 1, Y: m.height - 1, Button: tea.MouseLeft})
				if len(fe.Mice) != 1 {
					t.Fatalf("adjacent cell consumed: mouse calls %d", len(fe.Mice))
				}
			})
		}
	}
}

func TestDumpFrameActivityTracksContent(t *testing.T) {
	m := goldenModel()
	d := dumpFrameDriver{WorkbenchModel: m}
	m.content("p", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Title: "one", Lines: []string{"first"}})
	prior := d.activity()
	for _, change := range []func(){
		func() { m.slotUpdates[protocol.SurfaceMainStage].PluginID = "q" },
		func() { m.slotUpdates[protocol.SurfaceMainStage].Title = "two" },
		func() { m.slotUpdates[protocol.SurfaceMainStage].Lines[0] = "second" },
		func() { m.podStatus["p"] = contracts.PodExited },
	} {
		change()
		next := d.activity()
		if next == prior {
			t.Fatal("activity ignored content or status change")
		}
		if next != d.activity() {
			t.Fatal("unstable fingerprint")
		}
		prior = next
	}
}
