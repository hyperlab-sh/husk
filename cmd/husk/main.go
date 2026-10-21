package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// stringFlag is a repeatable string flag.
type stringFlag []string

func (m *stringFlag) String() string { return strings.Join(*m, ",") }

func (m *stringFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// repeatable registers a repeatable string flag and returns its pointer.
func repeatable(name, usage string) *stringFlag {
	f := &stringFlag{}
	flag.Var(f, name, usage)
	return f
}

type ConnectedPlugin struct {
	Info       huskipc.HelloPayload
	Writer     *huskipc.ConnWriter
	Closer     io.Closer
	Connected  time.Time
	FramesRecv uint64
}

type SlotRect struct {
	X      int
	Y      int
	Width  int
	Height int
	InnerX int
	InnerY int
	InnerW int
	InnerH int
}

type ipcEventMsg struct {
	pluginID string
	env      huskipc.Envelope
}

type pluginRegisteredMsg struct {
	plugin *ConnectedPlugin
}

type toastExpireMsg struct {
	id int
}

type modifierTickMsg struct{}

type WorkbenchModel struct {
	mu          sync.Mutex
	socketPath  string
	listener    net.Listener
	subChan     chan tea.Msg
	childCmds   []*exec.Cmd
	plugins     map[string]*ConnectedPlugin
	railItems   []huskipc.RailItemSpec
	slotUpdates map[huskipc.SlotID]*huskipc.SlotUpdatePayload
	slotOwners  map[huskipc.SlotID]string
	slotRects   map[huskipc.SlotID]SlotRect
	modalRect   SlotRect
	demoFrame   bool

	wsContext huskipc.WorkspaceContextPayload

	width        int
	height       int
	palette      theme.Palette
	focusedSlot  huskipc.SlotID
	showSidebar  bool
	sidebarWidth int
	sidebarNavH  int
	sidebarFootH int
	mainHeaderH  int
	showSidePane bool
	sidePaneW    int

	activeDrag   string
	hoverDivider string
	hoverRailIdx int

	modifiers  huskipc.ModifierState
	altLatched bool
	heldKeys   map[string]time.Time

	activeRailID      string // "" (Home), or rail item ID
	activePlacement   huskipc.RailPlacement
	activeModalRailID string
	activeModalPlugin string

	showHotspots   bool
	stageMode      string // "plugin" or "hotspots"
	hotspotCursor  int
	showPalette    bool
	paletteQuery   string
	paletteCursor  int
	activeToast    *huskipc.ToastPayload
	toastSeq       int
	totalIPCEvents uint64
}

const activityRailWidth = 5

func newWorkbenchModel(socketPath string, listener net.Listener, cwd string) *WorkbenchModel {
	return &WorkbenchModel{
		socketPath:   socketPath,
		listener:     listener,
		subChan:      make(chan tea.Msg, 256),
		plugins:      make(map[string]*ConnectedPlugin),
		slotUpdates:  make(map[huskipc.SlotID]*huskipc.SlotUpdatePayload),
		slotOwners:   make(map[huskipc.SlotID]string),
		slotRects:    make(map[huskipc.SlotID]SlotRect),
		wsContext:    huskipc.WorkspaceContextPayload{WorkspaceID: "demo", WorkspaceLabel: "Demo", Cwd: cwd},
		width:        144,
		height:       40,
		palette:      theme.Default(),
		focusedSlot:  huskipc.SlotSidebarList,
		showSidebar:  true,
		sidebarWidth: 38,
		sidebarNavH:  4,
		sidebarFootH: 4,
		mainHeaderH:  4,
		showSidePane: true,
		sidePaneW:    36,
		hoverRailIdx: -1,
		heldKeys:     make(map[string]time.Time),
		showHotspots: false,
		stageMode:    "plugin",
	}
}

func (m *WorkbenchModel) startAcceptLoop() {
	go func() {
		for {
			conn, err := m.listener.Accept()
			if err != nil {
				return
			}
			go m.handleStream(conn, conn, conn)
		}
	}()
}

func (m *WorkbenchModel) handleStream(r io.Reader, w io.Writer, closer io.Closer) {
	writer := huskipc.NewConnWriter(w)
	var pluginID string

	_ = huskipc.ReadLoop(r, func(env huskipc.Envelope) {
		if env.Type == "hello" && env.Hello != nil {
			pluginID = env.Hello.PluginID
			cp := &ConnectedPlugin{
				Info:      *env.Hello,
				Writer:    writer,
				Closer:    closer,
				Connected: time.Now(),
			}
			m.subChan <- pluginRegisteredMsg{plugin: cp}
			return
		}
		m.subChan <- ipcEventMsg{pluginID: pluginID, env: env}
	})
}

func (m *WorkbenchModel) spawnSocketCommand(dir string, argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "HUSK_SOCKET_PATH="+m.socketPath)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.childCmds = append(m.childCmds, cmd)
	return nil
}

