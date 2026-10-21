package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func (m *WorkbenchModel) View() tea.View {
	if m.tooSmall() {
		view := tea.NewView(m.renderTooSmall())
		view.AltScreen = true
		view.MouseMode = tea.MouseModeAllMotion
		return view
	}
	topBar := m.renderTopWorkbenchBar()
	bottomBar := m.renderBottomKeybindBar()

	var cols []string
	cols = append(cols, m.renderActivityRailBox())

	if m.activePlacement == huskipc.PlacementFullCanvas {
		fullBox := m.renderSurface(huskipc.SlotFullCanvas, "②-⑦", "full-canvas")
		cols = append(cols, fullBox)
	} else {
		if m.showSidebar {
			navBox := m.renderSurface(huskipc.SlotSidebarNavigation, "①", "sidebar-navigation")
			listBox := m.renderSurface(huskipc.SlotSidebarList, "②③", "sidebar-list")
			footBox := m.renderSurface(huskipc.SlotSidebarFooter, "④", "sidebar-footer")
			leftCol := lipgloss.JoinVertical(lipgloss.Left, navBox, listBox, footBox)
			cols = append(cols, leftCol)
		}

		if m.activePlacement == huskipc.PlacementStage {
			stageBox := m.renderSurface(huskipc.SlotWorkspaceStage, "⑤-⑦", "workspace-stage")
			cols = append(cols, stageBox)
		} else {
			hdrBox := m.renderSurface(huskipc.SlotMainHeader, "⑤", "main-header")
			var stageBox string
			if m.stageMode == "hotspots" {
				stageBox = m.renderHotspotArchitectureStage()
			} else {
				stageBox = m.renderSurface(huskipc.SlotMainStage, "⑥", "main-stage")
			}
			centerCol := lipgloss.JoinVertical(lipgloss.Left, hdrBox, stageBox)
			cols = append(cols, centerCol)

			if _, ok := m.slotRects[huskipc.SlotSidePanel]; ok {
				rightCol := m.renderSurface(huskipc.SlotSidePanel, "⑦", "side-panel")
				cols = append(cols, rightCol)
			}
		}
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	baseView := lipgloss.JoinVertical(lipgloss.Left, topBar, body, bottomBar)

	if m.activeModalRailID != "" {
		baseView = m.overlayModalPlugin(baseView)
	}
	if m.activeToast != nil {
		baseView = m.overlayToast(baseView)
	}
	if m.showPalette {
		baseView = m.overlayCommandPalette(baseView)
	}
	view := tea.NewView(baseView)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeAllMotion
	return view
}

func (m *WorkbenchModel) renderActivityRailBox() string {
	rect := m.slotRects[huskipc.SlotActivityRail]
	railW := rect.Width
	bodyH := rect.Height

	railBg := lipgloss.Color(m.palette.Surface)
	bgRowStyle := lipgloss.NewStyle().Background(railBg)
	emptyRow := bgRowStyle.Render(strings.Repeat(" ", railW))

	rows := make([]string, bodyH)
	for i := 0; i < bodyH; i++ {
		rows[i] = emptyRow
	}

	btns := m.railButtons()
	showShortcuts := m.modifiers.Alt
	for idx, b := range btns {
		if b.endY >= bodyH {
			continue
		}
		isHovered := m.hoverRailIdx == idx

		centerCell := " " + b.icon + " "
		if showShortcuts {
			short := b.shortcut
			if strings.EqualFold(short, "ctrl+p") {
				short = "^P"
			}
			switch len(short) {
			case 1:
				centerCell = " " + short + " "
			case 2:
				centerCell = " " + short
			default:
				centerCell = fitLineWidth(short, 3)
			}
		}

		if b.active || isHovered || showShortcuts {
			pillColor := lipgloss.Color(m.palette.SurfaceSelected)
			iconFg := lipgloss.Color(m.palette.Accent)
			if !b.active {
				pillColor = lipgloss.Color(m.palette.SurfaceElevated)
				if showShortcuts {
					iconFg = lipgloss.Color(m.palette.Warning)
				} else {
					iconFg = lipgloss.Color(m.palette.Ink)
				}
			} else if showShortcuts {
				iconFg = lipgloss.Color(m.palette.Warning)
			}

			capStyle := lipgloss.NewStyle().Foreground(pillColor).Background(railBg)
			midStyle := lipgloss.NewStyle().Foreground(iconFg).Background(pillColor).Bold(b.active || showShortcuts)
			pad := bgRowStyle.Render(" ")

			rows[b.startY] = pad + capStyle.Render("▄▄▄") + pad
			rows[b.startY+1] = pad + midStyle.Render(centerCell) + pad
			rows[b.startY+2] = pad + capStyle.Render("▀▀▀") + pad
		} else {
			iconStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color(m.palette.InkMuted)).
				Background(railBg)
			rows[b.startY] = emptyRow
			rows[b.startY+1] = bgRowStyle.Render("  ") + iconStyle.Render(b.icon) + bgRowStyle.Render("  ")
			rows[b.startY+2] = emptyRow
		}
	}

	return strings.Join(rows, "\n")
}

