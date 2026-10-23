package main

import (
	"image"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type railButton struct {
	icon     string
	label    string
	shortcut string
	active   bool
	kind     string // "home", "plugin", "hotspots", "palette"
	item     contracts.RailItem
	startY   int // relative to rail top (0..bodyH-1)
	endY     int
}

func (m *WorkbenchModel) railButtons() []railButton {
	rect := m.slotRects[protocol.SurfaceActivityRail]
	bodyH := rect.Height

	btns := []railButton{
		{
			icon:     "",
			label:    "Home Workbench",
			shortcut: keyLabel(m.firstKey("husk.home")),
			active:   m.activeRailID == "" && m.stageMode != "hotspots" && !m.modalOwner().Open(),
			kind:     "home",
		},
	}

	for _, item := range m.orderedRailItems() {
		isAct := m.activeRailID == item.ID || m.modalOwner().RailID == item.ID
		btns = append(btns, railButton{
			icon:     item.Icon,
			label:    item.Label,
			shortcut: keyLabel(m.firstKey(item.ID)),
			active:   isAct,
			kind:     "plugin",
			item:     item,
		})
	}

	btns = append(btns,
		railButton{
			icon:     "",
			label:    "Slot Architecture Lab",
			shortcut: keyLabel(m.firstKey("husk.hotspots.toggle_stage")),
			active:   m.stageMode == "hotspots",
			kind:     "hotspots",
		},
		railButton{
			icon:     "",
			label:    "Command Palette",
			shortcut: m.firstKey("husk.palette.open"),
			active:   m.showPalette,
			kind:     "palette",
		},
	)

	curY := 1
	for i := range btns {
		if btns[i].kind == "palette" && bodyH >= curY+6 {
			btns[i].startY = bodyH - 4
			btns[i].endY = bodyH - 2
		} else {
			btns[i].startY = curY
			btns[i].endY = curY + 2
			curY += 4
		}
	}
	return btns
}

func (m *WorkbenchModel) detectRailHoverAt(x, y int) int {
	railRect, ok := m.slotRects[protocol.SurfaceActivityRail]
	if !ok {
		return -1
	}
	if !image.Pt(x, y).In(railRect.Rect()) {
		return -1
	}
	relY := y - railRect.Y
	for i, b := range m.railButtons() {
		if b.endY < railRect.Height && relY >= b.startY && relY <= b.endY {
			return i
		}
	}
	return -1
}

// orderedRailItems returns rail items in registration order: plugins appear in
// the order they connected, items in the order each plugin declared them. Husk
// has no preferred application order.
func (m *WorkbenchModel) orderedRailItems() []contracts.RailItem {
	return slices.Clone(m.railItems)
}

// firstRailItemWithPlacement returns the first registered rail item that opens
// the given placement.
func (m *WorkbenchModel) firstRailItemWithPlacement(p contracts.RailPlacement) (contracts.RailItem, bool) {
	for _, item := range m.orderedRailItems() {
		if item.Placement == p {
			return item, true
		}
	}
	return contracts.RailItem{}, false
}

// paletteCommand is one row of the command palette: a host command run
// through its action id.
type paletteCommand struct {
	ID          string
	Title       string
	Category    string
	Shortcut    string
	Description string
	PluginID    string
}

func (m *WorkbenchModel) allCommands() []paletteCommand {
	rows := m.builtinCommands()
	if m.fe == nil {
		return rows
	}
	for _, a := range m.fe.Actions() {
		if !m.plugins[a.Plugin] {
			continue
		}
		title := a.Title
		if title == "" {
			title = a.ID
		}
		rows = append(rows, paletteCommand{
			ID:          a.ID,
			Title:       title,
			Category:    a.Plugin,
			Shortcut:    keyHintText(m.keys.KeysFor(a.ID)),
			Description: a.Description,
			PluginID:    a.Plugin,
		})
	}
	return rows
}

// homeTitle is the palette title of husk.home; the key in it is whatever the
// keymap binds, and it is left out when nothing is.
func homeTitle(key string) string {
	if key == "" {
		return "Activity Rail (🏠): Return to Default Workbench Layout"
	}
	return "Activity Rail (🏠 " + key + "): Return to Default Workbench Layout"
}

// builtinCommands lists the host's own workbench commands. Each row's shortcut
// column shows the spellings the effective keymap binds to it.
func (m *WorkbenchModel) builtinCommands() []paletteCommand {
	rows := []paletteCommand{
		{ID: "husk.settings.open", Title: "Open settings", Category: "Workbench", Description: "Open the Settings center"},
		{ID: "husk.theme.select", Title: "Select theme", Category: "Workbench", Description: "Open Settings on the Theme tab"},
		{
			ID:          "husk.home",
			Title:       homeTitle(keyLabel(m.firstKey("husk.home"))),
			Category:    "Rail Plugins",
			Description: "Restores the default layout: main stage plus any sidebar or side panel a plugin publishes",
		},
		{
			ID:          "husk.hotspots.toggle_stage",
			Title:       "Husk Workbench: Toggle Stage between Live Plugin & Slot Architecture Map",
			Category:    "Workbench",
			Description: "Inspect every workbench slot and its owner",
		},
		{
			ID:          "husk.hotspots.badges",
			Title:       "Husk Workbench: Toggle Numbered Slot Callout Badges (⓪–⑧)",
			Category:    "Workbench",
			Description: "Shows or hides numbered slot badges on every surface",
		},
		{
			ID:          "husk.sidebar.toggle",
			Title:       "Husk Workbench: Toggle Left Sidebar",
			Category:    "Workbench",
			Description: "Collapses or expands the left sidebar column",
		},
		{
			ID:          "husk.sidepanel.toggle",
			Title:       "Husk Workbench: Toggle Right Inspector Drawer (side-panel)",
			Category:    "Workbench",
			Description: "Collapses or expands the right side-panel slot",
		},
		{
			ID:          "husk.docks.cycle",
			Title:       "Husk Workbench: Cycle Dock Layout (Both / Left / Right / Zen Stage)",
			Category:    "Workbench",
			Description: "Cycles left sidebar and right side-panel visibility",
		},
		{
			ID:          "husk.quit",
			Title:       "Husk Workbench: Quit",
			Category:    "Workbench",
			Description: "Closes the workbench and stops its plugins",
		},
		{
			ID:          "husk.palette.open",
			Title:       "Husk Workbench: Open Command Palette",
			Category:    "Workbench",
			Description: "Opens the command palette",
		},
		{
			ID:          "husk.layout.reset",
			Title:       "Husk Workbench: Reset Layout Sizes",
			Category:    "Workbench",
			Description: "Restores the default slot dimensions",
		},
		{
			ID:          "husk.sidebar.narrower",
			Title:       "Husk Workbench: Make Left Sidebar Narrower",
			Category:    "Workbench",
			Description: "Shrinks the left sidebar column",
		},
		{
			ID:          "husk.sidebar.wider",
			Title:       "Husk Workbench: Make Left Sidebar Wider",
			Category:    "Workbench",
			Description: "Widens the left sidebar column",
		},
		{
			ID:          "husk.focus.next",
			Title:       "Husk Workbench: Focus Next Surface",
			Category:    "Workbench",
			Description: "Moves focus to the next focusable surface",
		},
		{
			ID:          "husk.modifiers.toggle_alt_latch",
			Title:       "Husk Workbench: Toggle Alt Latch",
			Category:    "Workbench",
			Description: "Latches Alt on or off, so chord shortcuts can be tried without holding it",
		},
		{
			ID:          "husk.capture.release",
			Title:       "Husk Workbench: Release Capture for One Key",
			Category:    "Workbench",
			Description: "Lets the next key reach host bindings",
		},
		{
			ID:          "husk.modal.close",
			Title:       "Husk Workbench: Close Modal",
			Category:    "Workbench",
			Description: "Closes the open modal",
		},
	}
	for i := range rows {
		rows[i].PluginID = "husk.core"
		rows[i].Shortcut = keyHintText(m.keys.KeysFor(rows[i].ID))
	}
	return rows
}

// paletteVisibleRows is how many results the palette draws at once.
const paletteVisibleRows = 12

// paletteWindowTop returns the first drawn row for a list of n results so
// that the cursor is inside the window, moving it as little as possible from
// top. It is pure: View calls it too, so a stale top never hides the cursor.
func paletteWindowTop(top, cursor, n int) int {
	if n <= paletteVisibleRows {
		return 0
	}
	if cursor < top {
		top = cursor
	}
	if cursor >= top+paletteVisibleRows {
		top = cursor - paletteVisibleRows + 1
	}
	return max(0, min(top, n-paletteVisibleRows))
}

// movePaletteCursor moves the cursor by delta over n results, wrapping at both
// ends, and scrolls the window to keep it visible.
func (m *WorkbenchModel) movePaletteCursor(delta, n int) {
	m.paletteCursor = ((m.paletteCursor+delta)%n + n) % n
	m.paletteTop = paletteWindowTop(m.paletteTop, m.paletteCursor, n)
}

func (m *WorkbenchModel) filteredCommands() []paletteCommand {
	all := m.allCommands()
	q := strings.ToLower(strings.TrimSpace(m.paletteQuery))
	if q == "" {
		return all
	}
	var out []paletteCommand
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.Title), q) ||
			strings.Contains(strings.ToLower(c.Category), q) ||
			strings.Contains(strings.ToLower(c.ID), q) {
			out = append(out, c)
		}
	}
	return out
}

