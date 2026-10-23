package main

import (
	"fmt"
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func (m *WorkbenchModel) View() tea.View {
	if m.tooSmall() {
		view := tea.NewView(m.renderTooSmall())
		view.BackgroundColor = lipgloss.Color(m.palette.Canvas)
		view.AltScreen = true
		view.MouseMode = tea.MouseModeAllMotion
		view.ReportFocus = true
		view.KeyboardEnhancements.ReportEventTypes = true
		view.KeyboardEnhancements.ReportAlternateKeys = true
		view.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
		view.KeyboardEnhancements.ReportAssociatedText = true
		return view
	}
	c := newCanvas(m.width, m.height)
	c.blitBlock(0, 0, m.renderTopWorkbenchBar())
	// Every surface draws at its own slot rect, lowest slotZ first, so the
	// geometry hit-testing uses is the geometry drawn. Cells no surface owns
	// (the gutter beside the side panel) stay blank.
	order := m.hitOrder()
	for i := len(order) - 1; i >= 0; i-- {
		h := order[i]
		switch {
		case h.ID == protocol.SurfaceActivityRail:
			c.blitBlock(h.Rect.X, h.Rect.Y, m.renderActivityRailBox())
		case h.ID == protocol.SurfaceMainStage && m.stageMode == "hotspots":
			m.drawHotspotArchitectureStage(c)
		default:
			m.drawSurface(c, h.ID, surfaceCallouts[h.ID], string(h.ID))
		}
	}
	c.blitBlock(0, m.height-1, m.renderBottomKeybindBar())
	// Overlays are opaque blocks drawn over the base in z order: each replaces
	// every cell of its box and nothing it leaves open reaches a cell it did
	// not write.
	// The modal and the palette then cast a shadow (castShadow) that darkens
	// what is under them.
	ink, canvasBg := lipgloss.Color(m.palette.Ink), lipgloss.Color(m.palette.Canvas)
	if m.modalOwner().Open() {
		m.drawModal(c)
		r := m.modalRect
		c.castShadow(uv.Rect(r.X, r.Y, r.Width, r.Height), ink, canvasBg)
	}
	if m.surfaceOwner(protocol.SurfaceAppOverlay) != "" {
		c.blitOverlay(m.publishedContent(protocol.SurfaceAppOverlay, m.width, m.height))
	}
	if m.activeToast != nil {
		m.drawToast(c) // a toast is not a popup: no shadow
	}
	if m.showPalette {
		c.castShadow(m.drawCommandPalette(c), ink, canvasBg)
	}
	if m.secretPrompt != nil {
		c.castShadow(m.drawSecretPrompt(c), ink, canvasBg)
	}
	// The app background: every cell still on the default background (the
	// gutter, cells no surface owns) takes Canvas, so the slot shade derived
	// from Canvas is lighter than what the user sees on every terminal.
	// Overlays and surfaces have already set their own backgrounds.
	c.fillDefaultBg(uv.Rect(0, 0, m.width, m.height), lipgloss.Color(m.palette.Canvas))
	out := c.render()
	c.release()
	view := tea.NewView(out)
	// Terminals that support OSC 11 also get their own background set, so
	// cells outside the grid (resize flashes, scrollback edges) match; Bubble
	// Tea resets it on exit.
	view.BackgroundColor = lipgloss.Color(m.palette.Canvas)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeAllMotion
	view.ReportFocus = true
	view.KeyboardEnhancements.ReportEventTypes = true
	// Alternate keys make a shifted symbol's release carry the same shifted
	// code as its press, so both normalise to the same held-key name.
	view.KeyboardEnhancements.ReportAlternateKeys = true
	// All keys as escape codes makes kitty-protocol terminals report bare
	// modifier presses (trackModifierKey). It also stops them sending a key's
	// text, so associated text is requested too: without it the decoder can
	// only rebuild Text from the key code, which loses AltGr symbols and
	// dead-key and compose results on non-US layouts.
	view.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
	view.KeyboardEnhancements.ReportAssociatedText = true
	return view
}

// surfaceCallouts are the hotspot badge numbers shown on each surface when
// hotspots are toggled on ('h').
var surfaceCallouts = map[protocol.SurfaceID]string{
	protocol.SurfaceSidebarNavigation: "①",
	protocol.SurfaceSidebarList:       "②③",
	protocol.SurfaceSidebarFooter:     "④",
	protocol.SurfaceMainHeader:        "⑤",
	protocol.SurfaceMainStage:         "⑥",
	protocol.SurfaceSidePanel:         "⑦",
	protocol.SurfaceWorkspaceStage:    "⑤-⑦",
	protocol.SurfaceFullCanvas:        "②-⑦",
}

func (m *WorkbenchModel) renderActivityRailBox() string {
	rect := m.slotRects[protocol.SurfaceActivityRail]
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

		text := b.icon
		if showShortcuts && b.shortcut != "" {
			text = b.shortcut
			if strings.EqualFold(text, "ctrl+p") {
				text = "^P"
			}
		}
		text = ansi.Truncate(text, 5, "…")
		left := (5 - ansi.StringWidth(text) + 1) / 2
		centerCell := strings.Repeat(" ", left) + text + strings.Repeat(" ", 5-left-ansi.StringWidth(text))

		if b.active || isHovered || showShortcuts {
			pillColor := lipgloss.Color(m.palette.SurfaceSelected)
			iconFg := lipgloss.Color(m.palette.Accent)
			if !b.active {
				pillColor = lipgloss.Color(m.palette.SurfaceElevated)
				iconFg = lipgloss.Color(m.palette.Ink)
			} else if showShortcuts && b.shortcut != "" {
				iconFg = lipgloss.Color(m.palette.Ink)
			}

			tileStyle := lipgloss.NewStyle().Background(pillColor)
			midStyle := lipgloss.NewStyle().Foreground(iconFg).Background(pillColor).Bold(b.active || showShortcuts)
			pad := bgRowStyle.Render(" ")

			rows[b.startY] = pad + tileStyle.Render("     ") + pad
			rows[b.startY+1] = pad + midStyle.Render(centerCell) + pad
			rows[b.startY+2] = pad + tileStyle.Render("     ") + pad
		} else {
			iconStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color(m.palette.InkMuted)).
				Background(railBg)
			rows[b.startY] = emptyRow
			rows[b.startY+1] = bgRowStyle.Render(" ") + iconStyle.Render(centerCell) + bgRowStyle.Render(" ")
			rows[b.startY+2] = emptyRow
		}
	}

	return strings.Join(rows, "\n")
}