func (m *WorkbenchModel) spawnStdioCommand(dir string, argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "HUSK_SOCKET_PATH="+m.socketPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.childCmds = append(m.childCmds, cmd)
	go m.handleStream(stdout, stdin, stdin)
	return nil
}

func (m *WorkbenchModel) cleanup() {
	if m.listener != nil {
		_ = m.listener.Close()
	}
	for _, cmd := range m.childCmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	_ = os.Remove(m.socketPath)
}

func waitForIPC(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

func waitForModifierTick() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg {
		return modifierTickMsg{}
	})
}

func (m *WorkbenchModel) Init() tea.Cmd {
	m.recomputeLayout()
	return tea.Batch(waitForIPC(m.subChan), waitForModifierTick())
}

func (m *WorkbenchModel) currentHeldKeys() []string {
	var out []string
	if m.modifiers.Alt {
		out = append(out, "alt")
	}
	if m.modifiers.Ctrl {
		out = append(out, "ctrl")
	}
	if m.modifiers.Shift {
		out = append(out, "shift")
	}
	if m.modifiers.Super {
		out = append(out, "super")
	}
	for k := range m.heldKeys {
		out = append(out, k)
	}
	return out
}

func (m *WorkbenchModel) broadcastKeyState(phase, key string, held bool) {
	payload := &huskipc.KeyStatePayload{
		Phase:     phase,
		Key:       key,
		Held:      held,
		Modifiers: m.modifiers,
		HeldKeys:  m.currentHeldKeys(),
		Slot:      m.focusedSlot,
	}
	for _, cp := range m.plugins {
		_ = cp.Writer.Send(huskipc.Envelope{
			Type:     "key_state",
			KeyState: payload,
		})
	}
}

func (m *WorkbenchModel) syncModifierTransitions(next huskipc.ModifierState) {
	prev := m.modifiers
	m.modifiers = next

	if prev.Alt != next.Alt {
		phase := "up"
		if next.Alt {
			phase = "down"
		}
		m.broadcastKeyState(phase, "alt", next.Alt)
	}
	if prev.Ctrl != next.Ctrl {
		phase := "up"
		if next.Ctrl {
			phase = "down"
		}
		m.broadcastKeyState(phase, "ctrl", next.Ctrl)
	}
	if prev.Shift != next.Shift {
		phase := "up"
		if next.Shift {
			phase = "down"
		}
		m.broadcastKeyState(phase, "shift", next.Shift)
	}
	if prev.Super != next.Super {
		phase := "up"
		if next.Super {
			phase = "down"
		}
		m.broadcastKeyState(phase, "super", next.Super)
	}
}

