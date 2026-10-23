package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"image/color"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
)

func TestChromeColourDiscipline(t *testing.T) {
	t.Run("source roles", func(t *testing.T) {
		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatal(err)
		}
		counts := make(map[string]map[string]int)
		bad := false
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				name := fn.Name.Name
				ast.Inspect(fn, func(n ast.Node) bool {
					role, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					palette, ok := role.X.(*ast.SelectorExpr)
					if !ok || palette.Sel.Name != "palette" {
						return true
					}
					if counts[name] == nil {
						counts[name] = make(map[string]int)
					}
					r := role.Sel.Name
					counts[name][r]++
					switch r {
					case "AccentSecondary", "AccentSoft":
						bad = true
					case "Success", "Warning", "Danger", "Info":
						bad = bad || (name != "drawToast" && name != "statusMark")
					case "Accent":
						bad = bad || (name != "renderActivityRailBox" && name != "drawCommandPalette" && name != "dividerEdgeStyles")
					}
					return true
				})
			}
		}
		if bad {
			t.Fatalf("chrome role discipline violated; references per function: %v", counts)
		}
		for _, fn := range []string{"renderActivityRailBox", "drawCommandPalette", "dividerEdgeStyles"} {
			if counts[fn]["Accent"] != 1 {
				t.Errorf("want one accent reference in %s; counts: %v", fn, counts)
			}
		}
	})
	t.Run("empty frame accent only on active tile", func(t *testing.T) {
		m := goldenModel()
		rail := m.slotRects["activity-rail"]
		active := 0
		for _, b := range m.railButtons() {
			if b.active {
				active++
				for y, row := range frameCells(m.View().Content) {
					for x, cell := range row {
						want := x >= rail.X+1 && x < rail.X+6 && y == rail.Y+b.startY+1
						if got := sameColor(cell.Style.Fg, lipgloss.Color(m.palette.Accent)); got != want {
							t.Errorf("accent at %d,%d = %t, want %t", x, y, got, want)
						}
					}
				}
			}
		}
		if active != 1 {
			t.Fatalf("active tiles = %d, want 1", active)
		}
	})
}

func TestSelectedChromeNeverUsesSubtleInk(t *testing.T) {
	for _, mode := range []string{"palette", "explorer"} {
		t.Run(mode, func(t *testing.T) {
			m := goldenModel()
			if mode == "palette" {
				m.showPalette = true
			} else {
				m.stageMode = "hotspots"
				// Keep the owner column visible, not clipped behind the docks.
				m.showSidebar, m.showSidePane = false, false
				m.recomputeLayout()
			}
			selected := 0
			for y, row := range frameCells(m.View().Content) {
				for x, cell := range row {
					if sameColor(cell.Style.Bg, lipgloss.Color(m.palette.SurfaceSelected)) {
						selected++
						if sameColor(cell.Style.Fg, lipgloss.Color(m.palette.InkSubtle)) {
							t.Errorf("subtle ink on selected surface at %d,%d", x, y)
						}
					}
				}
			}
			if selected == 0 {
				t.Fatal("no selected cells rendered")
			}
		})
	}
}

// requireChromeText checks every cell of a specific visible text run.
func requireChromeText(t *testing.T, cells uv.Lines, text string, fg color.Color, bold bool) {
	t.Helper()
	for _, row := range cells {
		var plain strings.Builder
		for _, cell := range row {
			plain.WriteString(cell.Content)
		}
		if start := strings.Index(plain.String(), text); start >= 0 {
			// The fixtures use one-cell glyphs; count runes, not UTF-8 bytes.
			x := len([]rune(plain.String()[:start]))
			for _, cell := range row[x : x+len([]rune(text))] {
				if !sameColor(cell.Style.Fg, fg) || (cell.Style.Attrs&uv.AttrBold != 0) != bold {
					t.Errorf("%q cell %q has style %+v, want foreground %v bold %t", text, cell.Content, cell.Style, fg, bold)
				}
			}
			return
		}
	}
	t.Fatalf("text %q not rendered", text)
}

