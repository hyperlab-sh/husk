package main

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

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
	pluginID   string
	env        huskipc.Envelope
	connection *ConnectedPlugin
}

type pluginRegisteredMsg struct {
	plugin *ConnectedPlugin
}

type toastExpireMsg struct {
	id int
}

type modifierTickMsg struct{}

type WorkbenchModel struct {
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
	registry    *publicationRegistry

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
	registry := newPublicationRegistry()
	return &WorkbenchModel{
		registry:     registry,
		socketPath:   socketPath,
		listener:     listener,
		subChan:      make(chan tea.Msg, 256),
		plugins:      make(map[string]*ConnectedPlugin),
		slotUpdates:  make(map[huskipc.SlotID]*huskipc.SlotUpdatePayload),
		slotOwners:   registry.Occupants,
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

	case pluginDisconnectedMsg:
		m.disconnectPublications(msg.plugin)
		return m, waitForIPC(m.subChan)

	case pluginRegisteredMsg:
		cp := msg.plugin
		if err := m.registerPublications(cp.Info); err != nil {
			m.rejectPublication(cp, "hello", err)
			if cp.Closer != nil {
				_ = cp.Closer.Close()
			}
			return m, waitForIPC(m.subChan)
		}
		m.plugins[cp.Info.PluginID] = cp
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
		for _, slot := range cp.Info.PublishedSlots {
			m.deliverPublications([]registryDelivery{m.registry.slotDelivery(slot.ID)})
		}
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
		cp := m.plugins[msg.pluginID]
		if cp == nil || (msg.connection != nil && msg.connection != cp) {
			return m, waitForIPC(m.subChan)
		}
		m.totalIPCEvents++
		cp.FramesRecv++
		if m.routePublication(msg.pluginID, msg.env) {
			return m, waitForIPC(m.subChan)
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
				if m.slotOwners[su.Slot] != msg.pluginID {
					m.rejectPublication(m.plugins[msg.pluginID], "slot_update", fmt.Errorf("surface is not held by this plugin"))
					break
				}
				update := *su
				update.PluginID = msg.pluginID
				m.slotUpdates[su.Slot] = &update
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

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		// v1 delivered bracketed paste as a key whose string was wrapped in
		// brackets, preventing pasted text from triggering host shortcuts.
		return m.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "[" + msg.Content + "]"})

	case tea.KeyReleaseMsg:
		key := legacyKeyString(msg)
		if _, held := m.heldKeys[key]; held {
			delete(m.heldKeys, key)
			m.broadcastKeyState("up", key, false)
		}
		return m, nil
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
