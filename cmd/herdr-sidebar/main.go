package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"husk/pkg/herdr"
	"husk/pkg/huskipc"
	"husk/pkg/theme"
)

type ViewMode string

const (
	ViewModeSplit  ViewMode = "split"
	ViewModeTree   ViewMode = "tree"
	ViewModeAgents ViewMode = "agents"
)

type SortMode string

const (
	SortPriority SortMode = "priority"
	SortSpaces   SortMode = "spaces"
)

type SelectableItem struct {
	ID          string
	Kind        string // "workspace", "tab", "pane", "agent"
	WorkspaceID string
	TabID       string
	PaneID      string
	Label       string
}

type SidebarPlugin struct {
	mu sync.Mutex

	huskWriter *huskipc.ConnWriter
	herdrCli   *herdr.Client

	palette      theme.Palette
	slotSizes    map[huskipc.SlotID]huskipc.SlotSize
	focusedSlot  huskipc.SlotID
	modifiers    huskipc.ModifierState
	lastKeyState *huskipc.KeyStatePayload

	snap         *herdr.SessionSnapshot
	lastErr      string
	connected    bool
	eventCount   uint64
	lastEvent    string
	spinnerFrame int

	viewMode     ViewMode
	sortMode     SortMode
	cursor       int
	scrollOffset int
	items        []SelectableItem

	selectedWorkspaceID string
	selectedTabID       string
	selectedPaneID      string
	selfPaneID          string
	selfDetectedPanes   map[string]bool
	previewPaneText     string
	previewPaneID       string
	previewLayout       *herdr.PaneLayoutResult
	lastPreviewFetch    time.Time
	lastSnapFetch       time.Time
}

var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func main() {
	lipgloss.SetColorProfile(termenv.TrueColor)

	huskSock := os.Getenv("HUSK_SOCKET_PATH")
	if huskSock == "" {
		fmt.Fprintln(os.Stderr, "HUSK_SOCKET_PATH is required")
		os.Exit(1)
	}

	conn, err := net.DialTimeout("unix", huskSock, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to husk socket %s: %v\n", huskSock, err)
		os.Exit(1)
	}
	defer conn.Close()

	selfPane := os.Getenv("HERDR_PANE_ID")
	selfMap := make(map[string]bool)
	if selfPane != "" {
		selfMap[selfPane] = true
	}

	p := &SidebarPlugin{
		huskWriter: huskipc.NewConnWriter(conn),
		herdrCli:   herdr.NewClient(""),
		palette:    theme.Themes[0],
		slotSizes: map[huskipc.SlotID]huskipc.SlotSize{
			huskipc.SlotSidebarNavigation: {Width: 36, Height: 2, Visible: true},
			huskipc.SlotSidebarList:       {Width: 36, Height: 28, Visible: true, Focused: true},
			huskipc.SlotSidebarFooter:     {Width: 36, Height: 2, Visible: true},
			huskipc.SlotMainHeader:        {Width: 62, Height: 2, Visible: true},
			huskipc.SlotMainStage:         {Width: 62, Height: 32, Visible: true},
			huskipc.SlotSidePanel:         {Width: 36, Height: 36, Visible: true},
		},
		focusedSlot:       huskipc.SlotSidebarList,
		viewMode:          ViewModeSplit,
		sortMode:          SortPriority,
		selfPaneID:        selfPane,
		selfDetectedPanes: selfMap,
	}

	if err := p.sendHello(); err != nil {
		os.Exit(1)
	}

	p.refreshSnapshot(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.subscribeHerdrLoop(ctx)
	go p.tickerLoop(ctx)

	_ = huskipc.ReadLoop(conn, p.handleHostMessage)
}

func (p *SidebarPlugin) sendHello() error {
	return p.huskWriter.Send(huskipc.Envelope{
		Type: "hello",
		Hello: &huskipc.HelloPayload{
			PluginID:    "dolphin.herdr-sidebar",
			Name:        "Herdr Sidebar",
			Version:     "0.1.0",
			Language:    "Go 1.26 (Charm Lipgloss)",
			PID:         os.Getpid(),
			Description: "Live Herdr Spaces, Worktrees, Tabs, Panes & Agents sidebar + stage preview",
			Slots: []huskipc.SlotID{
				huskipc.SlotSidebarNavigation,
				huskipc.SlotSidebarList,
				huskipc.SlotThreadRowStatus,
				huskipc.SlotSidebarFooter,
				huskipc.SlotMainHeader,
				huskipc.SlotMainStage,
				huskipc.SlotSidePanel,
			},
			Commands: []huskipc.CommandSpec{
				{
					ID:          "herdr.focus_selected",
					Title:       "Herdr: Focus Selected Space / Agent in Herdr",
					Category:    "Herdr",
					Shortcut:    "enter",
					Description: "Switches the live Herdr server focus to the highlighted item",
				},
				{
					ID:          "herdr.view.split",
					Title:       "Sidebar: Switch to Spaces + Agents Split View",
					Category:    "View",
					Shortcut:    "1",
					Description: "Shows Spaces & Worktrees above Live Agents",
				},
				{
					ID:          "herdr.view.tree",
					Title:       "Sidebar: Switch to Workspace → Tab → Pane Tree View",
					Category:    "View",
					Shortcut:    "2",
					Description: "Shows full hierarchical tree of all Herdr tabs and panes",
				},
				{
					ID:          "herdr.view.agents",
					Title:       "Sidebar: Switch to Agent Mission Control View",
					Category:    "View",
					Shortcut:    "3",
					Description: "Shows detailed agent cards with context % bars and models",
				},
				{
					ID:          "herdr.sort.toggle",
					Title:       "Herdr: Toggle Agent Sort Order (Priority / Spaces)",
					Category:    "Herdr",
					Shortcut:    "o",
					Description: "Cycles agent sorting between status priority and workspace number",
				},
				{
					ID:          "herdr.workspace.create",
					Title:       "Herdr: Create New Workspace",
					Category:    "Herdr",
					Shortcut:    "n",
					Description: "Creates a new workspace in the live Herdr server",
				},
				{
					ID:          "herdr.refresh",
					Title:       "Herdr: Force Snapshot Refresh",
					Category:    "Herdr",
					Shortcut:    "r",
					Description: "Re-fetches session.snapshot and pane.read from herdr.sock",
				},
			},
			Keybindings: []huskipc.KeybindSpec{
				{Key: "1", CommandID: "herdr.view.split", Description: "Split View"},
				{Key: "2", CommandID: "herdr.view.tree", Description: "Tree View"},
				{Key: "3", CommandID: "herdr.view.agents", Description: "Agents View"},
				{Key: "o", CommandID: "herdr.sort.toggle", Description: "Toggle Sort"},
				{Key: "r", CommandID: "herdr.refresh", Description: "Refresh Herdr"},
				{Key: "n", CommandID: "herdr.workspace.create", Description: "New Space"},
			},
		},
	})
}

func (p *SidebarPlugin) isSelfPaneLocked(paneID string) bool {
	if paneID == "" {
		return false
	}
	return p.selfDetectedPanes[paneID]
}

func (p *SidebarPlugin) subscribeHerdrLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = p.herdrCli.SubscribeEvents(ctx, func(ev herdr.SubscriptionEvent) {
			p.mu.Lock()
			if ev.PaneID != "" && p.isSelfPaneLocked(ev.PaneID) && ev.Event == "pane.updated" {
				p.mu.Unlock()
				return
			}
			if time.Since(p.lastSnapFetch) < 150*time.Millisecond {
				p.mu.Unlock()
				return
			}
			p.lastSnapFetch = time.Now()
			p.eventCount++
			p.lastEvent = ev.Event
			p.mu.Unlock()
			p.refreshSnapshot(false)
		})
		time.Sleep(1500 * time.Millisecond)
	}
}

