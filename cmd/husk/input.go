package main

import (
	"image"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// legacyKeyString preserves v1 wire names for keys decoded by Bubble Tea v2.
// v1 prefixed Alt before all other modifiers and used printable shifted text,
// even when v2 omits Text (Alt-prefixed input and native release events).
func legacyKeyString(msg tea.KeyMsg) string {
	key := msg.Key()
	alt := key.Mod.Contains(tea.ModAlt)
	// Num/Scroll Lock do not affect text or legacy modifier names. Caps Lock
	// affects printable fallback casing but not control-key matching.
	key.Mod &^= tea.ModAlt | tea.ModNumLock | tea.ModScrollLock

	if key.Code == tea.KeySpace && key.Mod&^tea.ModCapsLock == tea.ModCtrl {
		// A terminal NUL was named ctrl+@ in v1, not ctrl+space.
		key.Code = '@'
	}
	if unicode.IsPrint(key.Code) && key.Mod&^(tea.ModShift|tea.ModCapsLock) == 0 {
		if key.Text == "" {
			code := key.ShiftedCode
			if code == 0 {
				code = key.Code
				if key.Mod.Contains(tea.ModShift) || key.Mod.Contains(tea.ModCapsLock) {
					code = unicode.ToUpper(code)
				}
			}
			key.Text = string(code)
		}
		key.Mod &^= tea.ModShift | tea.ModCapsLock
	}

	name := key.String()
	if name == "space" {
		name = " "
	}
	if alt {
		name = "alt+" + name
	}
	return name
}

// trackModifierKey folds a bare modifier key event into the modifier state and
// reports whether the key was one. Terminals send these only when View asks
// for all keys as escape codes (kitty protocol), which gives platforms without
// an OS modifier poll real Shift/Ctrl/Alt/Super state. Modifier keys are
// state, not input: they never reach slot_key, shortcuts or held_keys. Hyper,
// Meta and ISO level shifts have no ModifierState field and are dropped.
func (m *WorkbenchModel) trackModifierKey(k tea.Key, down bool) bool {
	if k.Code < tea.KeyLeftShift || k.Code > tea.KeyIsoLevel5Shift {
		return false
	}
	bit := uint16(1) << (k.Code - tea.KeyLeftShift)
	if down {
		m.termMods |= bit
	} else {
		m.termMods &^= bit
	}
	held := func(left, right rune) bool {
		return m.termMods&(1<<(left-tea.KeyLeftShift)|1<<(right-tea.KeyLeftShift)) != 0
	}
	next := m.modifiers
	switch k.Code {
	case tea.KeyLeftShift, tea.KeyRightShift:
		next.Shift = held(tea.KeyLeftShift, tea.KeyRightShift)
	case tea.KeyLeftCtrl, tea.KeyRightCtrl:
		next.Ctrl = held(tea.KeyLeftCtrl, tea.KeyRightCtrl)
	case tea.KeyLeftAlt, tea.KeyRightAlt:
		next.Alt = held(tea.KeyLeftAlt, tea.KeyRightAlt) || m.altLatched
	case tea.KeyLeftSuper, tea.KeyRightSuper:
		next.Super = held(tea.KeyLeftSuper, tea.KeyRightSuper)
	}
	m.syncModifierTransitions(next)
	return true
}

func (m *WorkbenchModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := legacyKeyString(msg)

	// While the terminal is too small the UI is hidden: only quit is honoured
	// and nothing is forwarded to plugins.
	if m.layoutFrozen() {
		if key == "q" || key == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}

	if msg.Mod.Contains(tea.ModAlt) && !m.modifiers.Alt {
		next := m.modifiers
		next.Alt = true
		m.syncModifierTransitions(next)
	}

	phase := huskipc.KeyPhaseDown
	if _, already := m.heldKeys[key]; already || msg.IsRepeat {
		phase = huskipc.KeyPhaseRepeat
	}
	m.heldKeys[key] = time.Now()
	m.broadcastKeyState(phase, key, true)

	if m.showPalette {
		switch key {
		case "esc", "ctrl+p", "ctrl+c":
			m.showPalette = false
			m.paletteQuery = ""
			return m, nil
		case "up", "ctrl+k":
			cmds := m.filteredCommands()
			if len(cmds) > 0 {
				m.paletteCursor = (m.paletteCursor - 1 + len(cmds)) % len(cmds)
			}
			return m, nil
		case "down", "ctrl+j", "tab":
			cmds := m.filteredCommands()
			if len(cmds) > 0 {
				m.paletteCursor = (m.paletteCursor + 1) % len(cmds)
			}
			return m, nil
		case "enter":
			cmds := m.filteredCommands()
			if m.paletteCursor >= 0 && m.paletteCursor < len(cmds) {
				chosen := cmds[m.paletteCursor]
				m.showPalette = false
				m.paletteQuery = ""
				return m.executeCommand(chosen.ID)
			}
			return m, nil
		case "backspace":
			if len(m.paletteQuery) > 0 {
				_, size := utf8.DecodeLastRuneInString(m.paletteQuery)
				m.paletteQuery = m.paletteQuery[:len(m.paletteQuery)-size]
				m.paletteCursor = 0
			}
			return m, nil
		default:
			if paletteTextFor(msg, key) {
				m.paletteQuery += key
				m.paletteCursor = 0
			}
			return m, nil
		}
	}

	if m.modalOwner().Open() {
		closeKey := key == "esc"
		if ri, ok := m.railItemByID(m.modalOwner().RailID); ok && ri.Shortcut != "" && ri.Shortcut == key {
			closeKey = true // the item's own shortcut toggles it closed
		}
		if closeKey {
			m.closeModal()
			return m, nil
		}
		if cp, ok := m.plugins[m.modalOwner().PluginID]; ok {
			_ = cp.Writer.Send(huskipc.Envelope{
				Type: "slot_key",
				SlotKey: &huskipc.SlotKeyPayload{
					Slot: huskipc.SlotModalSurface,
					Key:  key,
				},
			})
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		// main stops the plugins once the terminal is restored.
		return m, tea.Quit
	case "?":
		m.altLatched = !m.altLatched
		next := m.modifiers
		next.Alt = m.altLatched
		m.syncModifierTransitions(next)
		return m, nil
	case "esc", "f1", "alt+1":
		if m.activeRailID != "" || m.stageMode == "hotspots" {
			m.activateHomeWorkbench()
			return m, nil
		}
	case "ctrl+p", ":", "alt+p":
		m.showPalette = true
		m.paletteQuery = ""
		m.paletteCursor = 0
		return m, nil
	case "ctrl+i", "v", "alt+v":
		return m.executeCommand("husk.hotspots.toggle_stage")
	case "h":
		return m.executeCommand("husk.hotspots.badges")
	case "[":
		return m.executeCommand("husk.sidebar.toggle")
	case "]":
		return m.executeCommand("husk.sidepanel.toggle")
	case "/":
		return m.executeCommand("husk.docks.cycle")
	case "=":
		m.sidebarWidth = 38
		m.sidebarNavH = 4
		m.sidebarFootH = 4
		m.mainHeaderH = 4
		m.sidePaneW = 36
		m.recomputeLayout()
		m.broadcastSlotResize()
		return m, m.showLocalToast("info", "Layout Reset", "Restored default slot dimensions")
	case "<", ",":
		if m.showSidebar && m.sidebarWidth > 26 {
			m.sidebarWidth -= 2
			m.recomputeLayout()
			m.broadcastSlotResize()
		}
		return m, nil
	case ">", ".":
		if m.showSidebar && m.sidebarWidth < 56 {
			m.sidebarWidth += 2
			m.recomputeLayout()
			m.broadcastSlotResize()
		}
		return m, nil
	case "tab":
		if m.activePlacement == huskipc.PlacementStage {
			if m.focusedSlot == huskipc.SlotSidebarList {
				m.focusedSlot = huskipc.SlotWorkspaceStage
			} else {
				m.focusedSlot = huskipc.SlotSidebarList
			}
			m.broadcastSlotResize()
			return m, nil
		}
		order := []huskipc.SlotID{
			huskipc.SlotSidebarList,
			huskipc.SlotMainStage,
			huskipc.SlotSidePanel,
		}
		for i, s := range order {
			if s == m.focusedSlot {
				m.focusedSlot = order[(i+1)%len(order)]
				break
			}
		}
		m.broadcastSlotResize()
		return m, nil
	}

	if m.stageMode == "hotspots" && m.focusedSlot == huskipc.SlotMainStage {
		if key == "j" || key == "down" {
			m.hotspotCursor = (m.hotspotCursor + 1) % len(huskipc.AllSlots)
			return m, nil
		}
		if key == "k" || key == "up" {
			m.hotspotCursor = (m.hotspotCursor - 1 + len(huskipc.AllSlots)) % len(huskipc.AllSlots)
			return m, nil
		}
	}

	// Rail item shortcuts and plugin key bindings come from what plugins
	// declared in hello; the host binds no application keys of its own.
	if ri, ok := m.railShortcutItem(key); ok {
		m.activateRailItem(ri)
		return m, nil
	}
	targetSlot := m.focusedSlot
	if pid, kb, ok := m.keyBinding(key, targetSlot); ok {
		return m.runPluginBinding(pid, kb.CommandID)
	}

	// Everything else goes to the focused surface's occupant only. An
	// unoccupied focused surface drops the key.
	ownerID := m.keyOwner(targetSlot)
	if cp, ok := m.plugins[ownerID]; ok {
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "slot_key",
			SlotKey: &huskipc.SlotKeyPayload{
				Slot: targetSlot,
				Key:  key,
			},
		})
	}
	return m, nil
}

