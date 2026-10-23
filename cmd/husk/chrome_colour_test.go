package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"image/color"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// auditChromeRoles keeps drawing-role reads explicit and attributes each one
// to its receiver-qualified method or package initializer, including literals.
func auditChromeRoles(f *ast.File) (map[string]map[string]int, []string) {
	counts := make(map[string]map[string]int)
	var violations []string
	checkAliases := func(name string, values []ast.Expr) {
		for _, value := range values {
			if sel, ok := ast.Unparen(value).(*ast.SelectorExpr); ok && sel.Sel.Name == "palette" {
				violations = append(violations, name+": palette alias hides role reads")
			}
		}
	}
	walk := func(name string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				checkAliases(name, n.Rhs)
			case *ast.ValueSpec:
				checkAliases(name, n.Values)
			case *ast.SelectorExpr:
				palette, ok := ast.Unparen(n.X).(*ast.SelectorExpr)
				if !ok || palette.Sel.Name != "palette" || n.Sel.Name == "Name" {
					return true
				}
				if counts[name] == nil {
					counts[name] = make(map[string]int)
				}
				role := n.Sel.Name
				counts[name][role]++
				allowed := true
				switch role {
				case "AccentSecondary", "AccentSoft":
					allowed = false
				case "Success", "Warning", "Danger", "Info":
					allowed = name == "(*WorkbenchModel).drawToast" || name == "(*WorkbenchModel).statusMark"
				case "Accent":
					allowed = name == "(*WorkbenchModel).renderActivityRailBox" || name == "(*WorkbenchModel).drawCommandPalette" || name == "(*WorkbenchModel).dividerEdgeStyles" || name == "(*WorkbenchModel).drawSettings" || name == "(*WorkbenchModel).cacheSettingsGeneral"
				}
				if !allowed {
					violations = append(violations, fmt.Sprintf("%s: forbidden %s", name, role))
				}
			}
			return true
		})
	}
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			name := decl.Name.Name
			if decl.Recv != nil {
				var receiver strings.Builder
				if err := format.Node(&receiver, token.NewFileSet(), decl.Recv.List[0].Type); err != nil {
					violations = append(violations, "unreadable receiver: "+err.Error())
				}
				name = "(" + receiver.String() + ")." + name
			}
			walk(name, decl)
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				if value, ok := spec.(*ast.ValueSpec); ok {
					var names []string
					for _, id := range value.Names {
						names = append(names, id.Name)
					}
					walk("package initializer "+strings.Join(names, ","), value)
				}
			}
		}
	}
	return counts, violations
}

func TestChromeRoleAuditRejectsBypasses(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		bad          bool
	}{
		{"assignment alias", `func (m *WorkbenchModel) draw() { p := m.palette; _ = p.Warning }`, true},
		{"declaration alias", `func (m *WorkbenchModel) draw() { var p = m.palette; _ = p.Warning }`, true},
		{"package alias", `var p = m.palette`, true},
		{"foreign toast", `func (m *Other) drawToast() { _ = m.palette.Warning }`, true},
		{"foreign status mark", `func (m *Other) statusMark() { _ = m.palette.Success }`, true},
		{"foreign rail", `func (m *Other) renderActivityRailBox() { _ = m.palette.Accent }`, true},
		{"foreign settings", `func (m *Other) drawSettings() { _ = m.palette.Accent }`, true},
		{"host settings focus", `func (m *WorkbenchModel) drawSettings() { _ = m.palette.Accent }`, false},
		{"host General group focus", `func (m *WorkbenchModel) cacheSettingsGeneral() { _ = m.palette.Accent }`, false},
		{"foreign General group focus", `func (m *Other) cacheSettingsGeneral() { _ = m.palette.Accent }`, true},
		{"bare function", `func drawToast(m *WorkbenchModel) { _ = m.palette.Warning }`, true},
		{"package literal", `var f = func(m *WorkbenchModel) { _ = m.palette.Warning }`, true},
		{"package literal alias", `var f = func(m *WorkbenchModel) { p := m.palette; _ = p.Warning }`, true},
		{"secondary accent", `func (m *WorkbenchModel) drawToast() { _ = m.palette.AccentSecondary }`, true},
		{"soft accent", `func (m *WorkbenchModel) drawToast() { _ = m.palette.AccentSoft }`, true},
		{"host toast", `func (m *WorkbenchModel) drawToast() { _ = m.palette.Warning }`, false},
		{"host status mark", `func (m *WorkbenchModel) statusMark() { _ = m.palette.Success }`, false},
		{"neutral literal", `var f = func(m *WorkbenchModel) { _ = m.palette.Ink }`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "chrome.go", "package main\n"+tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			counts, violations := auditChromeRoles(f)
			if got := len(violations) > 0; got != tc.bad {
				t.Fatalf("violations %v, counts %v; want rejected %t", violations, counts, tc.bad)
			}
		})
	}
}