func (p *SidebarPlugin) tickerLoop(ctx context.Context) {
	ticker := time.NewTicker(220 * time.Millisecond)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ticks++
			p.mu.Lock()
			p.spinnerFrame = (p.spinnerFrame + 1) % len(brailleFrames)
			hasWorking := false
			if p.snap != nil {
				for _, a := range p.snap.Agents {
					if a.AgentStatus == herdr.StatusWorking {
						hasWorking = true
						break
					}
				}
			}
			p.mu.Unlock()

			if hasWorking {
				p.mu.Lock()
				p.renderSidebarListLocked()
				p.mu.Unlock()
			}
		}
	}
}

func (p *SidebarPlugin) refreshSnapshot(forcePreview bool) {
	snap, err := p.herdrCli.GetSnapshot()
	p.mu.Lock()
	if err != nil {
		p.connected = false
		p.lastErr = err.Error()
		p.renderAllSlotsLocked()
		p.mu.Unlock()
		return
	}
	p.connected = true
	p.lastErr = ""
	p.snap = snap
	p.rebuildSelectableItemsLocked()

	if p.selectedWorkspaceID == "" {
		p.selectedWorkspaceID = snap.FocusedWorkspaceID
		p.selectedTabID = snap.FocusedTabID
		p.selectedPaneID = snap.FocusedPaneID
		for idx, it := range p.items {
			if it.Kind == "workspace" && it.WorkspaceID == snap.FocusedWorkspaceID {
				p.cursor = idx
				break
			}
		}
	}
	p.syncSelectionFromCursorLocked()
	targetPaneID := p.resolveTargetPaneIDLocked()
	shouldFetch := forcePreview || targetPaneID != p.previewPaneID || time.Since(p.lastPreviewFetch) > 800*time.Millisecond
	p.renderAllSlotsLocked()
	p.mu.Unlock()

	if shouldFetch && targetPaneID != "" {
		p.fetchPanePreview(targetPaneID)
	}
}

func (p *SidebarPlugin) refreshPreviewOnly() {
	p.mu.Lock()
	targetPaneID := p.resolveTargetPaneIDLocked()
	isSelf := p.isSelfPaneLocked(targetPaneID)
	p.mu.Unlock()
	if targetPaneID != "" && !isSelf {
		p.fetchPanePreview(targetPaneID)
	}
}

func (p *SidebarPlugin) fetchPanePreview(paneID string) {
	layout, _ := p.herdrCli.GetPaneLayout(paneID)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.previewPaneID = paneID
	p.previewLayout = layout
	p.lastPreviewFetch = time.Now()
	p.renderMainStageLocked()
	p.renderSidePanelLocked()
}

func (p *SidebarPlugin) resolveTargetPaneIDLocked() string {
	if p.snap == nil {
		return ""
	}
	if p.selectedPaneID != "" {
		return p.selectedPaneID
	}
	if p.selectedTabID != "" {
		for _, pane := range p.snap.Panes {
			if pane.TabID == p.selectedTabID && !p.isSelfPaneLocked(pane.PaneID) && pane.Focused {
				return pane.PaneID
			}
		}
		for _, pane := range p.snap.Panes {
			if pane.TabID == p.selectedTabID && !p.isSelfPaneLocked(pane.PaneID) {
				return pane.PaneID
			}
		}
		for _, pane := range p.snap.Panes {
			if pane.TabID == p.selectedTabID {
				return pane.PaneID
			}
		}
	}
	if p.selectedWorkspaceID != "" {
		for _, pane := range p.snap.Panes {
			if pane.WorkspaceID == p.selectedWorkspaceID && !p.isSelfPaneLocked(pane.PaneID) && pane.Focused {
				return pane.PaneID
			}
		}
		for _, pane := range p.snap.Panes {
			if pane.WorkspaceID == p.selectedWorkspaceID && !p.isSelfPaneLocked(pane.PaneID) {
				return pane.PaneID
			}
		}
		for _, pane := range p.snap.Panes {
			if pane.WorkspaceID == p.selectedWorkspaceID {
				return pane.PaneID
			}
		}
	}
	return p.snap.FocusedPaneID
}

func (p *SidebarPlugin) rebuildSelectableItemsLocked() {
	p.items = nil
	if p.snap == nil {
		return
	}

	switch p.viewMode {
	case ViewModeSplit:
		for _, ws := range p.snap.Workspaces {
			p.items = append(p.items, SelectableItem{
				ID:          "ws:" + ws.WorkspaceID,
				Kind:        "workspace",
				WorkspaceID: ws.WorkspaceID,
				TabID:       ws.ActiveTabID,
				Label:       ws.Label,
			})
		}
		for _, ag := range p.sortedAgentsLocked() {
			p.items = append(p.items, SelectableItem{
				ID:          "ag:" + ag.PaneID,
				Kind:        "agent",
				WorkspaceID: ag.WorkspaceID,
				TabID:       ag.TabID,
				PaneID:      ag.PaneID,
				Label:       ag.TerminalTitleStripped,
			})
		}

	case ViewModeTree:
		for _, ws := range p.snap.Workspaces {
			p.items = append(p.items, SelectableItem{
				ID:          "ws:" + ws.WorkspaceID,
				Kind:        "workspace",
				WorkspaceID: ws.WorkspaceID,
				TabID:       ws.ActiveTabID,
				Label:       ws.Label,
			})
			for _, tab := range p.snap.Tabs {
				if tab.WorkspaceID != ws.WorkspaceID {
					continue
				}
				p.items = append(p.items, SelectableItem{
					ID:          "tab:" + tab.TabID,
					Kind:        "tab",
					WorkspaceID: ws.WorkspaceID,
					TabID:       tab.TabID,
					Label:       tab.Label,
				})
				for _, pane := range p.snap.Panes {
					if pane.TabID != tab.TabID {
						continue
					}
					p.items = append(p.items, SelectableItem{
						ID:          "pane:" + pane.PaneID,
						Kind:        "pane",
						WorkspaceID: ws.WorkspaceID,
						TabID:       tab.TabID,
						PaneID:      pane.PaneID,
						Label:       pane.PaneID,
					})
				}
			}
		}

	case ViewModeAgents:
		for _, ag := range p.sortedAgentsLocked() {
			p.items = append(p.items, SelectableItem{
				ID:          "ag:" + ag.PaneID,
				Kind:        "agent",
				WorkspaceID: ag.WorkspaceID,
				TabID:       ag.TabID,
				PaneID:      ag.PaneID,
				Label:       ag.TerminalTitleStripped,
			})
		}
	}

	if p.cursor >= len(p.items) {
		p.cursor = max(0, len(p.items)-1)
	}
}