func (m *WorkbenchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(84, msg.Width)
		m.height = max(24, msg.Height)
		m.recomputeLayout()
		m.broadcastSlotResize()
		return m, nil

	case pluginRegisteredMsg:
		cp := msg.plugin
		m.plugins[cp.Info.PluginID] = cp
		for _, s := range cp.Info.Slots {
			m.slotOwners[s] = cp.Info.PluginID
		}
		for _, ri := range cp.Info.RailItems {
			ri.PluginID = cp.Info.PluginID
			replaced := false
			for idx := range m.railItems {
				if m.railItems[idx].ID == ri.ID {
					m.railItems[idx] = ri
					replaced = true
					break
				}
			}
			if !replaced {
				m.railItems = append(m.railItems, ri)
			}
		}
		m.recomputeLayout()
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "welcome",
			Welcome: &huskipc.WelcomePayload{
				HostVersion: "0.1.0",
				Palette:     m.palette,
				SlotSizes:   m.currentSlotSizes(),
				FocusedSlot: m.focusedSlot,
				Context:     m.wsContext,
				Modifiers:   m.modifiers,
			},
		})
		return m, waitForIPC(m.subChan)

	case modifierTickMsg:
		if osMods, ok := pollOSModifiers(); ok {
			if m.altLatched {
				osMods.Alt = true
			}
			m.syncModifierTransitions(osMods)
		}
		now := time.Now()
		for k, ts := range m.heldKeys {
			if now.Sub(ts) > 320*time.Millisecond {
				delete(m.heldKeys, k)
				m.broadcastKeyState("up", k, false)
			}
		}
		return m, waitForModifierTick()

	case ipcEventMsg:
		m.totalIPCEvents++
		if cp, ok := m.plugins[msg.pluginID]; ok {
			cp.FramesRecv++
		}
		if msg.env.CloseModal {
			m.activeModalRailID = ""
			m.activeModalPlugin = ""
			if m.activePlacement == huskipc.PlacementFullCanvas {
				m.activateHomeWorkbench()
			}
		}
		switch msg.env.Type {
		case "slot_update":
			if su := msg.env.SlotUpdate; su != nil {
				m.slotUpdates[su.Slot] = su
				if m.slotOwners[su.Slot] == "" {
					m.slotOwners[su.Slot] = su.PluginID
				}
			}
		case "workspace_context":
			if wc := msg.env.WorkspaceContext; wc != nil {
				changed := wc.WorkspaceID != m.wsContext.WorkspaceID || wc.Cwd != m.wsContext.Cwd
				m.wsContext = *wc
				if changed {
					for pid, cp := range m.plugins {
						if pid == msg.pluginID {
							continue
						}
						_ = cp.Writer.Send(huskipc.Envelope{
							Type:             "workspace_context",
							WorkspaceContext: wc,
						})
					}
				}
			}
		case "toast":
			if t := msg.env.Toast; t != nil {
				m.toastSeq++
				seq := m.toastSeq
				m.activeToast = t
				return m, tea.Batch(
					waitForIPC(m.subChan),
					tea.Tick(3200*time.Millisecond, func(time.Time) tea.Msg {
						return toastExpireMsg{id: seq}
					}),
				)
			}
		}
		return m, waitForIPC(m.subChan)

	case toastExpireMsg:
		if msg.id == m.toastSeq {
			m.activeToast = nil
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m *WorkbenchModel) activateHomeWorkbench() {
	m.activeRailID = ""
	m.activePlacement = ""
	m.activeModalRailID = ""
	m.activeModalPlugin = ""
	m.stageMode = "plugin"
	m.focusedSlot = huskipc.SlotSidebarList
	m.recomputeLayout()
	m.broadcastSlotResize()
}

func (m *WorkbenchModel) activateRailItem(ri huskipc.RailItemSpec) {
	cp, ok := m.plugins[ri.PluginID]
	if !ok {
		return
	}

	switch ri.Placement {
	case huskipc.PlacementModal:
		if m.activeModalRailID == ri.ID {
			m.activeModalRailID = ""
			m.activeModalPlugin = ""
			return
		}
		m.activeModalRailID = ri.ID
		m.activeModalPlugin = ri.PluginID
		m.recomputeLayout()
		rect := m.modalRect
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "rail_activated",
			RailActivated: &huskipc.RailActivatedPayload{
				RailID:    ri.ID,
				Placement: huskipc.PlacementModal,
				Active:    true,
				Width:     rect.InnerW,
				Height:    rect.InnerH,
				Context:   m.wsContext,
			},
		})

	case huskipc.PlacementFullCanvas:
		if m.activeRailID == ri.ID && m.activePlacement == huskipc.PlacementFullCanvas {
			m.activateHomeWorkbench()
			return
		}
		m.activeModalRailID = ""
		m.activeModalPlugin = ""
		m.activeRailID = ri.ID
		m.activePlacement = huskipc.PlacementFullCanvas
		m.focusedSlot = huskipc.SlotFullCanvas
		m.recomputeLayout()
		rect := m.slotRects[huskipc.SlotFullCanvas]
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "rail_activated",
			RailActivated: &huskipc.RailActivatedPayload{
				RailID:    ri.ID,
				Placement: huskipc.PlacementFullCanvas,
				Active:    true,
				Width:     rect.InnerW,
				Height:    rect.InnerH,
				Context:   m.wsContext,
			},
		})

	case huskipc.PlacementStage:
		if m.activeRailID == ri.ID && m.activePlacement == huskipc.PlacementStage {
			m.activateHomeWorkbench()
			return
		}
		m.activeModalRailID = ""
		m.activeModalPlugin = ""
		m.activeRailID = ri.ID
		m.activePlacement = huskipc.PlacementStage
		if !m.showSidebar {
			m.showSidebar = true
		}
		m.focusedSlot = huskipc.SlotWorkspaceStage
		m.recomputeLayout()
		rect := m.slotRects[huskipc.SlotWorkspaceStage]
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "rail_activated",
			RailActivated: &huskipc.RailActivatedPayload{
				RailID:    ri.ID,
				Placement: huskipc.PlacementStage,
				Active:    true,
				Width:     rect.InnerW,
				Height:    rect.InnerH,
				Context:   m.wsContext,
			},
		})
	}
}