// drawModal draws the modal surface, framed as its plugin declared, at the
// modal rect.
func (m *WorkbenchModel) drawModal(c *canvas) {
	rect := m.modalRect
	innerW, innerH := rect.InnerW, rect.InnerH

	su := m.slotUpdates[protocol.SurfaceModal]
	// A declared region composes over the blank canvas before slot_update too.
	m.publishedContent(protocol.SurfaceModal, innerW, innerH).draw(c, rect.InnerX, rect.InnerY)

	spec := m.frameFor(protocol.SurfaceModal)
	label := ""
	if spec.Label != "" {
		label = spec.Label
		if su != nil && su.Title != "" {
			label = su.Title
		}
	}
	m.drawChrome(c, protocol.SurfaceModal, rect, spec, label)
	m.fillSlotBg(c, rect)
}

// layoutLabel describes the placement and the side columns actually drawn.
func (m *WorkbenchModel) layoutLabel() string {
	_, left := m.slotRects[protocol.SurfaceSidebarList]
	_, right := m.slotRects[protocol.SurfaceSidePanel]
	switch {
	case m.modalOwner().Open():
		return "Modal"
	case m.activePlacement == contracts.PlacementFullCanvas:
		return "Full canvas"
	case m.activePlacement == contracts.PlacementStage:
		if left {
			return "Stage · Left"
		}
		return "Stage"
	case left && right:
		return "Workbench · Left + Right"
	case left:
		return "Workbench · Left"
	case right:
		return "Workbench · Right"
	}
	return "Workbench"
}