func (p *SidebarPlugin) sortedAgentsLocked() []herdr.AgentInfo {
	if p.snap == nil {
		return nil
	}
	agents := make([]herdr.AgentInfo, len(p.snap.Agents))
	copy(agents, p.snap.Agents)

	wsNum := map[string]int{}
	for _, w := range p.snap.Workspaces {
		wsNum[w.WorkspaceID] = w.Number
	}

	statusRank := func(s herdr.AgentStatus) int {
		switch s {
		case herdr.StatusBlocked:
			return 0
		case herdr.StatusWorking:
			return 1
		case herdr.StatusDone:
			return 2
		case herdr.StatusIdle:
			return 3
		default:
			return 4
		}
	}

	sort.SliceStable(agents, func(i, j int) bool {
		a, b := agents[i], agents[j]
		if p.sortMode == SortPriority {
			ra, rb := statusRank(a.AgentStatus), statusRank(b.AgentStatus)
			if ra != rb {
				return ra < rb
			}
		}
		wa, wb := wsNum[a.WorkspaceID], wsNum[b.WorkspaceID]
		if wa != wb {
			return wa < wb
		}
		return a.PaneID < b.PaneID
	})
	return agents
}

func (p *SidebarPlugin) syncSelectionFromCursorLocked() {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return
	}
	it := p.items[p.cursor]
	p.selectedWorkspaceID = it.WorkspaceID
	p.selectedTabID = it.TabID
	p.selectedPaneID = it.PaneID
}

func (p *SidebarPlugin) handleHostMessage(env huskipc.Envelope) {
	p.mu.Lock()
	switch env.Type {
	case "welcome":
		if env.Welcome != nil {
			p.palette = env.Welcome.Palette
			for k, v := range env.Welcome.SlotSizes {
				p.slotSizes[k] = v
			}
			p.focusedSlot = env.Welcome.FocusedSlot
			p.modifiers = env.Welcome.Modifiers
			p.renderAllSlotsLocked()
		}
	case "key_state":
		if env.KeyState != nil {
			p.modifiers = env.KeyState.Modifiers
			p.lastKeyState = env.KeyState
			p.renderSidebarNavLocked()
			p.renderSidebarFooterLocked()
			p.renderSidePanelLocked()
		}
	case "slot_resize":
		if env.SlotResize != nil {
			for k, v := range env.SlotResize.SlotSizes {
				p.slotSizes[k] = v
			}
			p.focusedSlot = env.SlotResize.FocusedSlot
			p.renderAllSlotsLocked()
		}
	case "theme_changed":
		if env.ThemeChanged != nil {
			p.palette = env.ThemeChanged.Palette
			p.renderAllSlotsLocked()
		}
	case "slot_key":
		if env.SlotKey != nil {
			p.handleKeyLocked(env.SlotKey.Key)
		}
	case "slot_mouse":
		if env.SlotMouse != nil {
			p.handleMouseLocked(env.SlotMouse)
		}
	case "command_invoke":
		if env.CommandInvoke != nil {
			p.handleCommandLocked(env.CommandInvoke.CommandID)
		}
	}
	p.mu.Unlock()
}

func (p *SidebarPlugin) handleKeyLocked(key string) {
	switch key {
	case "j", "down":
		if len(p.items) > 0 {
			p.cursor = (p.cursor + 1) % len(p.items)
			p.syncSelectionFromCursorLocked()
			targetPane := p.resolveTargetPaneIDLocked()
			p.renderAllSlotsLocked()
			go p.fetchPanePreview(targetPane)
		}
	case "k", "up":
		if len(p.items) > 0 {
			p.cursor = (p.cursor - 1 + len(p.items)) % len(p.items)
			p.syncSelectionFromCursorLocked()
			targetPane := p.resolveTargetPaneIDLocked()
			p.renderAllSlotsLocked()
			go p.fetchPanePreview(targetPane)
		}
	case "g", "home":
		if len(p.items) > 0 {
			p.cursor = 0
			p.syncSelectionFromCursorLocked()
			targetPane := p.resolveTargetPaneIDLocked()
			p.renderAllSlotsLocked()
			go p.fetchPanePreview(targetPane)
		}
	case "G", "end":
		if len(p.items) > 0 {
			p.cursor = len(p.items) - 1
			p.syncSelectionFromCursorLocked()
			targetPane := p.resolveTargetPaneIDLocked()
			p.renderAllSlotsLocked()
			go p.fetchPanePreview(targetPane)
		}
	case "enter", "f":
		p.activateCurrentSelectionLocked()
	case "1":
		p.handleCommandLocked("herdr.view.split")
	case "2":
		p.handleCommandLocked("herdr.view.tree")
	case "3":
		p.handleCommandLocked("herdr.view.agents")
	case "o":
		p.handleCommandLocked("herdr.sort.toggle")
	case "r":
		p.handleCommandLocked("herdr.refresh")
	case "n":
		p.handleCommandLocked("herdr.workspace.create")
	}
}

func (p *SidebarPlugin) handleMouseLocked(m *huskipc.SlotMousePayload) {
	switch m.Button {
	case "wheel_down":
		p.handleKeyLocked("j")
		return
	case "wheel_up":
		p.handleKeyLocked("k")
		return
	}

	if m.HitAction == "view_mode" && m.HitPayload != nil {
		mode := m.HitPayload["mode"]
		switch mode {
		case "split":
			p.handleCommandLocked("herdr.view.split")
		case "tree":
			p.handleCommandLocked("herdr.view.tree")
		case "agents":
			p.handleCommandLocked("herdr.view.agents")
		}
		return
	}

	if m.HitAction == "toggle_sort" {
		p.handleCommandLocked("herdr.sort.toggle")
		return
	}

	if m.HitAction == "focus_tab" && m.HitPayload != nil {
		tabID := m.HitPayload["tab_id"]
		if tabID != "" {
			p.selectedTabID = tabID
			go func() {
				_ = p.herdrCli.FocusTab(tabID)
				p.refreshSnapshot(true)
			}()
		}
		return
	}

	if m.HitAction == "select_item" && m.HitID != "" {
		for idx, it := range p.items {
			if it.ID == m.HitID {
				alreadySelected := p.cursor == idx
				p.cursor = idx
				p.syncSelectionFromCursorLocked()
				targetPane := p.resolveTargetPaneIDLocked()
				p.renderAllSlotsLocked()
				go p.fetchPanePreview(targetPane)
				if alreadySelected {
					p.activateCurrentSelectionLocked()
				}
				break
			}
		}
	}
}