func (m *WorkbenchModel) overlayModalPlugin(base string) string {
	rect := m.modalRect
	innerW, innerH := rect.InnerW, rect.InnerH

	su := m.slotUpdates[huskipc.SlotModalSurface]
	// A declared region composes over the blank canvas before slot_update too.
	rows, _ := m.publishedContent(huskipc.SlotModalSurface, innerW, innerH)

	spec := m.frameFor(huskipc.SlotModalSurface)
	label := ""
	if spec.Label != "" {
		label = spec.Label
		if su != nil && su.Title != "" {
			label = su.Title
		}
	}
	box := m.renderFrame(huskipc.SlotModalSurface, rect, spec, label, rows)
	return spliceOverlay(base, box, rect.X, rect.Y)
}

func (m *WorkbenchModel) renderTopWorkbenchBar() string {
	brand := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Canvas)).
		Background(lipgloss.Color(m.palette.Accent)).
		Bold(true).
		Padding(0, 1).
		Render("◆ HUSK WORKBENCH")

	modeLabel := " 3-Column Workbench "
	switch m.activePlacement {
	case huskipc.PlacementFullCanvas:
		modeLabel = "  Full-Canvas Mode (Case 2) "
	case huskipc.PlacementStage:
		modeLabel = "  Stage-minus-Sidebar Mode (Case 3) "
	}
	if m.activeModalRailID != "" {
		modeLabel = "  Modal Overlay Active (Case 1) "
	}

	sub := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.InkMuted)).
		Render(modeLabel)

	pluginCountBadge := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Success)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1).
		Render(fmt.Sprintf("● %d plugins", len(m.plugins)))

	themePill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.AccentSecondary)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1).
		Render("✦ " + m.palette.Name)

	dragPill := ""
	if m.activeDrag != "" {
		dragPill = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Canvas)).
			Background(lipgloss.Color(m.palette.Success)).
			Bold(true).
			Padding(0, 1).
			Render("⇄ Dragging "+m.activeDrag) + " "
	} else if m.hoverDivider != "" {
		dragPill = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Canvas)).
			Background(lipgloss.Color(m.palette.Warning)).
			Bold(true).
			Padding(0, 1).
			Render("⇄ Drag border: "+m.hoverDivider) + " "
	} else if m.hoverRailIdx >= 0 {
		btns := m.railButtons()
		if m.hoverRailIdx < len(btns) {
			b := btns[m.hoverRailIdx]
			dragPill = lipgloss.NewStyle().
				Foreground(lipgloss.Color(m.palette.Ink)).
				Background(lipgloss.Color(m.palette.SurfaceSelected)).
				Bold(true).
				Padding(0, 1).
				Render(fmt.Sprintf("%s %s (%s)", b.icon, b.label, b.shortcut)) + " "
		}
	} else if m.modifiers.Alt {
		dragPill = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Canvas)).
			Background(lipgloss.Color(m.palette.Warning)).
			Bold(true).
			Padding(0, 1).
			Render("⌥ ALT HELD · Shortcuts Active") + " "
	}

	head := brand + sub + pluginCountBadge + " "
	right := dragPill + themePill
	// Rail shortcuts come from the rail items plugins declared, spelled as the
	// rail's own shortcut labels. Entries that do not fit beside the right-hand
	// pills are dropped from the end rather than pushing those pills off.
	railKeys := []string{"F1 home"}
	for _, ri := range m.orderedRailItems() {
		if ri.Shortcut != "" {
			railKeys = append(railKeys, railShortcutLabel(ri.Shortcut)+" "+ri.Label)
		}
	}
	railStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Info)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1)
	budget := m.width - ansi.StringWidth(head) - ansi.StringWidth(right) - 1
	railHint := ""
	for n := len(railKeys); n > 0; n-- {
		text := "Rail: " + strings.Join(railKeys[:n], "  ")
		if n < len(railKeys) {
			text += "  …"
		}
		if r := railStyle.Render(text); ansi.StringWidth(r) <= budget {
			railHint = r
			break
		}
	}
	left := head + railHint
	gap := max(1, m.width-ansi.StringWidth(left)-ansi.StringWidth(right))
	line := left + strings.Repeat(" ", gap) + right
	return lipgloss.NewStyle().
		Background(lipgloss.Color(m.palette.Surface)).
		Render(fitLineWidth(line, m.width))
}

