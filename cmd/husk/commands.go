package main

import (
	"image"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

type railButton struct {
	icon     string
	label    string
	shortcut string
	active   bool
	kind     string // "home", "plugin", "hotspots", "palette"
	item     huskipc.RailItemSpec
	startY   int // relative to rail top (0..bodyH-1)
	endY     int
}

func (m *WorkbenchModel) railButtons() []railButton {
	rect := m.slotRects[huskipc.SlotActivityRail]
	bodyH := rect.Height

	btns := []railButton{
		{
			icon:     "",
			label:    "Home Workbench",
			shortcut: "F1",
			active:   m.activeRailID == "" && m.stageMode != "hotspots" && !m.modalOwner().Open(),
			kind:     "home",
		},
	}

	for _, item := range m.orderedRailItems() {
		isAct := m.activeRailID == item.ID || m.modalOwner().RailID == item.ID
		btns = append(btns, railButton{
			icon:     item.Icon,
			label:    item.Label,
			shortcut: railShortcutLabel(item.Shortcut),
			active:   isAct,
			kind:     "plugin",
			item:     item,
		})
	}

	btns = append(btns,
		railButton{
			icon:     "",
			label:    "Slot Architecture Lab",
			shortcut: "v",
			active:   m.stageMode == "hotspots",
			kind:     "hotspots",
		},
		railButton{
			icon:     "",
			label:    "Command Palette",
			shortcut: "ctrl+p",
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
			curY += 3
		}
	}
	return btns
}

func (m *WorkbenchModel) detectRailHoverAt(x, y int) int {
	railRect, ok := m.slotRects[huskipc.SlotActivityRail]
	if !ok {
		return -1
	}
	if !image.Pt(x, y).In(railRect.Rect()) {
		return -1
	}
	relY := y - railRect.Y
	for i, b := range m.railButtons() {
		if relY >= b.startY && relY <= b.endY {
			return i
		}
	}
	return -1
}

// orderedRailItems returns rail items in registration order: plugins appear in
// the order they connected, items in the order each plugin declared them. Husk
// has no preferred application order.
func (m *WorkbenchModel) orderedRailItems() []huskipc.RailItemSpec {
	return slices.Clone(m.railItems)
}

// firstRailItemWithPlacement returns the first registered rail item that opens
// the given placement.
func (m *WorkbenchModel) firstRailItemWithPlacement(p huskipc.RailPlacement) (huskipc.RailItemSpec, bool) {
	for _, item := range m.orderedRailItems() {
		if item.Placement == p {
			return item, true
		}
	}
	return huskipc.RailItemSpec{}, false
}

type paletteCommand struct {
	huskipc.CommandSpec
	PluginID string
}

func (m *WorkbenchModel) allCommands() []paletteCommand {
	builtins := builtinCommands()
	for _, pid := range m.sortedPluginIDs() {
		for _, c := range m.plugins[pid].Info.Commands {
			builtins = append(builtins, paletteCommand{
				CommandSpec: c,
				PluginID:    pid,
			})
		}
	}
	return builtins
}

// builtinCommands lists the host's own workbench commands.
func builtinCommands() []paletteCommand {
	return []paletteCommand{
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.home",
				Title:       "Activity Rail (🏠 F1): Return to Default Workbench Layout",
				Category:    "Rail Plugins",
				Shortcut:    "f1 / esc",
				Description: "Restores the default layout: main stage plus any sidebar or side panel a plugin publishes",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.hotspots.toggle_stage",
				Title:       "Husk Workbench: Toggle Stage between Live Plugin & Slot Architecture Map",
				Category:    "Workbench",
				Shortcut:    "v / ctrl+i",
				Description: "Inspect every workbench slot and its owner",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.hotspots.badges",
				Title:       "Husk Workbench: Toggle Numbered Slot Callout Badges (⓪–⑧)",
				Category:    "Workbench",
				Shortcut:    "h",
				Description: "Shows or hides numbered slot badges on every surface",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.sidebar.toggle",
				Title:       "Husk Workbench: Toggle Left Sidebar",
				Category:    "Workbench",
				Shortcut:    "[",
				Description: "Collapses or expands the left sidebar column",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.sidepanel.toggle",
				Title:       "Husk Workbench: Toggle Right Inspector Drawer (side-panel)",
				Category:    "Workbench",
				Shortcut:    "]",
				Description: "Collapses or expands the right side-panel slot",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.docks.cycle",
				Title:       "Husk Workbench: Cycle Dock Layout (Both / Left / Right / Zen Stage)",
				Category:    "Workbench",
				Shortcut:    "/",
				Description: "Cycles left sidebar and right side-panel visibility",
			},
			PluginID: "husk.core",
		},
	}
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