// keyOwner is the plugin that receives keys for slot: the surface's occupant,
// or, for the surface opened by the active full-canvas/stage rail item, the
// plugin that declared that rail item.
func (m *WorkbenchModel) keyOwner(slot huskipc.SlotID) string {
	if owner := m.surfaceOwner(slot); owner != "" {
		return owner
	}
	if ri, ok := m.activeRailItem(); ok {
		if (ri.Placement == huskipc.PlacementFullCanvas && slot == huskipc.SlotFullCanvas) ||
			(ri.Placement == huskipc.PlacementStage && slot == huskipc.SlotWorkspaceStage) {
			return ri.PluginID
		}
	}
	return ""
}

// paletteChordMods are the modifiers that turn a key into a chord. Shift and
// Caps Lock only change the produced text, so they stay allowed.
const paletteChordMods = tea.ModAlt | tea.ModCtrl | tea.ModMeta | tea.ModSuper | tea.ModHyper

// paletteTextFor reports whether a key press is typed text for the palette
// query. The decision comes from the event, not from its spelling: a press
// carrying a chord modifier is never text, whatever script its base key is in
// (Alt+o on a German layout arrives as "alt+ö"). The legacy spelling is then
// checked for printable content.
func paletteTextFor(msg tea.KeyPressMsg, key string) bool {
	if msg.Key().Mod&paletteChordMods != 0 {
		return false
	}
	return isPaletteText(key)
}