func (m *WorkbenchModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if msg.Alt && !m.modifiers.Alt {
		next := m.modifiers
		next.Alt = true
		m.syncModifierTransitions(next)
	}

	phase := "down"
	if _, already := m.heldKeys[key]; already {
		phase = "repeat"
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
				m.paletteQuery = m.paletteQuery[:len(m.paletteQuery)-1]
				m.paletteCursor = 0
			}
			return m, nil
		default:
			if len(key) == 1 && key >= " " && key <= "~" {
				m.paletteQuery += key
				m.paletteCursor = 0
			}
			return m, nil
		}
	}

	if m.activeModalPlugin != "" {
		if key == "esc" || key == "f2" || key == "ctrl+f" {
			m.activeModalRailID = ""
			m.activeModalPlugin = ""
			return m, nil
		}
		if cp, ok := m.plugins[m.activeModalPlugin]; ok {
			_ = cp.Writer.Send(huskipc.Envelope{
				Type: "slot_key",
				SlotKey: &huskipc.SlotKeyPayload{
					Slot: string(huskipc.SlotModalSurface),
					Key:  key,
				},
			})
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		m.cleanup()
		return m, tea.Quit
	case "?":
		m.altLatched = !m.altLatched
		next := m.modifiers
		next.Alt = m.altLatched
		m.syncModifierTransitions(next)
		return m, nil
	case "esc", "f1", "alt+1", "¡":
		if m.activeRailID != "" || m.stageMode == "hotspots" {
			m.activateHomeWorkbench()
			return m, nil
		}
	case "f2", "ctrl+f", "alt+2", "™":
		return m.executeCommand("tv.open")
	case "f3", "ctrl+g", "alt+3", "£":
		return m.executeCommand("fleet.open")
	case "f4", "ctrl+e", "alt+4", "¢":
		return m.executeCommand("gitstage.open")
	case "ctrl+p", ":", "alt+p", "π":
		m.showPalette = true
		m.paletteQuery = ""
		m.paletteCursor = 0
		return m, nil
	case "ctrl+i", "v", "alt+v", "√":
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

	targetSlot := m.focusedSlot
	if m.activePlacement == huskipc.PlacementStage {
		switch key {
		case "j", "k", "up", "down", "enter", "1", "2", "3", "o", "n":
			targetSlot = huskipc.SlotSidebarList
		default:
			targetSlot = huskipc.SlotWorkspaceStage
		}
	}

	ownerID := m.slotOwners[targetSlot]
	if ownerID == "" {
		ownerID = m.slotOwners[huskipc.SlotSidebarList]
	}
	if cp, ok := m.plugins[ownerID]; ok {
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "slot_key",
			SlotKey: &huskipc.SlotKeyPayload{
				Slot: string(targetSlot),
				Key:  key,
			},
		})
	}
	return m, nil
}