func (p *SidebarPlugin) handleCommandLocked(cmdID string) {
	switch cmdID {
	case "herdr.focus_selected":
		p.activateCurrentSelectionLocked()
	case "herdr.view.split":
		p.viewMode = ViewModeSplit
		p.rebuildSelectableItemsLocked()
		p.syncSelectionFromCursorLocked()
		p.renderAllSlotsLocked()
	case "herdr.view.tree":
		p.viewMode = ViewModeTree
		p.rebuildSelectableItemsLocked()
		p.syncSelectionFromCursorLocked()
		p.renderAllSlotsLocked()
	case "herdr.view.agents":
		p.viewMode = ViewModeAgents
		p.rebuildSelectableItemsLocked()
		p.syncSelectionFromCursorLocked()
		p.renderAllSlotsLocked()
	case "herdr.sort.toggle":
		if p.sortMode == SortPriority {
			p.sortMode = SortSpaces
		} else {
			p.sortMode = SortPriority
		}
		p.rebuildSelectableItemsLocked()
		p.syncSelectionFromCursorLocked()
		p.renderAllSlotsLocked()
		_ = p.huskWriter.Send(huskipc.Envelope{
			Type: "toast",
			Toast: &huskipc.ToastPayload{
				PluginID: "dolphin.herdr-sidebar",
				Level:    "info",
				Title:    "Agent Sort Mode",
				Message:  fmt.Sprintf("Sorted by %s", strings.ToUpper(string(p.sortMode))),
			},
		})
	case "herdr.refresh":
		go func() {
			p.refreshSnapshot(true)
			_ = p.huskWriter.Send(huskipc.Envelope{
				Type: "toast",
				Toast: &huskipc.ToastPayload{
					PluginID: "dolphin.herdr-sidebar",
					Level:    "success",
					Title:    "Herdr Synced",
					Message:  "Refreshed live session snapshot over herdr.sock",
				},
			})
		}()
	case "herdr.workspace.create":
		go func() {
			label := fmt.Sprintf("husk-%d", time.Now().Unix()%1000)
			if err := p.herdrCli.CreateWorkspace(label); err == nil {
				p.refreshSnapshot(true)
				_ = p.huskWriter.Send(huskipc.Envelope{
					Type: "toast",
					Toast: &huskipc.ToastPayload{
						PluginID: "dolphin.herdr-sidebar",
						Level:    "success",
						Title:    "Workspace Created",
						Message:  fmt.Sprintf("Created space '%s' in Herdr", label),
					},
				})
			}
		}()
	}
}

func (p *SidebarPlugin) activateCurrentSelectionLocked() {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return
	}
	it := p.items[p.cursor]
	go func() {
		var err error
		switch it.Kind {
		case "workspace":
			err = p.herdrCli.FocusWorkspace(it.WorkspaceID)
		case "tab":
			err = p.herdrCli.FocusTab(it.TabID)
		case "pane":
			err = p.herdrCli.FocusPane(it.PaneID)
		case "agent":
			err = p.herdrCli.FocusAgent(it.PaneID)
		}
		if err == nil {
			_ = p.huskWriter.Send(huskipc.Envelope{
				Type: "toast",
				Toast: &huskipc.ToastPayload{
					PluginID: "dolphin.herdr-sidebar",
					Level:    "success",
					Title:    "Herdr Focus Updated",
					Message:  fmt.Sprintf("Focused %s (%s) in live Herdr server", it.Label, it.Kind),
				},
			})
			p.refreshSnapshot(true)
		} else {
			_ = p.huskWriter.Send(huskipc.Envelope{
				Type: "toast",
				Toast: &huskipc.ToastPayload{
					PluginID: "dolphin.herdr-sidebar",
					Level:    "error",
					Title:    "Herdr RPC Error",
					Message:  err.Error(),
				},
			})
		}
	}()
}

func (p *SidebarPlugin) renderAllSlotsLocked() {
	p.publishWorkspaceContextLocked()
	p.renderSidebarNavLocked()
	p.renderSidebarListLocked()
	p.renderSidebarFooterLocked()
	p.renderMainHeaderLocked()
	p.renderMainStageLocked()
	p.renderSidePanelLocked()
}

func (p *SidebarPlugin) publishWorkspaceContextLocked() {
	if p.snap == nil {
		return
	}
	var wsLabel, repoName, cwd string
	for _, ws := range p.snap.Workspaces {
		if ws.WorkspaceID == p.selectedWorkspaceID {
			wsLabel = ws.Label
			if ws.Worktree != nil {
				repoName = ws.Worktree.RepoName
				cwd = ws.Worktree.CheckoutPath
			}
			break
		}
	}
	paneID := p.resolveTargetPaneIDLocked()
	for _, sp := range p.snap.Panes {
		if sp.PaneID == paneID {
			if sp.ForegroundCwd != "" {
				cwd = sp.ForegroundCwd
			} else if sp.Cwd != "" && cwd == "" {
				cwd = sp.Cwd
			}
			break
		}
	}
	if cwd == "" {
		cwd = "/work/lab"
	}
	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "workspace_context",
		WorkspaceContext: &huskipc.WorkspaceContextPayload{
			WorkspaceID:    p.selectedWorkspaceID,
			WorkspaceLabel: wsLabel,
			TabID:          p.selectedTabID,
			PaneID:         paneID,
			Cwd:            cwd,
			RepoName:       repoName,
		},
	})
}

func (p *SidebarPlugin) renderSidebarNavLocked() {
	sz := p.slotSizes[huskipc.SlotSidebarNavigation]
	w := max(20, sz.Width)

	activePill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(p.palette.Canvas)).
		Background(lipgloss.Color(p.palette.Accent)).
		Bold(true).
		Padding(0, 1)

	inactivePill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(p.palette.InkMuted)).
		Background(lipgloss.Color(p.palette.SurfaceElevated)).
		Padding(0, 1)

	renderTab := func(key, label string, mode ViewMode) string {
		text := key + " " + label
		if p.modifiers.Alt {
			text = "[" + key + "] " + label
		}
		if p.viewMode == mode {
			return activePill.Render(text)
		}
		if p.modifiers.Alt {
			return lipgloss.NewStyle().
				Foreground(lipgloss.Color(p.palette.Warning)).
				Background(lipgloss.Color(p.palette.SurfaceElevated)).
				Bold(true).
				Padding(0, 1).
				Render(text)
		}
		return inactivePill.Render(text)
	}

	row0 := lipgloss.JoinHorizontal(
		lipgloss.Center,
		renderTab("1", "Spaces", ViewModeSplit),
		" ",
		renderTab("2", "Tree", ViewModeTree),
		" ",
		renderTab("3", "Agents", ViewModeAgents),
	)

	wsCount, agCount, workingCount, blockedCount := 0, 0, 0, 0
	if p.snap != nil {
		wsCount = len(p.snap.Workspaces)
		agCount = len(p.snap.Agents)
		for _, a := range p.snap.Agents {
			if a.AgentStatus == herdr.StatusWorking {
				workingCount++
			} else if a.AgentStatus == herdr.StatusBlocked {
				blockedCount++
			}
		}
	}

	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle))
	sortBadge := lipgloss.NewStyle().
		Foreground(lipgloss.Color(p.palette.AccentSecondary)).
		Render(fmt.Sprintf("o:%s", p.sortMode))

	summary := fmt.Sprintf(" %d sp · %d ag", wsCount, agCount)
	if workingCount > 0 {
		summary += lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Success)).Render(fmt.Sprintf(" · %d⚡", workingCount))
	}
	if blockedCount > 0 {
		summary += lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Warning)).Render(fmt.Sprintf(" · %d!", blockedCount))
	}

	gap := max(1, w-ansi.StringWidth(summary)-ansi.StringWidth(sortBadge)-1)
	row1 := fitLine(metaStyle.Render(summary)+strings.Repeat(" ", gap)+sortBadge+" ", w)

	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "slot_update",
		SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID: "dolphin.herdr-sidebar",
			Slot:     huskipc.SlotSidebarNavigation,
			Title:    "Navigation",
			Badge:    strings.ToUpper(string(p.viewMode)),
			Lines: []string{
				fitLine(row0, w),
				row1,
			},
			HitTargets: []huskipc.RowHitTarget{
				{
					Line:    0,
					ID:      "nav-tabs",
					Kind:    "nav",
					Action:  "view_mode",
					Payload: map[string]string{"mode": nextViewMode(p.viewMode)},
				},
				{
					Line:   1,
					ID:     "nav-sort",
					Kind:   "nav",
					Action: "toggle_sort",
				},
			},
		},
	})
}