// chordPrefixes are the legacy modifier prefixes of a chord spelling.
var chordPrefixes = []string{"alt+", "ctrl+", "meta+", "super+", "hyper+", "shift+"}

// isPaletteText reports whether a legacy key spelling is typed text for the
// palette query: one printable rune of any script, or a multi-rune composed
// string containing non-ASCII text (IME output, emoji sequences). Named keys
// ("up", "f5") are ASCII and longer than one rune, and any spelling with a
// modifier prefix longer than one rune ("alt+x", "alt+ö", "ctrl+日") is a
// chord, so none of them match.
func isPaletteText(key string) bool {
	if key == "" {
		return false
	}
	if utf8.RuneCountInString(key) > 1 {
		for _, p := range chordPrefixes {
			if strings.HasPrefix(key, p) {
				return false
			}
		}
	}
	runes, nonASCII := 0, false
	for _, r := range key {
		if r != ' ' && !unicode.IsPrint(r) {
			return false
		}
		if r > unicode.MaxASCII {
			nonASCII = true
		}
		runes++
	}
	return runes == 1 || nonASCII
}

// onBorderRing reports whether (x, y) lies on the border ring of a surface
// whose occupant declared a border frame. Only those cells are drawn as
// dividers, so only they may start a drag.
func (m *WorkbenchModel) onBorderRing(x, y int) bool {
	for slot, r := range m.slotRects {
		if !image.Pt(x, y).In(r.Rect()) {
			continue
		}
		if x != r.X && x != r.X+r.Width-1 && y != r.Y && y != r.Y+r.Height-1 {
			continue
		}
		if m.frameFor(slot).Border {
			return true
		}
	}
	return false
}

// detectDividerAt names the resizable divider under (x, y), or "". A divider
// is the shared edge between two surfaces, so the cell must also be a drawn
// border cell: an unbordered neighbour's first row or column is content and
// stays clickable.
//
// The two checks are independent by design: any bordered ring cell that falls
// inside a divider's band counts, even when the ring belongs to a different
// bordered surface than the divider's own neighbours. This is accepted because
// every surface sharing a divider's column or row is drawn as part of the same
// edge, so a bordered cell there is a drawn divider cell.
func (m *WorkbenchModel) detectDividerAt(x, y int) string {
	if !m.onBorderRing(x, y) {
		return ""
	}
	return m.dividerBandAt(x, y)
}