func TestToastBorderNeutralAndTitleStatus(t *testing.T) {
	m := goldenModel()
	for _, tc := range []struct{ level, role string }{
		{"info", m.palette.Info}, {"success", m.palette.Success}, {"warn", m.palette.Warning}, {"error", m.palette.Danger},
	} {
		t.Run(tc.level, func(t *testing.T) {
			m.activeToast = &contracts.Toast{Level: tc.level, Title: "Toast title", Message: "Toast message"}
			c := newCanvas(m.width, m.height)
			defer c.release()
			m.drawToast(c)
			cells := frameCells(c.render())
			requireChromeText(t, cells, "● Toast title", lipgloss.Color(tc.role), true)
			requireChromeText(t, cells, "Toast message", lipgloss.Color(m.palette.Ink), false)
			borders, backgrounds := 0, 0
			for _, row := range cells {
				for _, cell := range row {
					if strings.ContainsAny(cell.Content, "╭╮╰╯─│") {
						borders++
						if !sameColor(cell.Style.Fg, lipgloss.Color(m.palette.Border)) {
							t.Errorf("toast border is not border: %+v", cell.Style)
						}
					}
					if cell.Style.Bg != nil {
						backgrounds++
						if !sameColor(cell.Style.Bg, lipgloss.Color(m.palette.SurfaceElevated)) {
							t.Errorf("toast background is not elevated: %+v", cell.Style)
						}
					}
				}
			}
			if borders == 0 || backgrounds == 0 {
				t.Fatal("toast border or background missing")
			}
		})
	}
}

func TestSecretPromptChromeRoles(t *testing.T) {
	m := goldenModel()
	openPrompt(m, 1, askPrompt)
	m.secretPrompt.armed = true
	c := newCanvas(m.width, m.height)
	defer c.release()
	r := m.drawSecretPrompt(c)
	cells := frameCells(c.render())
	requireChromeText(t, cells, "⚠", lipgloss.Color(m.palette.Warning), false)
	for _, text := range []string{"SECRET ACCESS REQUEST", askPrompt.Plugin} {
		requireChromeText(t, cells, text, lipgloss.Color(m.palette.Ink), true)
	}
	foundHints := false
	for _, row := range cells {
		var plain strings.Builder
		for _, cell := range row {
			plain.WriteString(cell.Content)
		}
		if strings.Contains(plain.String(), "ask again later") {
			foundHints = true
			for _, key := range []string{"y", "n", "esc"} {
				requireChromeText(t, uv.Lines{row}, key, lipgloss.Color(m.palette.Ink), true)
			}
		}
	}
	if !foundHints {
		t.Fatal("secret prompt hints missing")
	}
	if !sameColor(c.buf.CellAt(r.Min.X, r.Min.Y).Style.Fg, lipgloss.Color(m.palette.BorderFocused)) {
		t.Fatal("secret prompt border not focused border")
	}
	for y := r.Min.Y + 1; y < r.Max.Y-1; y++ {
		for x := r.Min.X + 1; x < r.Max.X-1; x++ {
			if !sameColor(c.buf.CellAt(x, y).Style.Bg, lipgloss.Color(m.palette.SurfaceElevated)) {
				t.Fatalf("secret prompt background not elevated at %d,%d", x, y)
			}
		}
	}
}

func TestStatusMarkLevels(t *testing.T) {
	m := goldenModel()
	for _, tc := range []struct{ level, role string }{
		{"info", m.palette.Info}, {"success", m.palette.Success}, {"warning", m.palette.Warning}, {"danger", m.palette.Danger},
	} {
		t.Run(tc.level, func(t *testing.T) {
			requireChromeText(t, frameCells(m.statusMark(tc.level, "●")), "●", lipgloss.Color(tc.role), false)
		})
	}
}