func TestChromeColourDiscipline(t *testing.T) {
	t.Run("source roles", func(t *testing.T) {
		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatal(err)
		}
		counts := make(map[string]map[string]int)
		var violations []string
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			fileCounts, bad := auditChromeRoles(f)
			violations = append(violations, bad...)
			for fn, roles := range fileCounts {
				if counts[fn] == nil {
					counts[fn] = make(map[string]int)
				}
				for role, n := range roles {
					counts[fn][role] += n
				}
			}
		}
		if len(violations) > 0 {
			t.Fatalf("chrome role discipline violated: %v; references per function: %v", violations, counts)
		}
		for _, fn := range []string{"renderActivityRailBox", "drawCommandPalette", "dividerEdgeStyles"} {
			name := "(*WorkbenchModel)." + fn
			if counts[name]["Accent"] != 1 {
				t.Errorf("want one accent reference in %s; counts: %v", name, counts)
			}
		}
		if counts["(*WorkbenchModel).drawSettings"]["Accent"] != 3 {
			t.Errorf("want three settings accent reads: focused corner, focused tabs and current theme")
		}
		if counts["(*WorkbenchModel).cacheSettingsGeneral"]["Accent"] != 1 {
			t.Errorf("want one General accent read for the focused plugin group")
		}
	})
	t.Run("empty frame accent only on active tile", func(t *testing.T) {
		m := goldenModel()
		rail := m.slotRects[protocol.SurfaceActivityRail]
		cells := frameCells(m.View().Content)
		acc := m.palette.Accent
		active := 0
		for _, b := range m.railButtons() {
			if b.active {
				active++
				for y, row := range cells {
					for x, cell := range row {
						want := x >= rail.X+1 && x < rail.X+6 && y == rail.Y+b.startY+1
						got := sameColor(cell.Style.Fg, acc) || sameColor(cell.Style.Bg, acc) || sameColor(cell.Style.UnderlineColor, acc)
						if got != want {
							t.Errorf("accent at %d,%d = %t, want %t", x, y, got, want)
						}
						if sameColor(cell.Style.Bg, acc) {
							t.Errorf("accent background at %d,%d; active tile accent must be foreground only", x, y)
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

func TestChromeManipulationRoles(t *testing.T) {
	for _, name := range []string{"hover edge", "drag edge", "drag pill", "hover pill", "Alt pill", "focused label", "unfocused label", "hotspot", "palette header", "palette shortcut"} {
		t.Run(name, func(t *testing.T) {
			m := goldenModel()
			c := newCanvas(m.width, m.height)
			defer c.release()
			text, fg, bold := "", m.palette.Ink, true
			var bg color.Color
			slot := protocol.SurfaceMainStage
			r := m.slotRects[slot]
			switch name {
			case "hover edge", "drag edge":
				m.hoverDivider = "sidebar-width"
				if name == "drag edge" {
					m.activeDrag = m.hoverDivider
					fg = m.palette.Accent
				}
				m.drawChrome(c, slot, r, frame.Spec{Border: true}, "")
				text = "┃"
			case "drag pill":
				m.activeDrag = "sidebar-width"
				text, bg = "⇄ Dragging sidebar-width", m.palette.SurfaceSelected
				c.blitBlock(0, 0, m.renderTopWorkbenchBar())
			case "hover pill":
				m.hoverDivider = "sidebar-width"
				text, bg = "⇄ Drag border: sidebar-width", m.palette.SurfaceElevated
				c.blitBlock(0, 0, m.renderTopWorkbenchBar())
			case "Alt pill":
				m.modifiers.Alt = true
				text, bg = "⌥ ALT HELD · Shortcuts Active", m.palette.SurfaceElevated
				c.blitBlock(0, 0, m.renderTopWorkbenchBar())
			case "focused label", "unfocused label":
				text = "Borderless label"
				fg = m.palette.InkMuted
				if name == "focused label" {
					m.focusedSlot = slot
					fg = m.palette.Ink
				}
				m.drawChrome(c, slot, r, frame.Spec{Label: text}, text)
			case "hotspot":
				m.showHotspots = true
				text, bg = "⑥ main-stage", m.palette.SurfaceSelected
				m.drawSurface(c, slot, "⑥", string(slot))
			case "palette header":
				text, fg = "⑧ COMMAND PALETTE (app-overlay)", m.palette.InkMuted
				m.drawCommandPalette(c)
			case "palette shortcut":
				cmd := paletteCommand{Title: "Example", Shortcut: "ctrl+x"}
				text, fg, bold = cmd.Shortcut, m.palette.InkMuted, false
				c.blitBlock(0, 0, m.paletteRow(cmd, false, 78, paletteColumns([]paletteCommand{cmd}, 78)))
			}
			cells := frameCells(c.render())
			var background []color.Color
			if bg != nil {
				background = append(background, bg)
			}
			requireChromeText(t, cells, text, fg, bold, background...)
		})
	}
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
					if sameColor(cell.Style.Bg, m.palette.SurfaceSelected) {
						selected++
						if sameColor(cell.Style.Fg, m.palette.InkSubtle) {
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
func requireChromeText(t *testing.T, cells uv.Lines, text string, fg color.Color, bold bool, bg ...color.Color) {
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
				if len(bg) > 0 && !sameColor(cell.Style.Bg, bg[0]) {
					t.Errorf("%q cell %q background %v, want %v", text, cell.Content, cell.Style.Bg, bg[0])
				}
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
	for _, tc := range []struct {
		level string
		role  color.Color
	}{
		{"info", m.palette.Info}, {"success", m.palette.Success}, {"warn", m.palette.Warning}, {"error", m.palette.Danger},
	} {
		t.Run(tc.level, func(t *testing.T) {
			m.activeToast = &contracts.Toast{Level: tc.level, Title: "Toast title", Message: "Toast message"}
			c := newCanvas(m.width, m.height)
			defer c.release()
			m.drawToast(c)
			cells := frameCells(c.render())
			requireChromeText(t, cells, "● Toast title", tc.role, true)
			requireChromeText(t, cells, "Toast message", m.palette.Ink, false)
			borders, backgrounds := 0, 0
			for _, row := range cells {
				for _, cell := range row {
					if strings.ContainsAny(cell.Content, "╭╮╰╯─│") {
						borders++
						if !sameColor(cell.Style.Fg, m.palette.Border) {
							t.Errorf("toast border is not border: %+v", cell.Style)
						}
					}
					if cell.Style.Bg != nil {
						backgrounds++
						if !sameColor(cell.Style.Bg, m.palette.SurfaceElevated) {
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
	requireChromeText(t, cells, "⚠", m.palette.Warning, false)
	for _, text := range []string{"SECRET ACCESS REQUEST", askPrompt.Plugin} {
		requireChromeText(t, cells, text, m.palette.Ink, true)
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
				requireChromeText(t, uv.Lines{row}, key, m.palette.Ink, true)
			}
		}
	}
	if !foundHints {
		t.Fatal("secret prompt hints missing")
	}
	if !sameColor(c.buf.CellAt(r.Min.X, r.Min.Y).Style.Fg, m.palette.BorderFocused) {
		t.Fatal("secret prompt border not focused border")
	}
	for y := r.Min.Y + 1; y < r.Max.Y-1; y++ {
		for x := r.Min.X + 1; x < r.Max.X-1; x++ {
			if !sameColor(c.buf.CellAt(x, y).Style.Bg, m.palette.SurfaceElevated) {
				t.Fatalf("secret prompt background not elevated at %d,%d", x, y)
			}
		}
	}
}

func TestStatusMarkLevels(t *testing.T) {
	m := goldenModel()
	for _, tc := range []struct {
		level string
		role  color.Color
	}{
		{"info", m.palette.Info}, {"success", m.palette.Success}, {"warning", m.palette.Warning}, {"danger", m.palette.Danger},
	} {
		t.Run(tc.level, func(t *testing.T) {
			requireChromeText(t, frameCells(m.statusMark(tc.level, "●")), "●", tc.role, false)
		})
	}
}