func (m *WorkbenchModel) renderBottomKeybindBar() string {
	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Accent)).
		Bold(true)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.InkMuted))
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Border)).
		Render(" │ ")

	hint := func(k, d string) string {
		return keyStyle.Render(k) + " " + descStyle.Render(d)
	}

	left := strings.Join([]string{
		hint("F1", " home"),
		hint("j/k", "nav"),
		hint("⏎", "focus"),
		hint("drag/= ", "resize"),
		hint("[ ] /", "docks"),
		hint("ctrl+p", "palette"),
		hint("q", "quit"),
	}, sep)

	ipcStat := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.InkSubtle)).
		Render(fmt.Sprintf("ipc: %d ", m.totalIPCEvents))

	gap := max(1, m.width-ansi.StringWidth(left)-ansi.StringWidth(ipcStat))
	line := " " + left + strings.Repeat(" ", max(0, gap-1)) + ipcStat
	return lipgloss.NewStyle().
		Background(lipgloss.Color(m.palette.Surface)).
		Render(fitLineWidth(line, m.width))
}

// renderSurface renders a slot's region: plugin content (or blank canvas)
// wrapped in the frame the occupying plugin declared. Husk adds no chrome of
// its own; calloutNum/slotName only appear as a label overlay when hotspot
// badges are toggled on ('h').
func (m *WorkbenchModel) renderSurface(slotID huskipc.SlotID, calloutNum, slotName string) string {
	rect, ok := m.slotRects[slotID]
	if !ok {
		return ""
	}
	su := m.slotUpdates[slotID]
	spec := m.frameFor(slotID)
	label := ""
	if spec.Label != "" {
		label = spec.Label
		if su != nil && su.Title != "" {
			label = su.Title
		}
	}
	lines, _ := m.publishedContent(slotID, rect.InnerW, rect.InnerH)
	if m.showHotspots && len(lines) > 0 {
		hot := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Warning)).
			Bold(true).
			Render(calloutNum + " " + slotName)
		lines[0] = overlayLabel(lines[0], hot)
	}
	return m.renderFrame(slotID, rect, spec, label, lines)
}

// surfaceContentLines pads/truncates slot_update lines to a surface's inner
// area; absent content is a blank canvas. Content is clipped, never scrolled
// or wrapped: a line wider than w is truncated with "…", and lines past
// index h-1 are dropped. The host does not tell the plugin it clipped; the
// plugin learns its inner size from slot_resize and must fit its own content
// (see docs/docs/architecture.md, "Content clipping"). A style left open by a
// line is closed before its padding so it cannot reach the frame edge.
func (m *WorkbenchModel) surfaceContentLines(su *huskipc.SlotUpdatePayload, w, h int) []string {
	lines := make([]string, h)
	for i := 0; i < h; i++ {
		if su != nil && i < len(su.Lines) {
			lines[i] = fitLineWidth(closeSGR(su.Lines[i]), w)
		} else {
			lines[i] = strings.Repeat(" ", w)
		}
	}
	return lines
}

// overlayLabel replaces the start of a rendered line with label.
func overlayLabel(line, label string) string {
	rest := ansi.Cut(line, ansi.StringWidth(label), ansi.StringWidth(line))
	return closeSGR(label) + rest
}

func (m *WorkbenchModel) frameLabelStyle() lipgloss.Style {
	col := lipgloss.Color(m.palette.InkMuted)
	// the focused slot's label uses the accent colour when it has a border
	return lipgloss.NewStyle().Foreground(col).Bold(true)
}