// executeCommand runs a palette command as the user.
func (m *WorkbenchModel) executeCommand(cmdID string) (tea.Model, tea.Cmd) {
	m.run(cmdID, nil)
	return m, nil
}

// railShortcutLabel formats a slot_key-spelled shortcut for display:
// modifiers and named keys title-cased ("ctrl+g" -> "Ctrl+g", "f4" -> "F4",
// "alt+enter" -> "Alt+Enter"). A single printable key keeps its case, so
// "d"/"D" and "alt+x"/"alt+X" stay distinct.
func railShortcutLabel(key string) string {
	parts := strings.Split(key, "+")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		// "ctrl++" style: the key itself is "+".
		parts = append(parts[:len(parts)-2], "+")
	}
	last := len(parts) - 1
	for i, p := range parts {
		if (i < last || len([]rune(p)) > 1) && p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

func (m *WorkbenchModel) showLocalToast(level, title, message string) tea.Cmd {
	m.toastSeq++
	seq := m.toastSeq
	m.activeToast = &contracts.Toast{
		Plugin:  "husk.core",
		Level:   level,
		Title:   title,
		Message: message,
	}
	m.engineToast = false
	return tea.Tick(2600*time.Millisecond, func(time.Time) tea.Msg {
		return toastExpireMsg{id: seq}
	})
}

// liveHint keeps public host and declared rail keys, but advertises plugin
// actions only while their catalog owner is live. Called after the hints cap.
func (m *WorkbenchModel) liveHint(action string) bool {
	if strings.HasPrefix(action, "husk.") {
		return true
	}
	for _, ri := range m.declared {
		if ri.ID == action {
			return true
		}
	}
	if m.fe != nil {
		for _, a := range m.fe.Actions() {
			if a.ID == action && m.plugins[a.Plugin] {
				return true
			}
		}
	}
	return false
}

type footerHint struct{ key, desc string }

// maxPluginFooterHints caps how many plugin-declared bindings the footer
// advertises so the host's own hints and the ipc counter keep their room.
const maxPluginFooterHints = 4

// footerHints returns the key hints the footer shows. Host chrome keys (home,
// docks, palette, quit) come from the effective keymap; action keys come only
// from the hints bindings declared for the focused surface. With no plugins
// connected nothing application-specific is advertised.
func (m *WorkbenchModel) footerHints() []footerHint {
	var hints []footerHint
	if m.captureReleased {
		hints = append(hints, footerHint{m.firstKey("husk.capture.release"), "release"})
	}
	if k := m.firstKey("husk.home"); k != "" {
		hints = append(hints, footerHint{keyLabel(k), "\uf015 home"})
	}
	if m.stageMode == "hotspots" && m.focusedSlot == protocol.SurfaceMainStage {
		hints = append(hints, footerHint{"j/k", "nav"})
	}
	for i, h := range m.keys.Hints(m.keyContext()) {
		if i == maxPluginFooterHints || len(h.Keys) == 0 {
			break
		}
		if m.liveHint(h.Action) {
			hints = append(hints, footerHint{keyLabel(strings.Join(h.Keys, " ")), h.Text})
		}
	}
	resize := "drag "
	if k := m.firstKey("husk.layout.reset"); k != "" {
		resize = "drag/" + k + " "
	}
	hints = append(hints, footerHint{resize, "resize"})
	var docks []string
	left := m.activePlacement != contracts.PlacementFullCanvas
	right := m.activePlacement == "" && panelFits(m.width, m.sidebarWidth, m.leftSideShown())
	for _, a := range []struct {
		id    string
		shown bool
	}{{"husk.sidebar.toggle", left}, {"husk.sidepanel.toggle", right}, {"husk.docks.cycle", left && right}} {
		if k := m.firstKey(a.id); a.shown && k != "" {
			docks = append(docks, k)
		}
	}
	if len(docks) > 0 {
		hints = append(hints, footerHint{strings.Join(docks, " "), "docks"})
	}
	if k := m.firstKey("husk.palette.open"); k != "" {
		hints = append(hints, footerHint{k, "palette"})
	}
	if k := m.firstKey("husk.quit"); k != "" {
		hints = append(hints, footerHint{k, "quit"})
	}
	return hints
}