func nextViewMode(m ViewMode) string {
	switch m {
	case ViewModeSplit:
		return "tree"
	case ViewModeTree:
		return "agents"
	default:
		return "split"
	}
}

func (p *SidebarPlugin) renderSidebarListLocked() {
	sz := p.slotSizes[huskipc.SlotSidebarList]
	w := max(22, sz.Width)
	h := max(6, sz.Height)

	if !p.connected || p.snap == nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Danger)).Bold(true)
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkMuted))
		lines := []string{
			errStyle.Render("⚠ Herdr Disconnected"),
			muted.Render(p.herdrCli.SocketPath),
			muted.Render(p.lastErr),
			"",
			muted.Render("Press 'r' to reconnect"),
		}
		_ = p.huskWriter.Send(huskipc.Envelope{
			Type: "slot_update",
			SlotUpdate: &huskipc.SlotUpdatePayload{
				PluginID: "dolphin.herdr-sidebar",
				Slot:     huskipc.SlotSidebarList,
				Title:    "Herdr Spaces & Agents",
				Badge:    "OFFLINE",
				Lines:    padLines(lines, w, h),
			},
		})
		return
	}

	var allLines []string
	var allHits []huskipc.RowHitTarget
	cursorLineIdx := 0

	sectionHdr := lipgloss.NewStyle().
		Foreground(lipgloss.Color(p.palette.InkSubtle)).
		Bold(true)

	wsByID := map[string]herdr.WorkspaceInfo{}
	for _, ws := range p.snap.Workspaces {
		wsByID[ws.WorkspaceID] = ws
	}

	switch p.viewMode {
	case ViewModeSplit:
		hdr := fmt.Sprintf("SPACES (%d)", len(p.snap.Workspaces))
		allLines = append(allLines, fitLine(sectionHdr.Render(hdr), w))

		itemIdx := 0
		for _, ws := range p.snap.Workspaces {
			selected := itemIdx == p.cursor
			if selected {
				cursorLineIdx = len(allLines)
			}
			line := p.formatWorkspaceRowLocked(ws, selected, w)
			allHits = append(allHits, huskipc.RowHitTarget{
				Line:   len(allLines),
				ID:     "ws:" + ws.WorkspaceID,
				Kind:   "workspace",
				Action: "select_item",
			})
			allLines = append(allLines, line)
			itemIdx++
		}

		allLines = append(allLines, "")
		agents := p.sortedAgentsLocked()
		agHdr := fmt.Sprintf("AGENTS (%d) · %s", len(agents), strings.ToUpper(string(p.sortMode)))
		allLines = append(allLines, fitLine(sectionHdr.Render(agHdr), w))

		for _, ag := range agents {
			selected := itemIdx == p.cursor
			if selected {
				cursorLineIdx = len(allLines)
			}
			ws := wsByID[ag.WorkspaceID]
			line1, line2 := p.formatAgentRowsLocked(ag, ws, selected, w)
			allHits = append(allHits,
				huskipc.RowHitTarget{
					Line:   len(allLines),
					ID:     "ag:" + ag.PaneID,
					Kind:   "agent",
					Action: "select_item",
				},
				huskipc.RowHitTarget{
					Line:   len(allLines) + 1,
					ID:     "ag:" + ag.PaneID,
					Kind:   "agent",
					Action: "select_item",
				},
			)
			allLines = append(allLines, line1, line2)
			itemIdx++
		}

	case ViewModeTree:
		allLines = append(allLines, fitLine(sectionHdr.Render("SESSION TOPOLOGY TREE"), w))
		itemIdx := 0
		for _, ws := range p.snap.Workspaces {
			wsSel := itemIdx == p.cursor
			if wsSel {
				cursorLineIdx = len(allLines)
			}
			allHits = append(allHits, huskipc.RowHitTarget{
				Line:   len(allLines),
				ID:     "ws:" + ws.WorkspaceID,
				Kind:   "workspace",
				Action: "select_item",
			})
			allLines = append(allLines, p.formatWorkspaceRowLocked(ws, wsSel, w))
			itemIdx++

			for _, tab := range p.snap.Tabs {
				if tab.WorkspaceID != ws.WorkspaceID {
					continue
				}
				tabSel := itemIdx == p.cursor
				if tabSel {
					cursorLineIdx = len(allLines)
				}
				allHits = append(allHits, huskipc.RowHitTarget{
					Line:   len(allLines),
					ID:     "tab:" + tab.TabID,
					Kind:   "tab",
					Action: "select_item",
				})
				allLines = append(allLines, p.formatTabRowLocked(tab, tabSel, w))
				itemIdx++

				for _, pane := range p.snap.Panes {
					if pane.TabID != tab.TabID {
						continue
					}
					paneSel := itemIdx == p.cursor
					if paneSel {
						cursorLineIdx = len(allLines)
					}
					allHits = append(allHits, huskipc.RowHitTarget{
						Line:   len(allLines),
						ID:     "pane:" + pane.PaneID,
						Kind:   "pane",
						Action: "select_item",
					})
					allLines = append(allLines, p.formatPaneRowLocked(pane, paneSel, w))
					itemIdx++
				}
			}
		}

	case ViewModeAgents:
		agents := p.sortedAgentsLocked()
		hdr := fmt.Sprintf("AGENT MISSION CONTROL (%d)", len(agents))
		allLines = append(allLines, fitLine(sectionHdr.Render(hdr), w))
		for idx, ag := range agents {
			selected := idx == p.cursor
			if selected {
				cursorLineIdx = len(allLines)
			}
			ws := wsByID[ag.WorkspaceID]
			line1, line2 := p.formatAgentRowsLocked(ag, ws, selected, w)
			allHits = append(allHits,
				huskipc.RowHitTarget{
					Line:   len(allLines),
					ID:     "ag:" + ag.PaneID,
					Kind:   "agent",
					Action: "select_item",
				},
				huskipc.RowHitTarget{
					Line:   len(allLines) + 1,
					ID:     "ag:" + ag.PaneID,
					Kind:   "agent",
					Action: "select_item",
				},
			)
			allLines = append(allLines, line1, line2)
		}
	}

	if cursorLineIdx < p.scrollOffset {
		p.scrollOffset = cursorLineIdx
	} else if cursorLineIdx >= p.scrollOffset+h {
		p.scrollOffset = cursorLineIdx - h + 2
	}
	p.scrollOffset = max(0, min(p.scrollOffset, max(0, len(allLines)-h)))

	visibleLines := make([]string, 0, h)
	for i := p.scrollOffset; i < len(allLines) && len(visibleLines) < h; i++ {
		visibleLines = append(visibleLines, allLines[i])
	}
	visibleLines = padLines(visibleLines, w, h)

	var visibleHits []huskipc.RowHitTarget
	for _, ht := range allHits {
		rel := ht.Line - p.scrollOffset
		if rel >= 0 && rel < h {
			ht.Line = rel
			visibleHits = append(visibleHits, ht)
		}
	}

	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "slot_update",
		SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID:   "dolphin.herdr-sidebar",
			Slot:       huskipc.SlotSidebarList,
			Title:      "Spaces & Agents",
			Badge:      fmt.Sprintf("%d/%d", p.cursor+1, max(1, len(p.items))),
			Lines:      visibleLines,
			HitTargets: visibleHits,
		},
	})
}