func (m *WorkbenchModel) renderTopWorkbenchBar() string {
	brand := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Ink)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Bold(true).
		Padding(0, 1).
		Render("◈ HUSK")

	dragPill := ""
	if m.activeDrag != "" {
		dragPill = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Ink)).
			Background(lipgloss.Color(m.palette.SurfaceSelected)).
			Bold(true).
			Padding(0, 1).
			Render("⇄ Dragging "+m.activeDrag) + " "
	} else if m.hoverDivider != "" {
		dragPill = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Ink)).
			Background(lipgloss.Color(m.palette.SurfaceElevated)).
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
				Render(strings.TrimSpace(fmt.Sprintf("%s %s %s", b.icon, b.label, parenthesized(b.shortcut)))) + " "
		}
	} else if m.modifiers.Alt {
		dragPill = lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Ink)).
			Background(lipgloss.Color(m.palette.SurfaceElevated)).
			Bold(true).
			Padding(0, 1).
			Render("⌥ ALT HELD · Shortcuts Active") + " "
	}

	gap := max(1, m.width-ansi.StringWidth(brand)-ansi.StringWidth(dragPill))
	line := brand + strings.Repeat(" ", gap) + dragPill
	return lipgloss.NewStyle().
		Background(lipgloss.Color(m.palette.Surface)).
		Render(fitLineWidth(line, m.width))
}

func (m *WorkbenchModel) renderBottomKeybindBar() string {
	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Ink)).
		Bold(true)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.InkMuted))
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Border)).
		Render(" │ ")

	hint := func(k, d string) string {
		return keyStyle.Render(k) + " " + descStyle.Render(d)
	}

	left := descStyle.Render(" " + m.layoutLabel() + " ")
	pillRect := themePillRect(m.width, m.height, m.palette.Name)
	for _, h := range m.footerHints() {
		group := sep + hint(h.key, h.desc)
		if ansi.StringWidth(left)+ansi.StringWidth(group)+1 > pillRect.Min.X {
			break
		}
		left += group
	}
	pill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.InkMuted)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1).
		Render("✦ " + themePillName(m.palette.Name))
	line := left + strings.Repeat(" ", max(1, pillRect.Min.X-ansi.StringWidth(left))) + pill
	return lipgloss.NewStyle().
		Background(lipgloss.Color(m.palette.Surface)).
		Render(fitLineWidth(line, m.width))
}

// themePillName bounds the name independently of ANSI byte length.
func themePillName(name string) string {
	if name == "" {
		name = "theme"
	}
	return ansi.Truncate(name, 24, "…")
}

// themePillRect is shared by rendering and input; View never records hit state.
func themePillRect(width, height int, name string) image.Rectangle {
	w := 4 + ansi.StringWidth(themePillName(name)) // padding, star and space
	return image.Rect(width-w, height-1, width, height)
}

// regionSlot includes the rail for completeness; View draws its host buttons
// directly rather than calling drawSurface, so it never shows an empty name.
func regionSlot(id protocol.SurfaceID) bool {
	switch id {
	case protocol.SurfaceActivityRail, protocol.SurfaceSidebarNavigation,
		protocol.SurfaceSidebarList, protocol.SurfaceSidebarFooter,
		protocol.SurfaceMainHeader, protocol.SurfaceMainStage, protocol.SurfaceSidePanel:
		return true
	}
	return false
}

// drawSurface draws occupant content or an empty region's name, then badges
// and the frame the occupant declared. Placement surfaces never show a name.
func (m *WorkbenchModel) drawSurface(c *canvas, slotID protocol.SurfaceID, calloutNum, slotName string) {
	rect, ok := m.slotRects[slotID]
	if !ok {
		return
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
	if regionSlot(slotID) && m.surfaceOwner(slotID) == "" {
		if rect.InnerW >= 3 && rect.InnerH > 0 {
			name := fitLineWidth(slotID.Humanised(), rect.InnerW)
			name = strings.TrimRight(name, " ")
			x := rect.InnerX + (rect.InnerW-ansi.StringWidth(name))/2
			y := rect.InnerY + (rect.InnerH-1)/2
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkSubtle)).Background(lipgloss.Color(m.palette.Slot))
			c.draw(uv.Rect(x, y, ansi.StringWidth(name), 1), style.Render(name))
		}
	} else {
		m.publishedContent(slotID, rect.InnerW, rect.InnerH).draw(c, rect.InnerX, rect.InnerY)
	}
	if m.showHotspots && rect.InnerW > 0 && rect.InnerH > 0 {
		hot := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Ink)).
			Background(lipgloss.Color(m.palette.SurfaceSelected)).
			Bold(true).
			Render(calloutNum + " " + slotName)
		c.draw(uv.Rect(rect.InnerX, rect.InnerY, min(ansi.StringWidth(hot), rect.InnerW), 1), hot)
	}
	m.drawChrome(c, slotID, rect, spec, label)
	m.fillSlotBg(c, rect)
}

