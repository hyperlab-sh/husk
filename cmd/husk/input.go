package main

import (
	"image"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/keyspelling"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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
	// Associated text (kitty flag 16) belongs to text-producing keys. A chord
	// keeps its keystroke name even if a terminal attached text to it.
	if key.Mod&(tea.ModCtrl|tea.ModMeta|tea.ModSuper|tea.ModHyper) != 0 && key.Code != tea.KeyExtended {
		key.Text = ""
	}

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

// normalizeTextEvent repairs a kitty "pure text event": CSI 0;mods;text u,
// which the spec sends when the terminal got text with no key behind it (for
// example macOS Option text in kitty). The pinned decoder maps key number 0 to
// NUL, so the event arrives as ctrl+space carrying Text. A real ctrl+space
// never carries Text: legacy NUL has none and kitty-protocol encoders send no
// associated text under Ctrl. So Ctrl is removed and the text becomes the key.
func normalizeTextEvent(msg tea.KeyPressMsg) tea.KeyPressMsg {
	if msg.Code != tea.KeySpace || !msg.Mod.Contains(tea.ModCtrl) || msg.Text == "" || msg.Text == " " {
		return msg
	}
	msg.Mod &^= tea.ModCtrl
	msg.Code = tea.KeyExtended
	if r, size := utf8.DecodeRuneInString(msg.Text); size == len(msg.Text) {
		msg.Code = r
	}
	return msg
}

// trackModifierKey folds a bare modifier key event into the modifier state and
// reports whether the key was one. Terminals send these only when View asks
// for all keys as escape codes (kitty protocol), which gives platforms without
// an OS modifier poll real Shift/Ctrl/Alt/Super state. Modifier keys are
// state, not input: they never reach slot_key, shortcuts or held_keys. Hyper,
// Meta and ISO level shifts have no ModifierState field and are dropped.
//
// The same mode makes terminals report Caps, Num and Scroll Lock presses,
// which they withhold otherwise. Those are swallowed too, so plugins see the
// same key set as before; the lock state still rides on other keys' Mod.
func (m *WorkbenchModel) trackModifierKey(k tea.Key, down bool) bool {
	switch k.Code {
	case tea.KeyCapsLock, tea.KeyNumLock, tea.KeyScrollLock:
		return true
	}
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

// recordHeldAlias remembers the key-code name of a press held under text the
// layout produced. The kitty protocol sends associated text on press and
// repeat only, so the release of AltGr+Q (held as "@") is named "q" and the
// release of a dead-key "é" is named "e". Shifted text needs no alias: the
// alternate-keys flag gives the release the same shifted name.
func (m *WorkbenchModel) recordHeldAlias(msg tea.KeyPressMsg, key string) {
	if msg.Text == "" || msg.Code == tea.KeyExtended {
		return
	}
	k := msg.Key()
	k.Text = ""
	code := legacyKeyString(tea.KeyPressMsg(k))
	if code == key {
		return
	}
	if m.heldAliases == nil {
		m.heldAliases = make(map[string]string)
	}
	m.heldAliases[code] = key
}

// releasedHeldKey maps a release's name to the name its press was held under.
// A stale alias (its key already expired) falls back to the release's name.
func (m *WorkbenchModel) releasedHeldKey(name string) string {
	text, ok := m.heldAliases[name]
	if !ok {
		return name
	}
	delete(m.heldAliases, name)
	if _, held := m.heldKeys[text]; held {
		return text
	}
	return name
}

// dropHeldAliases forgets the aliases pointing at a held key that expired.
func (m *WorkbenchModel) dropHeldAliases(key string) {
	for code, text := range m.heldAliases {
		if text == key {
			delete(m.heldAliases, code)
		}
	}
}

// keyContext is the focus and overlay state a key resolution depends on.
func (m *WorkbenchModel) keyContext() contracts.KeyContext {
	ctx := contracts.KeyContext{Frozen: m.layoutFrozen()}
	if m.modalOwner().Open() {
		ctx.ModalOpen = true
		ctx.ModalAction = m.modalOwner().RailID
		ctx.FocusSurface = string(protocol.SurfaceModal)
		ctx.FocusOwner = m.modalOwner().PluginID
		ctx.Capture = m.catalog.Capture[protocol.SurfaceModal]
		if m.captureReleased {
			ctx.Capture = protocol.CaptureNone
		}
		return ctx
	}
	ctx.FocusSurface = string(m.focusedSlot)
	ctx.FocusOwner = m.keyOwner(m.focusedSlot)
	ctx.Capture = m.catalog.Capture[m.focusedSlot]
	if m.captureReleased {
		ctx.Capture = protocol.CaptureNone
	}
	return ctx
}

// resolveKey runs key through the effective keymap, with the prefix typed so
// far, and reports whether the keymap consumed it (a binding ran, or a longer
// sequence is pending). A key it did not consume belongs to the focused
// surface's occupant: no binding claims it, or the bound host action declined.
func (m *WorkbenchModel) resolveKey(key string) bool {
	if key == "esc" && len(m.pendingKeys) > 0 {
		m.clearPending() // esc cancels a pending sequence and is consumed
		return true
	}
	ctx := m.keyContext()
	released := m.captureReleased
	if released {
		m.captureReleased = false
		m.captureGen++
	}
	keys := append(slices.Clone(m.pendingKeys), key)
	res := m.keys.Resolve(ctx, keys)
	if res.Outcome == contracts.KeyNoMatch && len(m.pendingKeys) > 0 {
		// The key broke the sequence: the pending keys are gone, and the key is
		// resolved on its own as if nothing had been pending.
		m.clearPending()
		keys = []string{key}
		res = m.keys.Resolve(ctx, keys)
	}
	switch res.Outcome {
	case contracts.KeyPrefix:
		m.pendingKeys = keys
		m.seqGen++
		m.cmds = append(m.cmds, m.scheduleSequenceTimeout(m.seqGen))
		return true
	case contracts.KeyMatch:
		m.clearPending()
		b := res.Binding
		if released && b.Action == "husk.capture.release" {
			return false
		}
		focus := protocol.FocusContext{Surface: ctx.FocusSurface, Owner: ctx.FocusOwner}
		// A declined action leaves the key to the occupant, never to a
		// lower-ranked candidate.
		return !m.invoke(b.Action, b.Args, protocol.CallerInput, &focus)
	}
	return false
}

// clearPending discards the keys of a pending sequence, if any, and retires
// its timer.
func (m *WorkbenchModel) clearPending() {
	m.pendingKeys = nil
	m.seqGen++
}

// scheduleSequenceTimeout arms the timeout of the sequence of generation gen.
func (m *WorkbenchModel) scheduleSequenceTimeout(gen uint64) tea.Cmd {
	msg := sequenceTimeoutMsg{gen: gen}
	if m.after == nil {
		return tea.Tick(sequenceTimeout, func(time.Time) tea.Msg { return msg })
	}
	return m.after(sequenceTimeout, msg)
}

// paletteClosesOn reports whether key closes the open palette: esc, ctrl+c
// (which closes rather than quits), and every single-key, global, non-text-key
// spelling of the palette's opener, so a rebind keeps its own key closing it
// and a text-key opener such as ":" is typed into the query. An empty context
// admits only global candidates, and the winner is the effective global entry,
// so a Match on the opener is exactly that set.
func (m *WorkbenchModel) paletteClosesOn(key string) bool {
	if key == "esc" || key == "ctrl+c" {
		return true
	}
	if keyspelling.IsTextKey(key) {
		return false
	}
	res := m.keys.Resolve(contracts.KeyContext{}, []string{key})
	return res.Outcome == contracts.KeyMatch && res.Binding != nil && res.Binding.Action == "husk.palette.open"
}

func (m *WorkbenchModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := legacyKeyString(msg)

	// While the terminal is too small the UI is hidden: only quit is honoured
	// and nothing is forwarded to plugins.
	if m.layoutFrozen() {
		m.resolveKey(key)
		return m, nil
	}

	if m.settings != nil && m.secretPrompt == nil {
		return m.settingsKey(msg)
	}

	// The secret confirm dialog is modal and private: no plugin sees its keys.
	if m.secretPrompt != nil {
		return m.secretPromptKey(key)
	}

	if msg.Mod.Contains(tea.ModAlt) && !m.modifiers.Alt {
		next := m.modifiers
		next.Alt = true
		m.syncModifierTransitions(next)
	}

	phase := protocol.KeyPhaseDown
	if _, already := m.heldKeys[key]; already || msg.IsRepeat {
		phase = protocol.KeyPhaseRepeat
	}
	m.heldKeys[key] = time.Now()
	m.recordHeldAlias(msg, key)
	m.broadcastKeyState(phase, key, true)

	if m.showPalette {
		if m.paletteClosesOn(key) {
			m.showPalette = false
			m.paletteQuery = ""
			return m, nil
		}
		switch key {
		case "up", "ctrl+k":
			cmds := m.filteredCommands()
			if len(cmds) > 0 {
				m.movePaletteCursor(-1, len(cmds))
			}
			return m, nil
		case "down", "ctrl+j", "tab":
			cmds := m.filteredCommands()
			if len(cmds) > 0 {
				m.movePaletteCursor(1, len(cmds))
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
				m.paletteCursor, m.paletteTop = 0, 0
			}
			return m, nil
		default:
			if paletteTextFor(msg, key) {
				m.paletteQuery += key
				m.paletteCursor, m.paletteTop = 0, 0
			}
			return m, nil
		}
	}

	if m.resolveKey(key) {
		return m, nil
	}

	// An open modal owns every key the keymap left alone.
	if m.modalOwner().Open() {
		m.sendKey(m.modalOwner().PluginID, protocol.SurfaceModal, key)
		return m, nil
	}

	if m.stageMode == "hotspots" && m.focusedSlot == protocol.SurfaceMainStage {
		if key == "j" || key == "down" {
			m.hotspotCursor = (m.hotspotCursor + 1) % len(allSurfaces)
			return m, nil
		}
		if key == "k" || key == "up" {
			m.hotspotCursor = (m.hotspotCursor - 1 + len(allSurfaces)) % len(allSurfaces)
			return m, nil
		}
	}

	// Everything else goes to the focused surface's occupant only. An
	// unoccupied focused surface drops the key.
	m.sendKey(m.keyOwner(m.focusedSlot), m.focusedSlot, key)
	return m, nil
}

// keyOwner is the plugin that receives keys for slot: the surface's occupant,
// or, for the surface opened by the active full-canvas/stage rail item, the
// plugin that declared that rail item.
func (m *WorkbenchModel) keyOwner(slot protocol.SurfaceID) string {
	if owner := m.surfaceOwner(slot); owner != "" {
		return owner
	}
	if ri, ok := m.activeRailItem(); ok {
		if (ri.Placement == contracts.PlacementFullCanvas && slot == protocol.SurfaceFullCanvas) ||
			(ri.Placement == contracts.PlacementStage && slot == protocol.SurfaceWorkspaceStage) {
			return ri.Plugin
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

// pastePalette appends pasted text to the palette query. Pastes are handled
// from the tea.PasteMsg itself rather than inferred from the bracketed key
// spelling used for plugins, so ASCII and non-ASCII text behave alike. The
// query is a single line: line breaks and tabs become spaces and other
// control characters are dropped.
func cleanQueryPaste(content string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, strings.ReplaceAll(content, "\r\n", "\n"))
}

func (m *WorkbenchModel) pastePalette(content string) (tea.Model, tea.Cmd) {
	clean := cleanQueryPaste(content)
	if clean == "" {
		return m, nil
	}
	m.paletteQuery += clean
	m.paletteCursor, m.paletteTop = 0, 0
	return m, nil
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
	if m.activePlacement == contracts.PlacementFullCanvas || y < 1 || y >= m.height-1 {
		return ""
	}
	if m.showSidebar {
		if sbRect, ok := m.slotRects[protocol.SurfaceSidebarList]; ok {
			rightEdge := sbRect.X + sbRect.Width - 1
			if x == rightEdge || x == rightEdge+1 {
				return "sidebar-width"
			}
		}
		if navRect, ok := m.slotRects[protocol.SurfaceSidebarNavigation]; ok {
			if x >= navRect.X+1 && x < navRect.X+navRect.Width-1 {
				botEdge := navRect.Y + navRect.Height - 1
				if y == botEdge || y == botEdge+1 {
					return "sidebar-nav-height"
				}
			}
		}
		if footRect, ok := m.slotRects[protocol.SurfaceSidebarFooter]; ok {
			if x >= footRect.X+1 && x < footRect.X+footRect.Width-1 {
				topEdge := footRect.Y
				if y == topEdge-1 || y == topEdge {
					return "sidebar-foot-height"
				}
			}
		}
	}
	if sideRect, ok := m.slotRects[protocol.SurfaceSidePanel]; ok {
		// The centre column's right edge sits surfaceGutter cells before the
		// gutter, so the band spans from that edge to the panel's own left
		// edge; the blank gutter cell between them is inside it.
		leftEdge := sideRect.X
		if x >= leftEdge-1-surfaceGutter && x <= leftEdge {
			return "sidepanel-width"
		}
	}
	if hdrRect, ok := m.slotRects[protocol.SurfaceMainHeader]; ok {
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
	if m.showPalette || m.secretPrompt != nil || m.tooSmall() {
		return m, nil
	}
	msg := event.Mouse()
	if click, ok := event.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && image.Pt(msg.X, msg.Y).In(themePillRect(m.width, m.height, m.palette.Name)) {
		m.run("husk.settings.open", map[string]any{"tab": "theme"})
		return m, nil
	}
	if m.settings != nil {
		return m.settingsMouse(event)
	}

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
			if slot == protocol.SurfaceModal {
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
					m.openPalette()
					return m, nil
				}
			}
		}
	}

	for _, hit := range m.hitOrder() {
		slotID, rect := hit.ID, hit.Rect
		if slotID == protocol.SurfaceActivityRail {
			continue
		}
		if image.Pt(msg.X, msg.Y).In(rect.Rect()) {
			if isLeftClick && (slotID == protocol.SurfaceSidebarList || slotID == protocol.SurfaceMainStage || slotID == protocol.SurfaceSidePanel || slotID == protocol.SurfaceWorkspaceStage || slotID == protocol.SurfaceFullCanvas) {
				m.setFocus(slotID)
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

			m.sendMouse(m.surfaceOwner(slotID), m.publisherMousePayload(slotID, relX, relY, btn))
			return m, nil
		}
	}

	return m, nil
}

// closeHitAt reports the framed surface (modal / full-canvas / stage) whose
// close button covers (x, y), if that surface declared one.
func (m *WorkbenchModel) closeHitAt(x, y int) (protocol.SurfaceID, bool) {
	// While a modal is open it is the topmost surface: clicks never reach a
	// close button on the underlying canvas/stage (matching esc, which only
	// dismisses the modal).
	if m.modalOwner().Open() {
		spec := m.frameFor(protocol.SurfaceModal)
		if !spec.Close {
			return "", false
		}
		rect := m.modalRect
		cx := rect.X + rect.Width - 1
		if spec.Border {
			cx = rect.X + rect.Width - 2
		}
		if y == rect.Y && (x == cx || (spec.Border && x == cx-1)) {
			return protocol.SurfaceModal, true
		}
		return "", false
	}
	for _, slot := range []protocol.SurfaceID{protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage} {
		var rect SlotRect
		switch slot {
		case protocol.SurfaceModal:
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