func (p *SidebarPlugin) formatWorkspaceRowLocked(ws herdr.WorkspaceInfo, selected bool, width int) string {
	spaceCol := p.palette.SpaceColor(ws.SpaceColorKey())
	glyph := p.statusGlyphLocked(ws.AgentStatus, spaceCol)

	bar := " "
	if selected {
		bar = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true).Render("▎")
	} else if ws.Focused {
		bar = lipgloss.NewStyle().Foreground(spaceCol).Bold(true).Render("•")
	}

	numStr := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render(fmt.Sprintf("%2d", ws.Number))

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Ink))
	if ws.Focused || selected {
		labelStyle = labelStyle.Bold(true)
	}
	if ws.Focused {
		labelStyle = labelStyle.Foreground(spaceCol)
	}

	var badges []string
	if ws.Worktree != nil {
		if ws.Worktree.IsLinkedWorktree {
			badges = append(badges, lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Info)).Render("⎇ wt"))
		} else if ws.Worktree.RepoName != "" && !strings.EqualFold(ws.Worktree.RepoName, ws.Label) {
			badges = append(badges, lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render("⎇ "+ws.Worktree.RepoName))
		}
	}
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render(fmt.Sprintf("%dp", ws.PaneCount))
	badges = append(badges, meta)
	rightStr := strings.Join(badges, " ")
	rightW := ansi.StringWidth(rightStr)

	leftPrefix := fmt.Sprintf("%s%s %s ", bar, numStr, glyph)
	leftPrefixW := ansi.StringWidth(leftPrefix)

	availLabel := max(4, width-leftPrefixW-rightW-2)
	truncLabel := ansi.Truncate(ws.Label, availLabel, "…")
	styledLabel := labelStyle.Render(truncLabel)

	gap := max(1, width-leftPrefixW-ansi.StringWidth(styledLabel)-rightW)
	row := leftPrefix + styledLabel + strings.Repeat(" ", gap) + rightStr

	if selected {
		return styleSelectedRow(row, width, p.palette.SurfaceSelected)
	}
	return fitLine(row, width)
}

func (p *SidebarPlugin) formatTabRowLocked(tab herdr.TabInfo, selected bool, width int) string {
	prefix := "   ├─ "
	if selected {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true).Render("▎") + "  ├─ "
	}
	col := lipgloss.Color(p.palette.InkMuted)
	if tab.Focused {
		col = lipgloss.Color(p.palette.AccentSecondary)
	}
	lbl := lipgloss.NewStyle().Foreground(col).Render(fmt.Sprintf("tab %d: %s", tab.Number, tab.Label))
	right := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render(fmt.Sprintf("%dp", tab.PaneCount))
	gap := max(1, width-ansi.StringWidth(prefix)-ansi.StringWidth(lbl)-ansi.StringWidth(right))
	row := prefix + lbl + strings.Repeat(" ", gap) + right
	if selected {
		return styleSelectedRow(row, width, p.palette.SurfaceSelected)
	}
	return fitLine(row, width)
}

func (p *SidebarPlugin) formatPaneRowLocked(pane herdr.PaneInfo, selected bool, width int) string {
	prefix := "   │  └─ "
	if selected {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true).Render("▎") + "  │  └─ "
	}
	glyph := p.statusGlyphLocked(pane.AgentStatus, lipgloss.Color(p.palette.Info))
	title := pane.TerminalTitleStripped
	if title == "" {
		title = filepath.Base(pane.Cwd)
	}
	lbl := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkMuted)).Render(ansi.Truncate(title, max(6, width-16), "…"))
	row := fmt.Sprintf("%s%s %s", prefix, glyph, lbl)
	if selected {
		return styleSelectedRow(row, width, p.palette.SurfaceSelected)
	}
	return fitLine(row, width)
}

func (p *SidebarPlugin) formatAgentRowsLocked(ag herdr.AgentInfo, ws herdr.WorkspaceInfo, selected bool, width int) (string, string) {
	spaceCol := p.palette.SpaceColor(ws.SpaceColorKey())
	glyph := p.statusGlyphLocked(ag.AgentStatus, spaceCol)

	bar := " "
	if selected {
		bar = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true).Render("▎")
	} else if ag.Focused {
		bar = lipgloss.NewStyle().Foreground(spaceCol).Bold(true).Render("•")
	}

	title := ag.TerminalTitleStripped
	if title == "" {
		title = ag.Title
	}
	if title == "" {
		title = ws.Label
	}

	wsBadge := lipgloss.NewStyle().
		Foreground(spaceCol).
		Render(fmt.Sprintf("#%d", ws.Number))

	ctxPctStr := ag.Tokens["context_used_pct"]
	gauge := p.renderContextGaugeLocked(ctxPctStr)

	leftPrefix := fmt.Sprintf("%s%s ", bar, glyph)
	right1 := wsBadge
	if gauge != "" {
		right1 = gauge + " " + wsBadge
	}

	availTitle := max(6, width-ansi.StringWidth(leftPrefix)-ansi.StringWidth(right1)-2)
	tStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Ink))
	if selected || ag.Focused {
		tStyle = tStyle.Bold(true)
	}
	styledTitle := tStyle.Render(ansi.Truncate(title, availTitle, "…"))
	gap1 := max(1, width-ansi.StringWidth(leftPrefix)-ansi.StringWidth(styledTitle)-ansi.StringWidth(right1))
	line1 := leftPrefix + styledTitle + strings.Repeat(" ", gap1) + right1

	modelInfo := ag.Tokens["model_effort"]
	if modelInfo == "" {
		modelInfo = ag.Tokens["model"]
	}
	if modelInfo == "" {
		modelInfo = ag.Agent
	}
	subLeft := "   " + lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render(ansi.Truncate(modelInfo, max(8, width-12), "…"))
	statusPill := p.statusLabelStyledLocked(ag.AgentStatus)
	gap2 := max(1, width-ansi.StringWidth(subLeft)-ansi.StringWidth(statusPill))
	line2 := subLeft + strings.Repeat(" ", gap2) + statusPill

	if selected {
		return styleSelectedRow(line1, width, p.palette.SurfaceSelected),
			styleSelectedRow(line2, width, p.palette.SurfaceSelected)
	}
	return fitLine(line1, width), fitLine(line2, width)
}

func (p *SidebarPlugin) renderContextGaugeLocked(pctStr string) string {
	if pctStr == "" {
		return ""
	}
	numStr := strings.TrimSuffix(strings.TrimSpace(pctStr), "%")
	pct, err := strconv.Atoi(numStr)
	if err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render(pctStr)
	}
	col := lipgloss.Color(p.palette.Success)
	if pct >= 80 {
		col = lipgloss.Color(p.palette.Danger)
	} else if pct >= 50 {
		col = lipgloss.Color(p.palette.Warning)
	}
	filled := max(1, min(4, (pct+24)/25))
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", 4-filled)
	return lipgloss.NewStyle().Foreground(col).Render(fmt.Sprintf("%s %d%%", bar, pct))
}