// fillSlotBg paints the slot background token under the content area of rect:
// cells with no background of their own take it, cells a plugin coloured keep
// theirs. It runs after the frame chrome so a borderless frame's label row,
// drawn over the first content row, is covered too; a border ring lies outside
// the content area and keeps its own styling.
func (m *WorkbenchModel) fillSlotBg(c *canvas, rect SlotRect) {
	if m.palette.Slot == "" || rect.InnerW <= 0 || rect.InnerH <= 0 {
		return
	}
	c.fillDefaultBg(uv.Rect(rect.InnerX, rect.InnerY, rect.InnerW, rect.InnerH), lipgloss.Color(m.palette.Slot))
}

// surfaceContentLines clips slot_update lines to a surface's inner area;
// absent content is a blank canvas. Content is clipped, never scrolled or
// wrapped: a line wider than w is truncated with "…", and lines past index
// h-1 are dropped. The host does not tell the plugin it clipped; the plugin
// learns its inner size from slot_resize and must fit its own content (see
// docs/docs/architecture.md, "Content clipping"). Lines are not padded: the
// canvas clears the rest of each row when it draws them, from a zero pen, so
// a style a line leaves open stops at its last character.
func (m *WorkbenchModel) surfaceContentLines(su *protocol.SurfaceUpdateParams, w, h int) []string {
	lines := make([]string, h)
	for i := 0; i < h && su != nil && i < len(su.Lines); i++ {
		if ansi.StringWidth(su.Lines[i]) > w {
			lines[i] = ansi.Truncate(su.Lines[i], w, "…")
		} else {
			lines[i] = su.Lines[i]
		}
	}
	return lines
}

func (m *WorkbenchModel) frameLabelStyle() lipgloss.Style {
	col := lipgloss.Color(m.palette.InkMuted)
	return lipgloss.NewStyle().Foreground(col).Bold(true)
}

func (m *WorkbenchModel) closeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkMuted)).Bold(true)
}

// drawFrame draws content lines into rect's inner area, then the declared
// frame chrome around (or over) them.
func (m *WorkbenchModel) drawFrame(c *canvas, slotID protocol.SurfaceID, rect SlotRect, spec frame.Spec, label string, lines []string) {
	c.blitLines(rect.InnerX, rect.InnerY, rect.InnerW, rect.InnerH, lines)
	m.drawChrome(c, slotID, rect, spec, label)
	m.fillSlotBg(c, rect)
}

