package main

import (
	"image"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
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
	railRect, ok := m.slotRects[protocol.SurfaceActivityRail]
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
	return m.builtinCommands()
}

// builtinCommands lists the host's own workbench commands. Each row's shortcut
// column shows the spellings the effective keymap binds to it.
func (m *WorkbenchModel) builtinCommands() []paletteCommand {
	rows := []paletteCommand{
		{
			ID:          "husk.home",
			Title:       "Activity Rail (🏠 F1): Return to Default Workbench Layout",
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
	}
	for i := range rows {
		rows[i].PluginID = "husk.core"
		rows[i].Shortcut = keyHintText(m.keys.KeysFor(rows[i].ID))
	}
	return rows
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
		hints = append(hints, footerHint{keyLabel(strings.Join(h.Keys, " ")), h.Text})
	}
	resize := "drag "
	if k := m.firstKey("husk.layout.reset"); k != "" {
		resize = "drag/" + k + " "
	}
	hints = append(hints, footerHint{resize, "resize"})
	var docks []string
	for _, a := range []string{"husk.sidebar.toggle", "husk.sidepanel.toggle", "husk.docks.cycle"} {
		if k := m.firstKey(a); k != "" {
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