func (m *WorkbenchModel) dividerBandAt(x, y int) string {
	if m.activePlacement == huskipc.PlacementFullCanvas || y < 1 || y >= m.height-1 {
		return ""
	}
	if m.showSidebar {
		if sbRect, ok := m.slotRects[huskipc.SlotSidebarList]; ok {
			rightEdge := sbRect.X + sbRect.Width - 1
			if x == rightEdge || x == rightEdge+1 {
				return "sidebar-width"
			}
		}
		if navRect, ok := m.slotRects[huskipc.SlotSidebarNavigation]; ok {
			if x >= navRect.X+1 && x < navRect.X+navRect.Width-1 {
				botEdge := navRect.Y + navRect.Height - 1
				if y == botEdge || y == botEdge+1 {
					return "sidebar-nav-height"
				}
			}
		}
		if footRect, ok := m.slotRects[huskipc.SlotSidebarFooter]; ok {
			if x >= footRect.X+1 && x < footRect.X+footRect.Width-1 {
				topEdge := footRect.Y
				if y == topEdge-1 || y == topEdge {
					return "sidebar-foot-height"
				}
			}
		}
	}
	if sideRect, ok := m.slotRects[huskipc.SlotSidePanel]; ok {
		// The centre column's right edge sits surfaceGutter cells before the
		// gutter, so the band spans from that edge to the panel's own left
		// edge; the blank gutter cell between them is inside it.
		leftEdge := sideRect.X
		if x >= leftEdge-1-surfaceGutter && x <= leftEdge {
			return "sidepanel-width"
		}
	}
	if hdrRect, ok := m.slotRects[huskipc.SlotMainHeader]; ok {
		if x >= hdrRect.X+1 && x < hdrRect.X+hdrRect.Width-1 {
			botEdge := hdrRect.Y + hdrRect.Height - 1
			if y == botEdge || y == botEdge+1 {
				return "main-header-height"
			}
		}
	}
	return ""
}

func (m *WorkbenchModel) applyDragAt(x, y int) bool {
	bodyH := max(20, m.height-2)
	railW := activityRailWidth
	changed := false

	switch m.activeDrag {
	case "sidebar-width":
		newW := max(24, min(m.width/2, x-railW+1))
		if newW != m.sidebarWidth {
			m.sidebarWidth = newW
			changed = true
		}
	case "sidepanel-width":
		newW := max(24, min(m.width/2, m.width-x))
		if newW != m.sidePaneW {
			m.sidePaneW = newW
			changed = true
		}
	case "sidebar-nav-height":
		newH := max(3, min(12, y))
		if newH != m.sidebarNavH && bodyH-newH-m.sidebarFootH >= 6 {
			m.sidebarNavH = newH
			changed = true
		}
	case "sidebar-foot-height":
		newH := max(3, min(12, (1+bodyH)-y))
		if newH != m.sidebarFootH && bodyH-m.sidebarNavH-newH >= 6 {
			m.sidebarFootH = newH
			changed = true
		}
	case "main-header-height":
		newH := max(3, min(12, y))
		if newH != m.mainHeaderH && bodyH-newH >= 8 {
			m.mainHeaderH = newH
			changed = true
		}
	}
	return changed
}