// drawChrome draws the frame chrome a plugin declared for rect, after its
// content. All Husk-drawn surface chrome lives here. With a border, the
// edges, label and close glyph occupy the ring around the inner area.
// Without one, the label and close glyph are drawn as cells over the first
// content row (no geometry change); the close glyph keeps the row's last
// cell and the label is truncated to the cells before it.
func (m *WorkbenchModel) drawChrome(c *canvas, slotID protocol.SurfaceID, rect SlotRect, spec frame.Spec, label string) {
	if !spec.Enabled() {
		return
	}
	if !spec.Border {
		rowW := rect.InnerW
		if rect.InnerH <= 0 || rowW <= 0 {
			return
		}
		avail := rowW
		if spec.Close {
			avail = rowW - 1
		}
		if label != "" && avail > 0 {
			labelStyle := m.frameLabelStyle()
			if m.focusedSlot == slotID {
				labelStyle = labelStyle.Foreground(lipgloss.Color(m.palette.Ink))
			}
			styled := labelStyle.
				Bold(true).
				Render(label)
			if w := ansi.StringWidth(styled); w > avail {
				styled = ansi.Truncate(styled, avail, "…")
			}
			c.blitBlock(rect.InnerX, rect.InnerY, styled)
		}
		if spec.Close {
			c.blitBlock(rect.InnerX+rowW-1, rect.InnerY, m.closeStyle().Render("✕"))
		}
		return
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
	c.blitBlock(rect.X, rect.Y, top)
	lp := leftEdge.Render(leftGlyph)
	rp := rightEdge.Render(rightGlyph)
	for i := 0; i < rect.InnerH; i++ {
		c.blitBlock(rect.X, rect.InnerY+i, lp)
		c.blitBlock(rect.InnerX+innerW, rect.InnerY+i, rp)
	}
	c.blitBlock(rect.X, rect.InnerY+rect.InnerH, leftEdge.Render("╰")+botEdge.Render(strings.Repeat(botGlyph, innerW))+rightEdge.Render("╯"))
}

// dividerEdgeStyles returns the edge styles/glyphs for a bordered surface,
// highlighting the divider being dragged or hovered (only visible when the
// occupying plugin declared a border frame).
func (m *WorkbenchModel) dividerEdgeStyles(slotID protocol.SurfaceID) (left, right, top, bot lipgloss.Style, lG, rG, tG, bG string) {
	borderCol := lipgloss.Color(m.palette.Border)
	if m.focusedSlot == slotID {
		borderCol = lipgloss.Color(m.palette.BorderFocused)
	}
	bStyle := lipgloss.NewStyle().Foreground(borderCol)
	left, right, top, bot = bStyle, bStyle, bStyle, bStyle
	lG, rG, tG, bG = "│", "│", "─", "─"

	activeDiv := m.hoverDivider
	edgeCol := lipgloss.Color(m.palette.Ink)
	if m.activeDrag != "" {
		activeDiv = m.activeDrag
		edgeCol = lipgloss.Color(m.palette.Accent)
	}
	hlStyle := lipgloss.NewStyle().Foreground(edgeCol).Bold(true)

	switch activeDiv {
	case "sidebar-width":
		if slotID == protocol.SurfaceSidebarNavigation || slotID == protocol.SurfaceSidebarList || slotID == protocol.SurfaceSidebarFooter {
			right = hlStyle
			rG = "┃"
		}
		if slotID == protocol.SurfaceMainHeader || slotID == protocol.SurfaceMainStage || slotID == protocol.SurfaceWorkspaceStage {
			left = hlStyle
			lG = "┃"
		}
	case "sidepanel-width":
		if slotID == protocol.SurfaceMainHeader || slotID == protocol.SurfaceMainStage {
			right = hlStyle
			rG = "┃"
		}
		if slotID == protocol.SurfaceSidePanel {
			left = hlStyle
			lG = "┃"
		}
	case "sidebar-nav-height":
		if slotID == protocol.SurfaceSidebarNavigation {
			bot = hlStyle
			bG = "━"
		}
		if slotID == protocol.SurfaceSidebarList {
			top = hlStyle
			tG = "━"
		}
	case "sidebar-foot-height":
		if slotID == protocol.SurfaceSidebarList {
			bot = hlStyle
			bG = "━"
		}
		if slotID == protocol.SurfaceSidebarFooter {
			top = hlStyle
			tG = "━"
		}
	case "main-header-height":
		if slotID == protocol.SurfaceMainHeader {
			bot = hlStyle
			bG = "━"
		}
		if slotID == protocol.SurfaceMainStage {
			top = hlStyle
			tG = "━"
		}
	}
	return
}

// drawHotspotArchitectureStage draws Husk's slot architecture explorer over
// the main-stage slot rect.
func (m *WorkbenchModel) drawHotspotArchitectureStage(c *canvas) {
	rect := m.slotRects[protocol.SurfaceMainStage]
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

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Ink)).Bold(true)
	subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkMuted))
	subtle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkSubtle))

	var lines []string
	lines = append(lines,
		titleStyle.Render("HUSK WORKBENCH SLOT ARCHITECTURE"),
		subStyle.Render("Every numbered region is an out-of-process IPC slot over husk.sock / stdio. Press 'v' to toggle back."),
		subtle.Render(strings.Repeat("─", innerW)),
	)

	for idx, slot := range allSurfaces {
		selected := idx == m.hotspotCursor
		owner := m.surfaceOwner(slot.ID)
		if owner == "" {
			owner = "husk.core (built-in)"
		}
		badgeBg := lipgloss.Color(m.palette.SurfaceElevated)
		ownerFg := lipgloss.Color(m.palette.InkSubtle)
		if selected {
			badgeBg = lipgloss.Color(m.palette.SurfaceSelected)
			ownerFg = lipgloss.Color(m.palette.InkMuted)
		}
		numBadge := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Ink)).
			Background(badgeBg).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("#%d", slot.Number))

		rowStyle := lipgloss.NewStyle()
		if selected {
			rowStyle = rowStyle.Background(lipgloss.Color(m.palette.SurfaceSelected))
		}
		idText := rowStyle.
			Foreground(lipgloss.Color(m.palette.Ink)).
			Bold(true).
			Render(fmt.Sprintf("%-20s", slot.ID))

		titleText := rowStyle.
			Foreground(lipgloss.Color(m.palette.InkMuted)).
			Render(fmt.Sprintf("%-20s", slot.Title))

		ownerPill := rowStyle.
			Foreground(ownerFg).
			Render("owner: " + owner)

		row1 := fmt.Sprintf(" %s %s  %s  %s", numBadge, idText, titleText, ownerPill)
		row2 := "      " + subtle.Render(slot.Description)
		if selected {
			// InkSubtle on SurfaceSelected is 4.42:1; InkMuted clears 4.5:1.
			row2 = "      " + subStyle.Background(lipgloss.Color(m.palette.SurfaceSelected)).Render(slot.Description)
			row1 = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.SurfaceSelected)).Render(fitLineWidth(row1, innerW))
			row2 = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.SurfaceSelected)).Render(fitLineWidth(row2, innerW))
		}
		lines = append(lines, row1, row2)
	}

	// The explorer is derived entirely from model state, so it is built here
	// and never stored: View must not write the model, and storing it in
	// slotUpdates would overwrite the main-stage plugin's last payload.
	explorer := &protocol.SurfaceUpdateParams{
		PluginID: "husk.core",
		Surface:  protocol.SurfaceMainStage,
		Title:    coreFrame.Label,
		Lines:    lines,
	}
	// The explorer renders through its own core rect, not the plugin's.
	rows := m.surfaceContentLines(explorer, coreW, coreH)
	m.drawFrame(c, protocol.SurfaceMainStage, coreRect, coreFrame, coreFrame.Label, rows)
}