func (m *WorkbenchModel) executeCommand(cmdID string) (tea.Model, tea.Cmd) {
	switch cmdID {
	case "husk.home":
		m.activateHomeWorkbench()
		return m, nil

	case "husk.hotspots.toggle_stage":
		m.activeRailID = ""
		m.activePlacement = ""
		if m.stageMode == "plugin" {
			m.stageMode = "hotspots"
			m.focusedSlot = huskipc.SlotMainStage
		} else {
			m.stageMode = "plugin"
			m.focusedSlot = huskipc.SlotSidebarList
		}
		m.recomputeLayout()
		return m, nil

	case "husk.hotspots.badges":
		m.showHotspots = !m.showHotspots
		return m, nil

	case "husk.sidebar.toggle":
		m.showSidebar = !m.showSidebar
		m.recomputeLayout()
		m.broadcastSlotResize()
		return m, nil

	case "husk.sidepanel.toggle":
		m.showSidePane = !m.showSidePane
		m.recomputeLayout()
		m.broadcastSlotResize()
		return m, nil

	case "husk.docks.cycle":
		switch {
		case m.showSidebar && m.showSidePane:
			m.showSidebar = true
			m.showSidePane = false
		case m.showSidebar && !m.showSidePane:
			m.showSidebar = false
			m.showSidePane = true
		case !m.showSidebar && m.showSidePane:
			m.showSidebar = false
			m.showSidePane = false
		default:
			m.showSidebar = true
			m.showSidePane = true
		}
		m.recomputeLayout()
		m.broadcastSlotResize()
		return m, nil
	}

	// A command whose ID names a rail item activates that item, so plugins can
	// expose their rail items in the palette and bind keys to them.
	if ri, ok := m.railItemByID(cmdID); ok {
		m.activateRailItem(ri)
		return m, nil
	}

	for _, pid := range m.sortedPluginIDs() {
		cp := m.plugins[pid]
		for _, c := range cp.Info.Commands {
			if c.ID == cmdID {
				m.sendCommandInvoke(cp, cmdID)
				return m, nil
			}
		}
	}
	return m, nil
}

func (m *WorkbenchModel) sendCommandInvoke(cp *ConnectedPlugin, cmdID string) {
	_ = cp.Writer.Send(huskipc.Envelope{
		Type: "command_invoke",
		CommandInvoke: &huskipc.CommandInvokePayload{
			CommandID: cmdID,
		},
	})
}

func (m *WorkbenchModel) sortedPluginIDs() []string {
	return slices.Sorted(maps.Keys(m.plugins))
}

// isHostCommand reports whether cmdID is handled by the host itself: a
// built-in husk.* command or the ID of a registered rail item.
func (m *WorkbenchModel) isHostCommand(cmdID string) bool {
	for _, c := range builtinCommands() {
		if c.ID == cmdID {
			return true
		}
	}
	_, ok := m.railItemByID(cmdID)
	return ok
}

