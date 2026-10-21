package main

import (
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
			active:   m.activeRailID == "" && m.stageMode != "hotspots" && m.activeModalRailID == "",
			kind:     "home",
		},
	}

	for _, item := range m.orderedRailItems() {
		isAct := m.activeRailID == item.ID || m.activeModalRailID == item.ID
		btns = append(btns, railButton{
			icon:     item.Icon,
			label:    item.Label,
			shortcut: strings.ToUpper(item.Shortcut),
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
	if x < railRect.X || x >= railRect.X+railRect.Width || y < railRect.Y || y >= railRect.Y+railRect.Height {
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

func (m *WorkbenchModel) orderedRailItems() []huskipc.RailItemSpec {
	order := []string{"rail.tv-search", "rail.fleet-dashboard", "rail.project-git"}
	var res []huskipc.RailItemSpec
	seen := map[string]bool{}
	for _, want := range order {
		for _, item := range m.railItems {
			if item.ID == want {
				res = append(res, item)
				seen[item.ID] = true
			}
		}
	}
	for _, item := range m.railItems {
		if !seen[item.ID] {
			res = append(res, item)
		}
	}
	return res
}

type paletteCommand struct {
	huskipc.CommandSpec
	PluginID string
}

func (m *WorkbenchModel) allCommands() []paletteCommand {
	builtins := []paletteCommand{
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.home",
				Title:       "Activity Rail (🏠 F1): Return to Default 3-Column Workbench",
				Category:    "Rail Plugins",
				Shortcut:    "f1 / esc",
				Description: "Restores Sidebar + Main Stage + Side Panel layout",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.hotspots.toggle_stage",
				Title:       "Husk Workbench: Toggle Stage between Live Plugin & bb Slot Architecture Map",
				Category:    "Workbench",
				Shortcut:    "v / ctrl+i",
				Description: "Inspect all 9 bb-style workbench slots, owners, PIDs, and IPC counters",
			},
			PluginID: "husk.core",
		},
		{
			CommandSpec: huskipc.CommandSpec{
				ID:          "husk.hotspots.badges",
				Title:       "Husk Workbench: Toggle Numbered Slot Callout Badges (⓪–⑧)",
				Category:    "Workbench",
				Shortcut:    "h",
				Description: "Shows or hides numbered bb hotspot badges in every slot header",
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

	for pid, cp := range m.plugins {
		for _, c := range cp.Info.Commands {
			builtins = append(builtins, paletteCommand{
				CommandSpec: c,
				PluginID:    pid,
			})
		}
	}
	return builtins
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

	case "tv.open":
		for _, ri := range m.railItems {
			if ri.ID == "rail.tv-search" {
				m.activateRailItem(ri)
				return m, nil
			}
		}
		return m, nil

	case "fleet.open":
		for _, ri := range m.railItems {
			if ri.ID == "rail.fleet-dashboard" {
				m.activateRailItem(ri)
				return m, nil
			}
		}
		return m, nil

	case "gitstage.open":
		for _, ri := range m.railItems {
			if ri.ID == "rail.project-git" {
				m.activateRailItem(ri)
				return m, nil
			}
		}
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

	for _, cp := range m.plugins {
		for _, c := range cp.Info.Commands {
			if c.ID == cmdID {
				_ = cp.Writer.Send(huskipc.Envelope{
					Type: "command_invoke",
					CommandInvoke: &huskipc.CommandInvokePayload{
						CommandID: cmdID,
					},
				})
				return m, nil
			}
		}
	}
	return m, nil
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