// drawToast draws the active toast near the top-right corner.
func (m *WorkbenchModel) drawToast(c *canvas) {
	if m.activeToast == nil {
		return
	}
	levelCol := lipgloss.Color(m.palette.Info)
	switch m.activeToast.Level {
	case "success":
		levelCol = lipgloss.Color(m.palette.Success)
	case "error":
		levelCol = lipgloss.Color(m.palette.Danger)
	case "warn":
		levelCol = lipgloss.Color(m.palette.Warning)
	}

	toastW := min(48, m.width-6)
	title := lipgloss.NewStyle().Foreground(levelCol).Bold(true).Render("● " + m.activeToast.Title)
	msg := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Ink)).Render(ansi.Truncate(m.activeToast.Message, toastW-4, "…"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.palette.Border)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1).
		Width(toastW).
		Render(title + "\n" + msg)

	c.blitBlock(max(2, m.width-toastW-4), 2, box)
}

// drawCommandPalette draws the command palette box.
func (m *WorkbenchModel) drawCommandPalette(c *canvas) uv.Rectangle {
	palW := min(82, m.width-8)
	cmds := m.filteredCommands()

	hdr := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.InkMuted)).
		Bold(true).
		Render("⑧ COMMAND PALETTE (app-overlay)")
	prompt := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Accent)).
		Bold(true).
		Render("❯ ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Ink)).Render(m.paletteQuery+"█")

	iw := palW - 4
	rule := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Border)).Render(strings.Repeat("─", iw))
	var rows []string
	rows = append(rows, hdr, "", prompt, "", rule)

	// Columns come from every match, so they hold still while the window scrolls.
	cols := paletteColumns(cmds, iw)
	top := paletteWindowTop(m.paletteTop, m.paletteCursor, len(cmds))
	for i := top; i < min(len(cmds), top+paletteVisibleRows); i++ {
		rows = append(rows, m.paletteRow(cmds[i], i == m.paletteCursor, iw, cols))
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
	return c.blitBlock(x, y, box)
}