func (m *WorkbenchModel) detectDividerAt(x, y int) string {
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
		leftEdge := sideRect.X
		if x == leftEdge-1 || x == leftEdge {
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

func (m *WorkbenchModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.showPalette {
		return m, nil
	}

	if msg.Action == tea.MouseActionRelease {
		if m.activeDrag != "" {
			m.activeDrag = ""
			m.hoverDivider = m.detectDividerAt(msg.X, msg.Y)
			return m, nil
		}
	}

	if msg.Action == tea.MouseActionMotion {
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

	isWheelDown := msg.Button == tea.MouseButtonWheelDown
	isWheelUp := msg.Button == tea.MouseButtonWheelUp
	isLeftClick := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft

	if !isWheelDown && !isWheelUp && !isLeftClick {
		return m, nil
	}

	if isLeftClick {
		if slot, ok := m.closeHitAt(msg.X, msg.Y); ok {
			if slot == huskipc.SlotModalSurface {
				m.activeModalRailID = ""
				m.activeModalPlugin = ""
			} else {
				m.activateHomeWorkbench()
			}
			return m, nil
		}
	}

	if isLeftClick {
		if div := m.detectDividerAt(msg.X, msg.Y); div != "" {
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

	for slotID, rect := range m.slotRects {
		if slotID == huskipc.SlotActivityRail {
			continue
		}
		if msg.X >= rect.X && msg.X < rect.X+rect.Width && msg.Y >= rect.Y && msg.Y < rect.Y+rect.Height {
			if isLeftClick && (slotID == huskipc.SlotSidebarList || slotID == huskipc.SlotMainStage || slotID == huskipc.SlotSidePanel || slotID == huskipc.SlotWorkspaceStage || slotID == huskipc.SlotFullCanvas) {
				m.focusedSlot = slotID
				m.broadcastSlotResize()
			}
			relX := msg.X - rect.InnerX
			relY := msg.Y - rect.InnerY
			if relX < 0 || relY < 0 || relX >= rect.InnerW || relY >= rect.InnerH {
				return m, nil
			}

			btn := "left"
			if isWheelDown {
				btn = "wheel_down"
			} else if isWheelUp {
				btn = "wheel_up"
			}

			var hitID, hitKind, hitAction string
			var hitPayload map[string]string
			if su := m.slotUpdates[slotID]; su != nil {
				for _, ht := range su.HitTargets {
					if ht.Line == relY {
						hitID = ht.ID
						hitKind = ht.Kind
						hitAction = ht.Action
						hitPayload = ht.Payload
						break
					}
				}
			}

			ownerID := m.slotOwners[slotID]
			if cp, ok := m.plugins[ownerID]; ok {
				_ = cp.Writer.Send(huskipc.Envelope{
					Type: "slot_mouse",
					SlotMouse: &huskipc.SlotMousePayload{
						Slot:       slotID,
						X:          relX,
						Y:          relY,
						Button:     btn,
						HitID:      hitID,
						HitKind:    hitKind,
						HitAction:  hitAction,
						HitPayload: hitPayload,
					},
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
	for _, slot := range []huskipc.SlotID{huskipc.SlotModalSurface, huskipc.SlotFullCanvas, huskipc.SlotWorkspaceStage} {
		var rect SlotRect
		switch slot {
		case huskipc.SlotModalSurface:
			if m.activeModalPlugin == "" {
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

func (m *WorkbenchModel) recomputeLayout() {
	totalW := max(84, m.width)
	bodyH := max(20, m.height-2)

	m.slotRects = make(map[huskipc.SlotID]SlotRect)

	modalW := min(92, m.width-10)
	modalH := min(24, m.height-6)
	m.modalRect = m.frameRect(huskipc.SlotModalSurface, max(4, (m.width-modalW)/2), max(2, (m.height-modalH)/2), modalW, modalH)

	railW := activityRailWidth
	m.slotRects[huskipc.SlotActivityRail] = m.frameRect(huskipc.SlotActivityRail, 0, 1, railW, bodyH)

	remW := max(40, totalW-railW)

	if m.activePlacement == huskipc.PlacementFullCanvas {
		m.slotRects[huskipc.SlotFullCanvas] = m.frameRect(huskipc.SlotFullCanvas, railW, 1, remW, bodyH)
		return
	}

	leftW := 0
	if m.showSidebar {
		leftW = min(m.sidebarWidth, remW/2)
		navH := max(3, m.sidebarNavH)
		footH := max(3, m.sidebarFootH)
		if navH+footH+6 > bodyH {
			navH = 4
			footH = 4
		}
		listH := max(6, bodyH-navH-footH)

		m.slotRects[huskipc.SlotSidebarNavigation] = m.frameRect(huskipc.SlotSidebarNavigation, railW, 1, leftW, navH)
		m.slotRects[huskipc.SlotSidebarList] = m.frameRect(huskipc.SlotSidebarList, railW, 1+navH, leftW, listH)
		m.slotRects[huskipc.SlotSidebarFooter] = m.frameRect(huskipc.SlotSidebarFooter, railW, 1+navH+listH, leftW, footH)
	}

	if m.activePlacement == huskipc.PlacementStage {
		stageW := max(30, remW-leftW)
		m.slotRects[huskipc.SlotWorkspaceStage] = m.frameRect(huskipc.SlotWorkspaceStage, railW+leftW, 1, stageW, bodyH)
		return
	}

	rightW := 0
	if m.showSidePane && remW-leftW >= 72 {
		rightW = min(m.sidePaneW, (remW-leftW)/2)
	}
	centerW := max(30, remW-leftW-rightW)

	hdrH := max(3, m.mainHeaderH)
	if hdrH+8 > bodyH {
		hdrH = 4
	}
	stageH := max(8, bodyH-hdrH)
	m.slotRects[huskipc.SlotMainHeader] = m.frameRect(huskipc.SlotMainHeader, railW+leftW, 1, centerW, hdrH)
	m.slotRects[huskipc.SlotMainStage] = m.frameRect(huskipc.SlotMainStage, railW+leftW, 1+hdrH, centerW, stageH)

	if rightW > 0 {
		m.slotRects[huskipc.SlotSidePanel] = m.frameRect(huskipc.SlotSidePanel, railW+leftW+centerW, 1, rightW, bodyH)
	}
}

// frameRect builds a SlotRect whose inner (content) area is inset by one cell
// per axis only when the surface's declared frame has a border.
func (m *WorkbenchModel) frameRect(slotID huskipc.SlotID, x, y, w, h int) SlotRect {
	r := SlotRect{
		X: x, Y: y, Width: w, Height: h,
		InnerX: x, InnerY: y, InnerW: max(1, w), InnerH: max(1, h),
	}
	if m.frameFor(slotID).Border {
		r.InnerX, r.InnerY = x+1, y+1
		r.InnerW, r.InnerH = max(1, w-2), max(1, h-2)
	}
	return r
}

// frameFor resolves the frame spec for a surface: the demo override first,
// then the occupying plugin's hello "frames" declaration, then (for rail
// surfaces) the rail item's "frame" field. Absent means a blank canvas.
func (m *WorkbenchModel) frameFor(slotID huskipc.SlotID) frame.Spec {
	if m.demoFrame {
		if s, ok := demoFrameSpecs[slotID]; ok {
			return s
		}
	}
	owner := ""
	switch slotID {
	case huskipc.SlotModalSurface:
		owner = m.activeModalPlugin
	case huskipc.SlotFullCanvas, huskipc.SlotWorkspaceStage:
		for _, ri := range m.railItems {
			if ri.ID == m.activeRailID {
				owner = ri.PluginID
				break
			}
		}
	}
	if owner == "" {
		owner = m.slotOwners[slotID]
	}
	if owner != "" {
		if cp, ok := m.plugins[owner]; ok {
			if s, ok := cp.Info.Frames[slotID]; ok {
				return s
			}
		}
	}
	if slotID == huskipc.SlotModalSurface || slotID == huskipc.SlotFullCanvas || slotID == huskipc.SlotWorkspaceStage {
		if ri, ok := m.activeRailItem(); ok && ri.Frame != nil {
			return *ri.Frame
		}
	}
	return frame.Spec{}
}

// activeRailItem returns the rail item currently driving full-canvas/stage
// placement, if any.
func (m *WorkbenchModel) activeRailItem() (huskipc.RailItemSpec, bool) {
	if m.activeRailID == "" {
		return huskipc.RailItemSpec{}, false
	}
	for _, ri := range m.railItems {
		if ri.ID == m.activeRailID {
			return ri, true
		}
	}
	return huskipc.RailItemSpec{}, false
}

func (m *WorkbenchModel) currentSlotSizes() map[huskipc.SlotID]huskipc.SlotSize {
	out := make(map[huskipc.SlotID]huskipc.SlotSize)
	for id, r := range m.slotRects {
		out[id] = huskipc.SlotSize{
			Width:   r.InnerW,
			Height:  r.InnerH,
			Focused: m.focusedSlot == id,
			Visible: true,
		}
	}
	return out
}

func (m *WorkbenchModel) broadcastSlotResize() {
	sizes := m.currentSlotSizes()
	for _, cp := range m.plugins {
		_ = cp.Writer.Send(huskipc.Envelope{
			Type: "slot_resize",
			SlotResize: &huskipc.SlotResizePayload{
				SlotSizes:   sizes,
				FocusedSlot: m.focusedSlot,
			},
		})
	}
}

func (m *WorkbenchModel) View() string {
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
	return baseView
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
	var lines []string
	if su != nil {
		lines = su.Lines
	} else {
		lines = []string{"Loading modal plugin surface..."}
	}
	rows := make([]string, innerH)
	for i := 0; i < innerH; i++ {
		if i < len(lines) {
			rows[i] = fitLineWidth(lines[i], innerW)
		} else {
			rows[i] = strings.Repeat(" ", innerW)
		}
	}

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

	railHint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Info)).
		Background(lipgloss.Color(m.palette.SurfaceElevated)).
		Padding(0, 1).
		Render("Rail: F1   F2  Modal  F3  Canvas  F4  Stage")

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

	left := brand + sub + pluginCountBadge + " " + railHint
	right := dragPill + themePill
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
		hint("F2/ctrl+f", " modal"),
		hint("F3/ctrl+g", " canvas"),
		hint("F4/ctrl+e", " stage"),
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
	lines := m.surfaceContentLines(su, rect.InnerW, rect.InnerH)
	if m.showHotspots && len(lines) > 0 {
		hot := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Warning)).
			Bold(true).
			Render(calloutNum + " " + slotName)
		lines[0] = overlayLabel(lines[0], hot)
	}
	return m.renderFrame(slotID, rect, spec, label, lines)
}

// surfaceContentLines pads/truncates a plugin's slot_update lines to the
// surface's inner area; absent content is a blank canvas.
func (m *WorkbenchModel) surfaceContentLines(su *huskipc.SlotUpdatePayload, w, h int) []string {
	lines := make([]string, h)
	for i := 0; i < h; i++ {
		if su != nil && i < len(su.Lines) {
			lines[i] = fitLineWidth(su.Lines[i], w)
		} else {
			lines[i] = strings.Repeat(" ", w)
		}
	}
	return lines
}

// overlayLabel replaces the start of a rendered line with label.
func overlayLabel(line, label string) string {
	rest := ansi.Cut(line, ansi.StringWidth(label), ansi.StringWidth(line))
	return label + rest
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
		if label != "" && len(lines) > 0 {
			styled := lipgloss.NewStyle().
				Foreground(lipgloss.Color(m.palette.Accent)).
				Bold(true).
				Render(label)
			lines[0] = overlayLabel(lines[0], styled)
		}
		if spec.Close && len(lines) > 0 {
			w := ansi.StringWidth(lines[0])
			body := ansi.Cut(lines[0], 0, max(0, w-1))
			lines[0] = body + m.closeStyle().Render("✕")
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
	if w := ansi.StringWidth(labelText) + ansi.StringWidth(closeText); w > innerW {
		labelText = ansi.Truncate(labelText, max(0, innerW-ansi.StringWidth(closeText)), "…")
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
	innerW := rect.InnerW

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true)
	subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkMuted))
	subtle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.InkSubtle))

	var lines []string
	lines = append(lines,
		titleStyle.Render("HUSK WORKBENCH HOTSPOT & SLOT ARCHITECTURE (bb plugin-api-docs counterpart)"),
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

		bbMap := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.AccentSecondary)).
			Render(fmt.Sprintf("bb:%-26s", slot.BBHotspot))

		ownerPill := lipgloss.NewStyle().
			Foreground(lipgloss.Color(m.palette.Success)).
			Render("owner: " + owner)

		row1 := fmt.Sprintf(" %s %s  %s  %s", numBadge, idText, bbMap, ownerPill)
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
	// The architecture explorer is Husk's own UI, so it draws its own frame.
	coreFrame := frame.Spec{Border: true, Label: "Slot Architecture Explorer"}
	iw := rect.InnerW
	if m.frameFor(huskipc.SlotMainStage).Border {
		iw = max(1, iw-2)
	}
	rows := m.surfaceContentLines(m.slotUpdates[huskipc.SlotMainStage], iw, max(1, rect.InnerH-2))
	return m.renderFrame(huskipc.SlotMainStage, rect, coreFrame, coreFrame.Label, rows)
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
		left := ansi.Cut(bgLine, 0, startX)
		right := ansi.Cut(bgLine, startX+ovW, ansi.StringWidth(bgLine))
		baseLines[rowIdx] = left + ovLine + right
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

// demoFrameSpecs is the frame declaration used by --demo-frame: a built-in
// demo plugin asking for border + label + close on every renderable surface.
var demoFrameSpecs = map[huskipc.SlotID]frame.Spec{
	huskipc.SlotMainStage:      {Border: true, Label: "Demo Frame", Close: true},
	huskipc.SlotModalSurface:   {Border: true, Label: "Demo Frame", Close: true},
	huskipc.SlotFullCanvas:     {Border: true, Label: "Demo Frame", Close: true},
	huskipc.SlotWorkspaceStage: {Border: true, Label: "Demo Frame", Close: true},
}

// setupDemoFrame registers the built-in demo plugin (no external process) and
// activates the requested rail placement, if any.
func (m *WorkbenchModel) setupDemoFrame(railMode string) {
	demoLines := []string{
		"This surface is a blank canvas; all chrome you see is a frame.",
		"",
		"The plugin declared: \"frame\": {\"border\": true, \"label\": \"Demo Frame\"}",
		"Husk draws the border, the label, and the ✕ close button.",
		"Clicking ✕ closes the surface exactly like esc.",
	}
	newDemoUpdate := func(slot huskipc.SlotID) *huskipc.SlotUpdatePayload {
		return &huskipc.SlotUpdatePayload{
			PluginID: "husk.demo-frame",
			Slot:     slot,
			Title:    "Demo Frame",
			Lines:    demoLines,
		}
	}

	m.plugins["husk.demo-frame"] = &ConnectedPlugin{
		Info: huskipc.HelloPayload{
			PluginID:    "husk.demo-frame",
			Name:        "Demo Frame",
			Version:     "0.1.0",
			Language:    "go",
			PID:         os.Getpid(),
			Description: "Built-in demo declaring a border+label+close frame",
			Slots:       []huskipc.SlotID{huskipc.SlotMainStage},
			Frames:      demoFrameSpecs,
		},
		Writer:    huskipc.NewConnWriter(io.Discard),
		Connected: time.Now(),
	}
	m.slotOwners[huskipc.SlotMainStage] = "husk.demo-frame"
	m.slotUpdates[huskipc.SlotMainStage] = newDemoUpdate(huskipc.SlotMainStage)

	switch railMode {
	case "modal":
		m.activeModalRailID = "rail.demo-frame"
		m.activeModalPlugin = "husk.demo-frame"
		m.slotUpdates[huskipc.SlotModalSurface] = newDemoUpdate(huskipc.SlotModalSurface)
	case "full-canvas":
		m.activeRailID = "rail.demo-frame"
		m.activePlacement = huskipc.PlacementFullCanvas
		m.focusedSlot = huskipc.SlotFullCanvas
		m.slotUpdates[huskipc.SlotFullCanvas] = newDemoUpdate(huskipc.SlotFullCanvas)
	case "stage":
		m.activeRailID = "rail.demo-frame"
		m.activePlacement = huskipc.PlacementStage
		m.focusedSlot = huskipc.SlotWorkspaceStage
		m.slotUpdates[huskipc.SlotWorkspaceStage] = newDemoUpdate(huskipc.SlotWorkspaceStage)
	}
	m.recomputeLayout()
}

func main() {
	lipgloss.SetColorProfile(termenv.TrueColor)

	dumpFrame := flag.Bool("dump-frame", false, "Render one live frame after plugin sync and exit")
	demoFrame := flag.Bool("demo-frame", false, "Render one frame from a built-in demo plugin that declares a border+label+close frame and exit")
	railMode := flag.String("rail", "", "Activate rail mode on startup: modal | full-canvas | stage")
	holdAlt := flag.Bool("hold-alt", false, "Simulate holding Alt/Option key on startup")
	pluginPaths := repeatable("plugin", "Plugin: executable, plugin directory, or husk-plugin.json path (repeatable)")
	pluginDirs := repeatable("plugin-dir", "Load every immediate subdirectory containing a husk-plugin.json (repeatable)")
	flag.Parse()

	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("husk-%d.sock", os.Getpid()))
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to listen on %s: %v\n", sockPath, err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	model := newWorkbenchModel(sockPath, listener, cwd)
	if *holdAlt {
		model.altLatched = true
		model.modifiers.Alt = true
	}
	model.startAcceptLoop()
	defer model.cleanup()

	if *demoFrame {
		model.demoFrame = true
		model.setupDemoFrame(*railMode)
		fmt.Println(model.View())
		return
	}

	var manifestPlugins []*manifest.Plugin
	loadPlugin := func(p *manifest.Plugin) {
		manifestPlugins = append(manifestPlugins, p)
	}
	for _, path := range *pluginPaths {
		p, err := manifest.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "husk: %v\n", err)
			os.Exit(1)
		}
		loadPlugin(p)
	}
	for _, dir := range *pluginDirs {
		ps, err := manifest.LoadDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "husk: --plugin-dir %s: %v\n", dir, err)
			os.Exit(1)
		}
		for _, p := range ps {
			loadPlugin(p)
		}
	}

	for _, p := range manifestPlugins {
		argv := p.ResolvedCommand()
		var err error
		if p.Transport == manifest.TransportStdio {
			err = model.spawnStdioCommand(p.WorkDir(), argv)
		} else {
			err = model.spawnSocketCommand(p.WorkDir(), argv)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "husk: failed to launch plugin %s: %v\n", p.ID, err)
		}
	}

	launched := len(manifestPlugins)
	if *dumpFrame {
		deadline := time.Now().Add(3000 * time.Millisecond)
		railTriggered := *railMode == ""
		for time.Now().Before(deadline) {
			select {
			case msg := <-model.subChan:
				model.Update(msg)
				if !railTriggered && launched > 0 && len(model.plugins) >= launched {
					railTriggered = true
					switch *railMode {
					case "modal":
						model.executeCommand("tv.open")
					case "full-canvas":
						model.executeCommand("fleet.open")
					case "stage":
						model.executeCommand("gitstage.open")
					}
				}
				ready := len(model.plugins) >= launched && len(model.slotUpdates) >= min(launched, 1)
				if *railMode == "modal" && model.slotUpdates[huskipc.SlotModalSurface] == nil {
					ready = false
				}
				if *railMode == "full-canvas" && model.slotUpdates[huskipc.SlotFullCanvas] == nil {
					ready = false
				}
				if *railMode == "stage" && model.slotUpdates[huskipc.SlotWorkspaceStage] == nil {
					ready = false
				}
				if ready {
					time.Sleep(150 * time.Millisecond)
					for len(model.subChan) > 0 {
						model.Update(<-model.subChan)
					}
					fmt.Println(model.View())
					return
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
		fmt.Println(model.View())
		return
	}

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "husk error: %v\n", err)
		os.Exit(1)
	}
}