func (m *WorkbenchModel) closeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkMuted)).Bold(true)
}

// renderFrame draws plugin content wrapped in the declared frame chrome.
// All Husk-drawn surface chrome lives here. Without a border, label and close
// glyph are overlaid on the first content row (no geometry change).
func (m *WorkbenchModel) renderFrame(slotID huskipc.SlotID, rect SlotRect, spec frame.Spec, label string, lines []string) string {
	if !spec.Enabled() {
		return strings.Join(lines, "\n")
	}
	if !spec.Border {
		if len(lines) > 0 {
			row := lines[0]
			rowW := ansi.StringWidth(row)
			// reserve the close cell before placing the label
			avail := rowW
			if spec.Close {
				avail = max(0, rowW-1)
			}
			if label != "" && avail > 0 {
				styled := lipgloss.NewStyle().
					Foreground(lipgloss.Color(m.palette.Accent)).
					Bold(true).
					Render(label)
				if w := ansi.StringWidth(styled); w > avail {
					styled = ansi.Truncate(styled, avail, "…")
				}
				row = overlayLabel(row, styled)
			}
			// clip any overflow back to the surface width (cell-aware, ANSI-safe)
			if w := ansi.StringWidth(row); w > rowW {
				row = ansi.Cut(row, 0, rowW)
			}
			if spec.Close {
				body := closeSGR(ansi.Cut(row, 0, max(0, rowW-1)))
				if pad := rowW - 1 - ansi.StringWidth(body); pad > 0 {
					body += strings.Repeat(" ", pad)
				}
				row = body + m.closeStyle().Render("✕")
			}
			lines[0] = row
		}
		return strings.Join(lines, "\n")
	}

	innerW := rect.InnerW
	labelText := ""
	if label != "" {
		labelText = " " + m.frameLabelStyle().Render(label) + " "
	}
	closeText := ""
	if spec.Close {
		closeText = m.closeStyle().Render(" ✕")
	}
	closeW := ansi.StringWidth(closeText)
	// a top edge narrower than the close glyph cannot fit it: omit it rather
	// than overflow the border row
	if closeW > 0 && innerW <= closeW {
		closeText = ""
		closeW = 0
	}
	if w := ansi.StringWidth(labelText) + closeW; w > innerW {
		labelText = ansi.Truncate(labelText, max(0, innerW-closeW), "…")
	}
	dashes := max(0, innerW-ansi.StringWidth(labelText)-ansi.StringWidth(closeText))

	leftEdge, rightEdge, topEdge, botEdge, leftGlyph, rightGlyph, topGlyph, botGlyph := m.dividerEdgeStyles(slotID)
	top := leftEdge.Render("╭") +
		topEdge.Render(strings.Repeat(topGlyph, dashes)) +
		labelText +
		closeText +
		rightEdge.Render("╮")

	var out []string
	out = append(out, top)
	lp := leftEdge.Render(leftGlyph)
	rp := rightEdge.Render(rightGlyph)
	for _, l := range lines {
		out = append(out, lp+l+rp)
	}
	out = append(out, leftEdge.Render("╰")+botEdge.Render(strings.Repeat(botGlyph, innerW))+rightEdge.Render("╯"))
	return strings.Join(out, "\n")
}