// Palette row layout: one lead cell, the tag column, a gap, the title, a gap,
// the right-aligned hint column, one trailing cell. Below paletteHintMinWidth
// inner cells the hint column is dropped; below paletteTagMinWidth the tag
// column goes too. The title is the only flexible column, so it is the one
// that is truncated.
const (
	paletteHintMinWidth = 56
	paletteTagMinWidth  = 36
	paletteMaxTagWidth  = 16
	paletteMinTitle     = 10
)

// paletteCols holds the resolved column widths of a palette list; a zero tag
// or hint width means the column is not drawn.
type paletteCols struct {
	tag, title, hint int
}

// paletteTag is the text of a command's tag cell.
func paletteTag(c paletteCommand) string {
	if c.Category == "" {
		return ""
	}
	return "[" + c.Category + "]"
}

// paletteColumns resolves column widths for rows within an inner width of iw
// cells. Widths are measured in terminal cells, so wide glyphs count as two.
func paletteColumns(cmds []paletteCommand, iw int) paletteCols {
	var tagW, hintW int
	for _, c := range cmds {
		tagW = max(tagW, ansi.StringWidth(paletteTag(c)))
		hintW = max(hintW, ansi.StringWidth(c.Shortcut))
	}
	tagW = min(tagW, paletteMaxTagWidth)
	if iw < paletteHintMinWidth {
		hintW = 0
	}
	if iw < paletteTagMinWidth {
		tagW = 0
	}
	for {
		title := iw - 2 // lead and trailing cells
		if tagW > 0 {
			title -= tagW + 1
		}
		if hintW > 0 {
			title -= hintW + 1
		}
		if title >= paletteMinTitle || (tagW == 0 && hintW == 0) {
			return paletteCols{tag: tagW, title: max(title, 0), hint: hintW}
		}
		// Too tight: the hint goes first, then the tag.
		if hintW > 0 {
			hintW = 0
		} else {
			tagW = 0
		}
	}
}

// paletteCell truncates s to w cells (wide-glyph safe), pads the remainder
// and styles the whole cell so the background covers every cell.
func paletteCell(s string, w int, right bool, st lipgloss.Style) string {
	s = ansi.Truncate(s, w, "…")
	pad := strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
	if right {
		return st.Render(pad + s)
	}
	return st.Render(s + pad)
}

// paletteRow renders one palette list row exactly iw cells wide, with the
// row background on every cell and the selected row highlighted end to end.
func (m *WorkbenchModel) paletteRow(c paletteCommand, sel bool, iw int, cols paletteCols) string {
	bg := lipgloss.Color(m.palette.Surface)
	if sel {
		bg = lipgloss.Color(m.palette.SurfaceSelected)
	}
	base := lipgloss.NewStyle().Background(bg)
	if sel {
		base = base.Bold(true)
	}
	var b strings.Builder
	b.WriteString(base.Render(" "))
	if cols.tag > 0 {
		tagFg := lipgloss.Color(m.palette.InkSubtle)
		if sel {
			tagFg = lipgloss.Color(m.palette.InkMuted)
		}
		b.WriteString(paletteCell(paletteTag(c), cols.tag, false, base.Foreground(tagFg)))
		b.WriteString(base.Render(" "))
	}
	b.WriteString(paletteCell(c.Title, cols.title, false, base.Foreground(lipgloss.Color(m.palette.Ink))))
	if cols.hint > 0 {
		b.WriteString(base.Render(" "))
		b.WriteString(paletteCell(c.Shortcut, cols.hint, true, base.Foreground(lipgloss.Color(m.palette.InkMuted))))
	}
	b.WriteString(base.Render(" "))
	return fitLineWidth(b.String(), iw)
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

// parenthesized wraps a key label for the hover pill; no key, no parentheses.
func parenthesized(key string) string {
	if key == "" {
		return ""
	}
	return "(" + key + ")"
}