func (p *SidebarPlugin) statusGlyphLocked(st herdr.AgentStatus, fallbackCol lipgloss.Color) string {
	switch st {
	case herdr.StatusBlocked:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Danger)).Bold(true).Render("●")
	case herdr.StatusWorking:
		frame := brailleFrames[p.spinnerFrame%len(brailleFrames)]
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Success)).Bold(true).Render(frame)
	case herdr.StatusDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Info)).Bold(true).Render("✓")
	default:
		return lipgloss.NewStyle().Foreground(fallbackCol).Render("○")
	}
}

func (p *SidebarPlugin) statusLabelStyledLocked(st herdr.AgentStatus) string {
	switch st {
	case herdr.StatusBlocked:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Danger)).Bold(true).Render("blocked")
	case herdr.StatusWorking:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Success)).Render("working")
	case herdr.StatusDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Info)).Render("done")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render("idle")
	}
}

func (p *SidebarPlugin) renderSidebarFooterLocked() {
	sz := p.slotSizes[huskipc.SlotSidebarFooter]
	w := max(20, sz.Width)

	var dot, stateText string
	if p.connected && p.snap != nil {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Success)).Bold(true).Render("●")
		stateText = fmt.Sprintf("herdr v%s · proto %d · ev #%d", p.snap.Version, p.snap.Protocol, p.eventCount)
	} else {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Danger)).Bold(true).Render("●")
		stateText = "disconnected from herdr.sock"
	}

	line1 := fmt.Sprintf("%s %s", dot, lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkMuted)).Render(stateText))
	line2 := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render("⏎ focus herdr · j/k nav · o sort · n new")
	if p.modifiers.Alt {
		line2 = lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.palette.Warning)).
			Bold(true).
			Render("⌥ ALT HELD · [1-3]View [o]Sort [⏎]Focus [n]New")
	}

	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "slot_update",
		SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID: "dolphin.herdr-sidebar",
			Slot:     huskipc.SlotSidebarFooter,
			Title:    "Daemon Link",
			Badge:    fmt.Sprintf("PID %d", os.Getpid()),
			Lines: []string{
				fitLine(line1, w),
				fitLine(line2, w),
			},
		},
	})
}

func (p *SidebarPlugin) renderMainHeaderLocked() {
	sz := p.slotSizes[huskipc.SlotMainHeader]
	w := max(40, sz.Width)

	if p.snap == nil {
		return
	}

	var activeWS *herdr.WorkspaceInfo
	for i := range p.snap.Workspaces {
		if p.snap.Workspaces[i].WorkspaceID == p.selectedWorkspaceID {
			activeWS = &p.snap.Workspaces[i]
			break
		}
	}
	if activeWS == nil && len(p.snap.Workspaces) > 0 {
		activeWS = &p.snap.Workspaces[0]
	}
	if activeWS == nil {
		return
	}

	spaceCol := p.palette.SpaceColor(activeWS.SpaceColorKey())
	wsPill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(p.palette.Canvas)).
		Background(spaceCol).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("#%d %s", activeWS.Number, activeWS.Label))

	var wtInfo string
	if activeWS.Worktree != nil {
		wtBadge := "repo:" + activeWS.Worktree.RepoName
		if activeWS.Worktree.IsLinkedWorktree {
			wtBadge += " (linked worktree)"
		}
		wtInfo = lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Info)).Render(" ⎇ " + wtBadge)
	}

	var tabPills []string
	for _, tab := range p.snap.Tabs {
		if tab.WorkspaceID != activeWS.WorkspaceID {
			continue
		}
		isCurrent := tab.TabID == p.selectedTabID || (p.selectedTabID == "" && tab.TabID == activeWS.ActiveTabID)
		if isCurrent {
			tabPills = append(tabPills, lipgloss.NewStyle().
				Foreground(lipgloss.Color(p.palette.Ink)).
				Background(lipgloss.Color(p.palette.SurfaceSelected)).
				Bold(true).
				Padding(0, 1).
				Render(fmt.Sprintf("Tab %d: %s", tab.Number, tab.Label)))
		} else {
			tabPills = append(tabPills, lipgloss.NewStyle().
				Foreground(lipgloss.Color(p.palette.InkSubtle)).
				Background(lipgloss.Color(p.palette.SurfaceElevated)).
				Padding(0, 1).
				Render(fmt.Sprintf("Tab %d: %s", tab.Number, tab.Label)))
		}
	}

	row0 := wsPill + wtInfo + "   " + strings.Join(tabPills, " ")
	sub := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle)).Render(
		fmt.Sprintf("Workspace %s · %d panes · Active Tab %s · Press Enter in sidebar to focus in Herdr",
			activeWS.WorkspaceID, activeWS.PaneCount, activeWS.ActiveTabID),
	)

	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "slot_update",
		SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID: "dolphin.herdr-sidebar",
			Slot:     huskipc.SlotMainHeader,
			Title:    activeWS.Label,
			Badge:    activeWS.WorkspaceID,
			Lines: []string{
				fitLine(row0, w),
				fitLine(sub, w),
			},
		},
	})
}