// dividerEdgeStyles returns the edge styles/glyphs for a bordered surface,
// highlighting the divider being dragged or hovered (only visible when the
// occupying plugin declared a border frame).
func (m *WorkbenchModel) dividerEdgeStyles(slotID huskipc.SlotID) (left, right, top, bot lipgloss.Style, lG, rG, tG, bG string) {
	borderCol := lipgloss.Color(m.palette.Border)
	if m.focusedSlot == slotID {
		borderCol = lipgloss.Color(m.palette.BorderFocused)
	}
	bStyle := lipgloss.NewStyle().Foreground(borderCol)
	left, right, top, bot = bStyle, bStyle, bStyle, bStyle
	lG, rG, tG, bG = "│", "│", "─", "─"

	activeDiv := m.hoverDivider
	edgeCol := lipgloss.Color(m.palette.Warning)
	if m.activeDrag != "" {
		activeDiv = m.activeDrag
		edgeCol = lipgloss.Color(m.palette.Success)
	}
	hlStyle := lipgloss.NewStyle().Foreground(edgeCol).Bold(true)

	switch activeDiv {
	case "sidebar-width":
		if slotID == huskipc.SlotSidebarNavigation || slotID == huskipc.SlotSidebarList || slotID == huskipc.SlotSidebarFooter {
			right = hlStyle
			rG = "┃"
		}
		if slotID == huskipc.SlotMainHeader || slotID == huskipc.SlotMainStage || slotID == huskipc.SlotWorkspaceStage {
			left = hlStyle
			lG = "┃"
		}
	case "sidepanel-width":
		if slotID == huskipc.SlotMainHeader || slotID == huskipc.SlotMainStage {
			right = hlStyle
			rG = "┃"
		}
		if slotID == huskipc.SlotSidePanel {
			left = hlStyle
			lG = "┃"
		}
	case "sidebar-nav-height":
		if slotID == huskipc.SlotSidebarNavigation {
			bot = hlStyle
			bG = "━"
		}
		if slotID == huskipc.SlotSidebarList {
			top = hlStyle
			tG = "━"
		}
	case "sidebar-foot-height":
		if slotID == huskipc.SlotSidebarList {
			bot = hlStyle
			bG = "━"
		}
		if slotID == huskipc.SlotSidebarFooter {
			top = hlStyle
			tG = "━"
		}
	case "main-header-height":
		if slotID == huskipc.SlotMainHeader {
			bot = hlStyle
			bG = "━"
		}
		if slotID == huskipc.SlotMainStage {
			top = hlStyle
			tG = "━"
		}
	}
	return
}

func (m *WorkbenchModel) renderHotspotArchitectureStage() string {
	rect := m.slotRects[huskipc.SlotMainStage]
	// The architecture explorer is Husk's own UI and always draws its own core
	// frame: derive a fresh inner rect from the OUTER slot rect so the explorer
	// spans the full slot whether or not the main-stage plugin declared a
	// border of its own.
	coreFrame := frame.Spec{Border: true, Label: "Slot Architecture Explorer"}
	_, _, coreW, coreH := frame.InnerRect(rect.X, rect.Y, rect.Width, rect.Height, coreFrame)
	coreRect := SlotRect{
		X: rect.X, Y: rect.Y, Width: rect.Width, Height: rect.Height,
		InnerX: rect.X + 1, InnerY: rect.Y + 1, InnerW: coreW, InnerH: coreH,
	}
	innerW := coreW

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true)
	subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkMuted))
	subtle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkSubtle))

	var lines []string
	lines = append(lines,
		titleStyle.Render("HUSK WORKBENCH SLOT ARCHITECTURE"),
		subStyle.Render("Every numbered region is an out-of-process IPC slot over husk.sock / stdio. Press 'v' to toggle back."),
		subtle.Render(strings.Repeat("─", innerW)),
	)

	for idx, slot := range huskipc.AllSlots {
		selected := idx == m.hotspotCursor
		owner := m.slotOwners[slot.ID]
		if owner == "" {
			owner = "husk.core (built-in)"
		}
		numBadge := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Canvas)).
			Background(lipgloss.Color(m.palette.Warning)).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("#%d", slot.Number))

		idText := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Ink)).
			Bold(true).
			Render(fmt.Sprintf("%-20s", slot.ID))

		titleText := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.AccentSecondary)).
			Render(fmt.Sprintf("%-20s", slot.Title))

		ownerPill := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Success)).
			Render("owner: " + owner)

		row1 := fmt.Sprintf(" %s %s  %s  %s", numBadge, idText, titleText, ownerPill)
		row2 := "      " + subtle.Render(slot.Description)
		if selected {
			row1 = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.SurfaceSelected)).Render(fitLineWidth(row1, innerW))
			row2 = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.SurfaceSelected)).Render(fitLineWidth(row2, innerW))
		}
		lines = append(lines, row1, row2)
	}

	m.slotUpdates[huskipc.SlotMainStage] = &huskipc.SlotUpdatePayload{
		PluginID: "husk.core",
		Slot:     huskipc.SlotMainStage,
		Title:    "Slot Architecture Explorer",
		Badge:    "9 HOTSPOTS",
		Lines:    lines,
	}
	// The explorer renders through its own core rect, not the plugin's.
	rows := m.surfaceContentLines(m.slotUpdates[huskipc.SlotMainStage], coreW, coreH)
	return m.renderFrame(huskipc.SlotMainStage, coreRect, coreFrame, coreFrame.Label, rows)
}