// runPluginBinding executes a key binding declared by pluginID: host commands
// and rail item IDs run in the host, anything else goes to the declaring
// plugin as command_invoke (whether or not it also lists the command).
func (m *WorkbenchModel) runPluginBinding(pluginID, cmdID string) (tea.Model, tea.Cmd) {
	if m.isHostCommand(cmdID) {
		return m.executeCommand(cmdID)
	}
	if cp, ok := m.plugins[pluginID]; ok {
		m.sendCommandInvoke(cp, cmdID)
	}
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

// railShortcutItem returns the rail item whose declared shortcut is key.
// Shortcuts use the host's slot_key spelling, compared exactly.
func (m *WorkbenchModel) railShortcutItem(key string) (huskipc.RailItemSpec, bool) {
	for _, item := range m.orderedRailItems() {
		if item.Shortcut != "" && item.Shortcut == key {
			if _, ok := m.plugins[item.PluginID]; ok {
				return item, true
			}
		}
	}
	return huskipc.RailItemSpec{}, false
}

// isChordKey reports whether key is a modified (ctrl+/alt+) or function key:
// a key a surface occupant does not receive as plain typing or navigation.
func isChordKey(key string) bool {
	if strings.HasPrefix(key, "ctrl+") || strings.HasPrefix(key, "alt+") {
		return true
	}
	if len(key) >= 2 && key[0] == 'f' {
		if _, err := strconv.Atoi(key[1:]); err == nil {
			return true
		}
	}
	return false
}

// keyBinding finds the plugin key binding for key given the focused surface.
// Precedence: bindings scoped to the focused surface (when_slot), then global
// bindings of the focused surface's owner, then global bindings of other
// plugins. Ties resolve by plugin ID, then declaration order. While the
// focused surface is occupied, other plugins' global bindings apply only to
// chord keys (isChordKey); every other key belongs to the occupant, so an
// unscoped single-letter binding cannot steal typing from a focused plugin.
func (m *WorkbenchModel) keyBinding(key string, focused huskipc.SlotID) (pluginID string, kb huskipc.KeybindSpec, ok bool) {
	ids := m.sortedPluginIDs()
	for _, pid := range ids {
		for _, b := range m.plugins[pid].Info.Keybindings {
			if b.Key == key && b.WhenSlot != "" && b.WhenSlot == focused {
				return pid, b, true
			}
		}
	}
	owner := m.keyOwner(focused)
	cp, occupied := m.plugins[owner]
	if occupied {
		for _, b := range cp.Info.Keybindings {
			if b.Key == key && b.WhenSlot == "" {
				return owner, b, true
			}
		}
		if !isChordKey(key) {
			return "", huskipc.KeybindSpec{}, false
		}
	}
	for _, pid := range ids {
		if pid == owner {
			continue
		}
		for _, b := range m.plugins[pid].Info.Keybindings {
			if b.Key == key && b.WhenSlot == "" {
				return pid, b, true
			}
		}
	}
	return "", huskipc.KeybindSpec{}, false
}

func (m *WorkbenchModel) showLocalToast(level, title, message string) tea.Cmd {
	m.toastSeq++
	seq := m.toastSeq
	m.activeToast = &huskipc.ToastPayload{
		PluginID: "husk.core",
		Level:    level,
		Title:    title,
		Message:  message,
	}
	return tea.Tick(2600*time.Millisecond, func(time.Time) tea.Msg {
		return toastExpireMsg{id: seq}
	})
}

type footerHint struct{ key, desc string }

// maxPluginFooterHints caps how many plugin-declared bindings the footer
// advertises so the host's own hints and the ipc counter keep their room.
const maxPluginFooterHints = 4

// footerHints returns the key hints the footer shows. Host chrome keys (home,
// docks, palette, quit) are always present; action keys come only from
// keybindings the plugin owning the focused surface declared with a
// description, or from the built-in slot map when it is the focused stage.
// With no plugins connected nothing application-specific is advertised.
func (m *WorkbenchModel) footerHints() []footerHint {
	hints := []footerHint{{"F1", " home"}}
	if m.stageMode == "hotspots" && m.focusedSlot == huskipc.SlotMainStage {
		hints = append(hints, footerHint{"j/k", "nav"})
	}
	seen := map[string]bool{}
	if cp, ok := m.plugins[m.keyOwner(m.focusedSlot)]; ok {
		n := 0
		for _, b := range cp.Info.Keybindings {
			if n == maxPluginFooterHints {
				break
			}
			if b.Description == "" || b.Key == "" || seen[b.Key] {
				continue
			}
			if b.WhenSlot != "" && b.WhenSlot != m.focusedSlot {
				continue
			}
			seen[b.Key] = true
			hints = append(hints, footerHint{railShortcutLabel(b.Key), b.Description})
			n++
		}
	}
	return append(hints,
		footerHint{"drag/= ", "resize"},
		footerHint{"[ ] /", "docks"},
		footerHint{"ctrl+p", "palette"},
		footerHint{"q", "quit"},
	)
}