func (p *SidebarPlugin) renderMainStageLocked() {
	sz := p.slotSizes[huskipc.SlotMainStage]
	w := max(40, sz.Width)
	h := max(12, sz.Height)

	if p.snap == nil {
		return
	}

	var activeWS *herdr.WorkspaceInfo
	for i := range p.snap.Workspaces {
		if p.snap.Workspaces[i].WorkspaceID == p.selectedWorkspaceID {
			activeWS = &p.snap.Workspaces[i]
			break
		}
	}
	if activeWS == nil && len(p.snap.Workspaces) > 0 {
		activeWS = &p.snap.Workspaces[0]
	}

	paneID := p.resolveTargetPaneIDLocked()

	var lines []string
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkMuted))
	subtleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle))
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Ink))

	wsTitle := "WORKSPACE TOPOLOGY & AGENT DECK"
	if activeWS != nil {
		wsTitle = fmt.Sprintf("SPACE #%d · %s (%s)", activeWS.Number, strings.ToUpper(activeWS.Label), activeWS.WorkspaceID)
	}
	headerLeft := titleStyle.Render(wsTitle)
	headerRight := subtleStyle.Render("press 'v' for Husk Slot Map")
	gap := max(1, w-ansi.StringWidth(headerLeft)-ansi.StringWidth(headerRight))
	lines = append(lines, fitLine(headerLeft+strings.Repeat(" ", gap)+headerRight, w))

	if p.previewLayout != nil && len(p.previewLayout.Panes) > 0 {
		mode := p.previewLayout.LayoutMode
		if mode == "" {
			mode = "tiled"
		}
		var chips []string
		for _, pr := range p.previewLayout.Panes {
			chip := fmt.Sprintf("[%s %dx%d]", pr.PaneID, pr.Width, pr.Height)
			if pr.PaneID == paneID {
				chips = append(chips, lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Success)).Bold(true).Render(chip))
			} else {
				chips = append(chips, subtleStyle.Render(chip))
			}
		}
		lines = append(lines, fitLine(subtleStyle.Render("BSP Geometry ("+mode+"): ")+strings.Join(chips, " "), w))
	}
	lines = append(lines, subtleStyle.Render(strings.Repeat("─", w)))

	if activeWS != nil && activeWS.Worktree != nil {
		wt := activeWS.Worktree
		wtKind := "Primary Checkout"
		if wt.IsLinkedWorktree {
			wtKind = "Linked Git Worktree (⎇)"
		}
		lines = append(lines,
			fitLine(titleStyle.Render("GIT WORKTREE"), w),
			fitLine("  "+subtleStyle.Render("Repo:     ")+valStyle.Render(wt.RepoName)+"  "+mutedStyle.Render("("+wtKind+")"), w),
			fitLine("  "+subtleStyle.Render("Checkout: ")+mutedStyle.Render(wt.CheckoutPath), w),
			"",
		)
	}

	lines = append(lines, fitLine(titleStyle.Render("TABS & PANES IN THIS SPACE"), w))
	for _, tab := range p.snap.Tabs {
		if activeWS != nil && tab.WorkspaceID != activeWS.WorkspaceID {
			continue
		}
		tabBadge := lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.palette.AccentSecondary)).
			Bold(true).
			Render(fmt.Sprintf("▾ Tab %d: %s (%s)", tab.Number, tab.Label, tab.TabID))
		lines = append(lines, fitLine("  "+tabBadge, w))

		for _, sp := range p.snap.Panes {
			if sp.TabID != tab.TabID {
				continue
			}
			isSel := sp.PaneID == paneID
			pointer := "    ├─ "
			if isSel {
				pointer = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true).Render("▸") + " ├─ "
			}
			glyph := p.statusGlyphLocked(sp.AgentStatus, lipgloss.Color(p.palette.Info))
			agentName := sp.Agent
			if agentName == "" {
				agentName = "shell"
			}
			title := sp.TerminalTitleStripped
			if title == "" {
				title = sp.Cwd
			}
			paneHdr := fmt.Sprintf("%s%s %-8s [%s] %s",
				pointer,
				glyph,
				sp.PaneID,
				agentName,
				title,
			)
			if isSel {
				lines = append(lines, styleSelectedRow(paneHdr, w, p.palette.SurfaceSelected))
			} else {
				lines = append(lines, fitLine(valStyle.Render(paneHdr), w))
			}

			var metaParts []string
			if sp.ForegroundCwd != "" {
				metaParts = append(metaParts, "cwd: "+sp.ForegroundCwd)
			}
			if m := sp.Tokens["model_effort"]; m != "" {
				metaParts = append(metaParts, "model: "+m)
			}
			if c := sp.Tokens["context_compact"]; c != "" {
				metaParts = append(metaParts, "ctx: "+c)
			}
			if len(metaParts) > 0 {
				lines = append(lines, fitLine("         "+subtleStyle.Render(strings.Join(metaParts, " · ")), w))
			}
		}
		lines = append(lines, "")
	}

	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "slot_update",
		SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID: "dolphin.herdr-sidebar",
			Slot:     huskipc.SlotMainStage,
			Title:    fmt.Sprintf("Stage · %s", paneID),
			Badge:    "TOPOLOGY DECK",
			Lines:    padLines(lines, w, h),
		},
	})
}

func (p *SidebarPlugin) renderSidePanelLocked() {
	sz := p.slotSizes[huskipc.SlotSidePanel]
	w := max(24, sz.Width)
	h := max(10, sz.Height)

	if p.snap == nil {
		return
	}

	paneID := p.resolveTargetPaneIDLocked()
	var targetPane *herdr.PaneInfo
	for i := range p.snap.Panes {
		if p.snap.Panes[i].PaneID == paneID {
			targetPane = &p.snap.Panes[i]
			break
		}
	}

	var targetWS *herdr.WorkspaceInfo
	for i := range p.snap.Workspaces {
		if p.snap.Workspaces[i].WorkspaceID == p.selectedWorkspaceID {
			targetWS = &p.snap.Workspaces[i]
			break
		}
	}

	secStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Accent)).Bold(true)
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.InkSubtle))
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(p.palette.Ink))

	kv := func(k, v string) string {
		prefix := keyStyle.Render(fmt.Sprintf("%-11s ", k+":"))
		avail := max(6, w-ansi.StringWidth(prefix))
		return fitLine(prefix+valStyle.Render(ansi.Truncate(v, avail, "…")), w)
	}

	var lines []string
	lines = append(lines, secStyle.Render("SELECTED SPACE"))
	if targetWS != nil {
		lines = append(lines,
			kv("Space", fmt.Sprintf("#%d %s (%s)", targetWS.Number, targetWS.Label, targetWS.WorkspaceID)),
			kv("Status", string(targetWS.AgentStatus)),
			kv("Topology", fmt.Sprintf("%d tabs · %d panes", targetWS.TabCount, targetWS.PaneCount)),
		)
		if targetWS.Worktree != nil {
			lines = append(lines,
				kv("Repo", targetWS.Worktree.RepoName),
				kv("Linked WT", fmt.Sprintf("%v", targetWS.Worktree.IsLinkedWorktree)),
				kv("Checkout", targetWS.Worktree.CheckoutPath),
			)
		}
	}

	lines = append(lines, "", secStyle.Render("PANE & AGENT INSPECTOR"))
	if targetPane != nil {
		lines = append(lines,
			kv("Pane ID", targetPane.PaneID),
			kv("Terminal", fmt.Sprintf("%v", targetPane.TerminalID)),
			kv("Agent", targetPane.Agent),
			kv("Status", string(targetPane.AgentStatus)),
			kv("Title", targetPane.TerminalTitleStripped),
			kv("CWD", targetPane.ForegroundCwd),
		)
		if targetPane.AgentSession != nil {
			lines = append(lines,
				kv("Session", targetPane.AgentSession.Value),
				kv("Source", targetPane.AgentSession.Source),
			)
		}
		if len(targetPane.Tokens) > 0 {
			lines = append(lines, "", secStyle.Render("AGENT TOKENS"))
			keys := []string{"model", "effort", "context_used_pct", "context_used", "context_left", "context_window"}
			for _, k := range keys {
				if v, ok := targetPane.Tokens[k]; ok && v != "" {
					lines = append(lines, kv(k, v))
				}
			}
		}
	}

	lines = append(lines, "", secStyle.Render("OUT-OF-PROCESS IPC"))
	lines = append(lines,
		kv("Plugin PID", fmt.Sprintf("%d (Go)", os.Getpid())),
		kv("Herdr Evts", fmt.Sprintf("%d (%s)", p.eventCount, p.lastEvent)),
		kv("Theme", p.palette.Name),
	)

	_ = p.huskWriter.Send(huskipc.Envelope{
		Type: "slot_update",
		SlotUpdate: &huskipc.SlotUpdatePayload{
			PluginID: "dolphin.herdr-sidebar",
			Slot:     huskipc.SlotSidePanel,
			Title:    "Inspector",
			Badge:    paneID,
			Lines:    padLines(lines, w, h),
		},
	})
}

func fitLine(s string, width int) string {
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

func styleSelectedRow(s string, width int, bgHex string) string {
	fitted := fitLine(s, width)
	return lipgloss.NewStyle().Background(lipgloss.Color(bgHex)).Render(fitted)
}

func padLines(lines []string, width, height int) []string {
	out := make([]string, 0, height)
	for i := 0; i < len(lines) && i < height; i++ {
		out = append(out, fitLine(lines[i], width))
	}
	blank := strings.Repeat(" ", max(0, width))
	for len(out) < height {
		out = append(out, blank)
	}
	return out
}