func (m *WorkbenchModel) overlayToast(base string) string {
	if m.activeToast == nil {
		return base
	}
	borderCol := lipgloss.Color(m.palette.Info)
	switch m.activeToast.Level {
	case "success":
		borderCol = lipgloss.Color(m.palette.Success)
	case "error":
		borderCol = lipgloss.Color(m.palette.Danger)
	case "warn":
		borderCol = lipgloss.Color(m.palette.Warning)
	}

	toastW := min(48, m.width-6)
	title := lipgloss.NewStyle().Foreground(borderCol).Bold(true).Render("● " + m.activeToast.Title)
	msg := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Ink)).Render(ansi.Truncate(m.activeToast.Message, toastW-4, "…"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderCol).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1).
		Width(toastW).
		Render(title + "\n" + msg)

	return spliceOverlay(base, box, max(2, m.width-toastW-4), 2)
}

func (m *WorkbenchModel) overlayCommandPalette(base string) string {
	palW := min(82, m.width-8)
	cmds := m.filteredCommands()

	hdr := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Warning)).
		Bold(true).
		Render("⑧ COMMAND PALETTE (app-overlay)")
	prompt := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Accent)).
		Bold(true).
		Render("❯ ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Ink)).Render(m.paletteQuery+"█")

	var rows []string
	rows = append(rows, hdr, prompt, lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Border)).Render(strings.Repeat("─", palW-4)))

	maxItems := min(12, len(cmds))
	for i := 0; i < maxItems; i++ {
		c := cmds[i]
		sel := i == m.paletteCursor
		cat := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.AccentSecondary)).Render(fmt.Sprintf("[%s]", c.Category))
		title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Ink)).Render(c.Title)
		short := ""
		if c.Shortcut != "" {
			short = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Warning)).Render(c.Shortcut)
		}
		left := fmt.Sprintf(" %s %s", cat, title)
		gap := max(1, (palW-4)-ansi.StringWidth(left)-ansi.StringWidth(short)-1)
		line := fitLineWidth(left+strings.Repeat(" ", gap)+short+" ", palW-4)
		if sel {
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.SurfaceSelected)).Bold(true).Render(line)
		}
		rows = append(rows, line)
	}
	if len(cmds) == 0 {
		rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkSubtle)).Render("  No matching commands"))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.palette.BorderFocused)).
		Background(lipgloss.Color(m.palette.Surface)).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))

	x := max(2, (m.width-palW)/2)
	y := max(3, m.height/6)
	return spliceOverlay(base, box, x, y)
}

// spliceOverlay draws overlay over base with its top-left cell at
// (startX, startY). No graphic rendition crosses a splice boundary:
//
//   - ansi.Cut keeps every escape sequence of the line, including those after
//     the cut, so the left fragment ends in the state the whole base line ends
//     in (often a background left open by plugin content). It is reset before
//     the overlay so the overlay's cells carry only the overlay's own styles.
//   - The overlay is reset before the right fragment, so an overlay line that
//     ends with an open style does not paint the base cells after it.
//   - The right fragment replays the base line's escapes from its start, so
//     once the state is reset it resumes exactly the style its first cell had.
func spliceOverlay(base, overlay string, startX, startY int) string {
	baseLines := strings.Split(base, "\n")
	ovLines := strings.Split(overlay, "\n")

	for i, ovLine := range ovLines {
		rowIdx := startY + i
		if rowIdx < 0 || rowIdx >= len(baseLines) {
			continue
		}
		bgLine := baseLines[rowIdx]
		ovW := ansi.StringWidth(ovLine)
		left := closeSGR(ansi.Cut(bgLine, 0, startX))
		right := ansi.Cut(bgLine, startX+ovW, ansi.StringWidth(bgLine))
		baseLines[rowIdx] = left + closeSGR(ovLine) + right
	}
	return strings.Join(baseLines, "\n")
}

func fitLineWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw > width {
		return ansi.Truncate(s, width, "…")
	}
	if sw < width {
		return s + strings.Repeat(" ", width-sw)
	}
	return s
}