func (m *WorkbenchModel) handleMouse(event tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.showPalette || m.tooSmall() {
		return m, nil
	}
	msg := event.Mouse()

	if _, release := event.(tea.MouseReleaseMsg); release {
		if m.activeDrag != "" {
			m.activeDrag = ""
			m.hoverDivider = m.detectDividerAt(msg.X, msg.Y)
			return m, nil
		}
	}

	if _, motion := event.(tea.MouseMotionMsg); motion {
		if m.activeDrag != "" {
			if m.applyDragAt(msg.X, msg.Y) {
				m.recomputeLayout()
				m.broadcastSlotResize()
			}
			return m, nil
		}
		m.hoverDivider = m.detectDividerAt(msg.X, msg.Y)
		m.hoverRailIdx = m.detectRailHoverAt(msg.X, msg.Y)
		return m, nil
	}

	_, wheel := event.(tea.MouseWheelMsg)
	_, click := event.(tea.MouseClickMsg)
	isWheelDown := wheel && msg.Button == tea.MouseWheelDown
	isWheelUp := wheel && msg.Button == tea.MouseWheelUp
	isLeftClick := click && msg.Button == tea.MouseLeft

	if !isWheelDown && !isWheelUp && !isLeftClick {
		return m, nil
	}

	if isLeftClick {
		if slot, ok := m.closeHitAt(msg.X, msg.Y); ok {
			if slot == huskipc.SlotModalSurface {
				m.closeModal()
			} else {
				m.activateHomeWorkbench()
			}
			return m, nil
		}
	}

	if m.modalOwner().Open() && m.handlePublishedModalMouse(msg.X, msg.Y, isLeftClick, isWheelDown, isWheelUp) {
		return m, nil
	}

	if isLeftClick {
		if div := m.detectDividerAt(msg.X, msg.Y); div != "" {
			if m.clickContributionAt(msg.X, msg.Y) {
				return m, nil
			}
			m.activeDrag = div
			m.hoverDivider = div
			return m, nil
		}
	}

	if isLeftClick {
		if btnIdx := m.detectRailHoverAt(msg.X, msg.Y); btnIdx >= 0 {
			btns := m.railButtons()
			if btnIdx < len(btns) {
				b := btns[btnIdx]
				switch b.kind {
				case "home":
					m.activateHomeWorkbench()
					return m, nil
				case "plugin":
					m.activateRailItem(b.item)
					return m, nil
				case "hotspots":
					return m.executeCommand("husk.hotspots.toggle_stage")
				case "palette":
					m.showPalette = true
					m.paletteQuery = ""
					m.paletteCursor = 0
					return m, nil
				}
			}
		}
	}

	for _, hit := range m.hitOrder() {
		slotID, rect := hit.ID, hit.Rect
		if slotID == huskipc.SlotActivityRail {
			continue
		}
		if image.Pt(msg.X, msg.Y).In(rect.Rect()) {
			if isLeftClick && (slotID == huskipc.SlotSidebarList || slotID == huskipc.SlotMainStage || slotID == huskipc.SlotSidePanel || slotID == huskipc.SlotWorkspaceStage || slotID == huskipc.SlotFullCanvas) {
				m.focusedSlot = slotID
				m.broadcastSlotResize()
			}
			relX := msg.X - rect.InnerX
			relY := msg.Y - rect.InnerY
			if relX < 0 || relY < 0 || relX >= rect.InnerW || relY >= rect.InnerH {
				return m, nil
			}

			if isLeftClick && m.clickContribution(slotID, relX, relY, rect.InnerW, rect.InnerH) {
				return m, nil
			}

			btn := "left"
			if isWheelDown {
				btn = "wheel_down"
			} else if isWheelUp {
				btn = "wheel_up"
			}

			ownerID := m.surfaceOwner(slotID)
			if cp, ok := m.plugins[ownerID]; ok {
				_ = cp.Writer.Send(huskipc.Envelope{
					Type:      "slot_mouse",
					SlotMouse: m.publisherMousePayload(slotID, relX, relY, btn),
				})
			}
			return m, nil
		}
	}

	return m, nil
}

// closeHitAt reports the framed surface (modal / full-canvas / stage) whose
// close button covers (x, y), if that surface declared one.
func (m *WorkbenchModel) closeHitAt(x, y int) (huskipc.SlotID, bool) {
	// While a modal is open it is the topmost surface: clicks never reach a
	// close button on the underlying canvas/stage (matching esc, which only
	// dismisses the modal).
	if m.modalOwner().Open() {
		spec := m.frameFor(huskipc.SlotModalSurface)
		if !spec.Close {
			return "", false
		}
		rect := m.modalRect
		cx := rect.X + rect.Width - 1
		if spec.Border {
			cx = rect.X + rect.Width - 2
		}
		if y == rect.Y && (x == cx || (spec.Border && x == cx-1)) {
			return huskipc.SlotModalSurface, true
		}
		return "", false
	}
	for _, slot := range []huskipc.SlotID{huskipc.SlotFullCanvas, huskipc.SlotWorkspaceStage} {
		var rect SlotRect
		switch slot {
		case huskipc.SlotModalSurface:
			if !m.modalOwner().Open() {
				continue
			}
			rect = m.modalRect
		default:
			if m.activePlacement == "" {
				continue
			}
			r, ok := m.slotRects[slot]
			if !ok {
				continue
			}
			rect = r
		}
		spec := m.frameFor(slot)
		if !spec.Close {
			continue
		}
		cx := rect.X + rect.Width - 1
		if spec.Border {
			cx = rect.X + rect.Width - 2
		}
		if y == rect.Y && (x == cx || (spec.Border && x == cx-1)) {
			return slot, true
		}
	}
	return "", false
}
