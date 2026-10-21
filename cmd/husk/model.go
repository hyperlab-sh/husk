package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

type ConnectedPlugin struct {
	Info       huskipc.HelloPayload
	Writer     *huskipc.ConnWriter
	Closer     io.Closer
	Connected  time.Time
	FramesRecv uint64
	// ExpectedID is the manifest id of the process this stream belongs to,
	// known only for stdio plugins; empty otherwise.
	ExpectedID string
	// PeerPID is the kernel-reported pid of a socket peer, or 0 when unknown.
	// checkManifest maps it, through its process ancestry, to the launched
	// process the connection came from (the peer itself or the wrapper that
	// started it).
	PeerPID int
}

// Rect returns the outer rectangle as a half-open image.Rectangle, so point
// and clip tests can use Point.In, Intersect and Overlaps.
func (r SlotRect) Rect() image.Rectangle {
	// A literal, not image.Rect: Rect swaps inverted corners, which would turn a
	// negative size into a non-empty rectangle.
	return image.Rectangle{Min: image.Pt(r.X, r.Y), Max: image.Pt(r.X+r.Width, r.Y+r.Height)}
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

// lineRejectedMsg reports an input line the read loop rejected (malformed or
// oversized). The protocol_error has already been sent; Update logs it.
// plugin is nil when the line arrived before a hello.
type lineRejectedMsg struct {
	plugin *ConnectedPlugin
	err    *huskipc.LineError
}

// connectionClosedMsg reports the end of a connection that never registered
// (it closed or failed before a hello), so its reason is still logged.
type connectionClosedMsg struct {
	reason error
}

// checkPluginMessage validates a plugin-to-host message that is not a
// published-slot, hook or event operation (those are checked by
// routePublication). Every other type, including host-to-plugin types sent
// the wrong way, is unknown.
func checkPluginMessage(env huskipc.Envelope) error {
	missing := false
	switch env.Type {
	case "close_modal":
	case "slot_update":
		missing = env.SlotUpdate == nil
	case "workspace_context":
		missing = env.WorkspaceContext == nil
	case "toast":
		missing = env.Toast == nil
	case "":
		return fmt.Errorf("message has no type")
	default:
		return fmt.Errorf("unknown message type %q", env.Type)
	}
	if missing {
		return fmt.Errorf("%s payload required", env.Type)
	}
	return nil
}

// hostVersion is the host build's version, sent as welcome.host_version. It is
// informational; compatibility is decided by huskipc.ProtocolVersion.
var hostVersion = "0.1.0"

type toastExpireMsg struct {
	id int
}

type modifierTickMsg struct{}

// hookSweepMsg fires while hook calls are pending to expire overdue ones.
type hookSweepMsg struct{}

const hookSweepInterval = time.Second

type WorkbenchModel struct {
	socketPath string
	listener   net.Listener
	subChan    chan tea.Msg
	procs      []*pluginProcess // launched plugin processes (see lifecycle.go)
	quit       chan struct{}    // closed when cleanup starts
	stopGrace  time.Duration    // SIGTERM-to-SIGKILL grace on shutdown

	// pluginCtx parents every plugin process; stopPlugins cancels it.
	pluginCtx    context.Context
	pluginCancel context.CancelFunc

	plugins     map[string]*ConnectedPlugin
	manifests   map[string]*manifest.Plugin // launched manifest plugins by id; hellos are checked against them
	railItems   []huskipc.RailItemSpec
	slotUpdates map[huskipc.SlotID]*huskipc.SlotUpdatePayload
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

	modifiers   huskipc.ModifierState
	altLatched  bool
	blurred     bool      // terminal reported focus loss
	modTickOn   bool      // a modifier tick is in flight
	hookSweepOn bool      // a hook sweep tick is in flight
	lastInput   time.Time // last key or mouse event, for tick cadence
	pollMods    func() (huskipc.ModifierState, bool)
	heldKeys    map[string]time.Time
	// keyReleases is true once the terminal confirmed it reports key release
	// events; held keys are then cleared by KeyReleaseMsg, not the timeout.
	keyReleases bool

	activeRailID    string // "" (Home), or rail item ID
	activePlacement huskipc.RailPlacement
	modal           modalState // the one source of truth for the open modal; use modalOwner/openModal/closeModal

	showHotspots   bool
	stageMode      string // "plugin" or "hotspots"
	hotspotCursor  int
	showPalette    bool
	paletteQuery   string
	paletteCursor  int
	activeToast    *huskipc.ToastPayload
	toastSeq       int
	totalIPCEvents uint64

	// Outbound policy for each plugin connection (see handleStream).
	sendQueueCapacity int
	writeTimeout      time.Duration
	logOut            io.Writer // host log lines, such as disconnect reasons
}

const (
	pluginSendQueueCapacity = 1024
	pluginWriteTimeout      = 5 * time.Second
)

// disconnectReason renders a pluginDisconnectedMsg reason for logs and toasts.
func disconnectReason(err error) string {
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return "connection closed (EOF)"
	case errors.Is(err, huskipc.ErrWriteTimeout), errors.Is(err, huskipc.ErrSendQueueFull):
		return "stopped reading: " + strings.TrimPrefix(err.Error(), "huskipc: ")
	case errors.Is(err, huskipc.ErrWriterClosed):
		return "closed by host"
	case strings.HasPrefix(err.Error(), "huskipc: write: "):
		return "write error: " + strings.TrimPrefix(err.Error(), "huskipc: write: ")
	}
	return "read error: " + err.Error()
}

func (m *WorkbenchModel) logf(format string, args ...any) {
	if m.logOut != nil {
		fmt.Fprintf(m.logOut, "husk: "+format+"\n", args...)
	}
}

const activityRailWidth = 5

func newWorkbenchModel(socketPath string, listener net.Listener, cwd string) *WorkbenchModel {
	registry := newPublicationRegistry()
	pluginCtx, pluginCancel := context.WithCancel(context.Background())
	return &WorkbenchModel{
		pluginCtx:    pluginCtx,
		pluginCancel: pluginCancel,
		registry:     registry,
		socketPath:   socketPath,
		listener:     listener,
		subChan:      make(chan tea.Msg, 256),
		quit:         make(chan struct{}),
		stopGrace:    pluginStopGrace,
		plugins:      make(map[string]*ConnectedPlugin),
		manifests:    make(map[string]*manifest.Plugin),
		slotUpdates:  make(map[huskipc.SlotID]*huskipc.SlotUpdatePayload),
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
		pollMods:     pollOSModifiers,
		showHotspots: false,
		stageMode:    "plugin",

		sendQueueCapacity: pluginSendQueueCapacity,
		writeTimeout:      pluginWriteTimeout,
		logOut:            os.Stderr,
	}
}

func waitForIPC(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

// Modifier polling cadence. The OS modifier poll is the only way to see a bare
// Alt/Option press, so it cannot stop entirely while the terminal is focused;
// it runs fast only while a modifier, held key or recent input makes the
// result matter, and otherwise backs off. It stops completely on blur.
const (
	modifierTickActive = 40 * time.Millisecond
	modifierTickIdle   = 150 * time.Millisecond
	modifierActiveFor  = 2 * time.Second
)

// startHookSweep schedules the next expiry check only while calls are pending,
// so an idle host runs no extra ticks.
func (m *WorkbenchModel) startHookSweep() tea.Cmd {
	if m.hookSweepOn || len(m.registry.Pending) == 0 {
		return nil
	}
	m.hookSweepOn = true
	return tea.Tick(hookSweepInterval, func(time.Time) tea.Msg { return hookSweepMsg{} })
}

func waitForModifierTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return modifierTickMsg{}
	})
}

// modifierTickInterval picks the next poll delay from current activity.
const (
	// heldKeyFallbackTTL expires a held key when the terminal reports no
	// release events.
	heldKeyFallbackTTL = 320 * time.Millisecond
	// heldKeyReleaseTTL is a safety net when releases are reported: repeats
	// refresh a held key, so only a release that never arrives (or does not
	// match its press, e.g. Enter/Tab without all-keys-as-escape-codes) lapses.
	heldKeyReleaseTTL = time.Second
)

func (m *WorkbenchModel) modifierTickInterval(now time.Time) time.Duration {
	mods := m.modifiers
	if mods.Alt || mods.Ctrl || mods.Shift || mods.Super || len(m.heldKeys) > 0 ||
		now.Sub(m.lastInput) < modifierActiveFor {
		return modifierTickActive
	}
	return modifierTickIdle
}

// startModifierTick starts the poll chain unless one is already running or
// the terminal is unfocused.
func (m *WorkbenchModel) startModifierTick() tea.Cmd {
	if m.modTickOn || m.blurred {
		return nil
	}
	m.modTickOn = true
	return waitForModifierTick(m.modifierTickInterval(time.Now()))
}

// handleBlur drops OS-derived modifier and held-key state (other apps' Alt
// presses must not leak in) and lets the tick chain lapse. A latched Alt
// (the "?" toggle or --hold-alt) is a host setting and survives.
func (m *WorkbenchModel) handleBlur() {
	m.blurred = true
	m.syncModifierTransitions(huskipc.ModifierState{Alt: m.altLatched})
	for k := range m.heldKeys {
		delete(m.heldKeys, k)
		m.broadcastKeyState(huskipc.KeyPhaseUp, k, false)
	}
}

func (m *WorkbenchModel) Init() tea.Cmd {
	m.recomputeLayout()
	return tea.Batch(waitForIPC(m.subChan), m.startModifierTick())
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

func (m *WorkbenchModel) broadcastKeyState(phase huskipc.KeyPhase, key string, held bool) {
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
		phase := huskipc.KeyPhaseUp
		if next.Alt {
			phase = huskipc.KeyPhaseDown
		}
		m.broadcastKeyState(phase, "alt", next.Alt)
	}
	if prev.Ctrl != next.Ctrl {
		phase := huskipc.KeyPhaseUp
		if next.Ctrl {
			phase = huskipc.KeyPhaseDown
		}
		m.broadcastKeyState(phase, "ctrl", next.Ctrl)
	}
	if prev.Shift != next.Shift {
		phase := huskipc.KeyPhaseUp
		if next.Shift {
			phase = huskipc.KeyPhaseDown
		}
		m.broadcastKeyState(phase, "shift", next.Shift)
	}
	if prev.Super != next.Super {
		phase := huskipc.KeyPhaseUp
		if next.Super {
			phase = huskipc.KeyPhaseDown
		}
		m.broadcastKeyState(phase, "super", next.Super)
	}
}

func (m *WorkbenchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(0, msg.Width)
		m.height = max(0, msg.Height)
		if m.tooSmall() {
			// Keep the last valid layout (and plugin slot sizes); View
			// renders the too-small screen until the terminal grows back.
			return m, nil
		}
		m.recomputeLayout()
		m.broadcastSlotResize()
		return m, nil

	case pluginExitedMsg, pluginLaunchFailedMsg, hostNoticeMsg:
		toast := m.handleLifecycleMsg(msg)
		return m, tea.Batch(waitForIPC(m.subChan), toast)

	case pluginDisconnectedMsg:
		if m.plugins[msg.plugin.Info.PluginID] != msg.plugin {
			// Rejected at hello, or a stale connection already replaced:
			// nothing to tear down, but the reason is still logged.
			m.logf("connection from plugin %q (not registered) closed: %s", msg.plugin.Info.PluginID, disconnectReason(msg.reason))
			return m, waitForIPC(m.subChan)
		}
		reason := disconnectReason(msg.reason)
		m.disconnectPublications(msg.plugin)
		m.logf("plugin %s disconnected: %s", msg.plugin.Info.PluginID, reason)
		return m, tea.Batch(
			waitForIPC(m.subChan),
			m.showLocalToast("warn", "Plugin disconnected", msg.plugin.Info.PluginID+": "+reason),
		)

	case connectionClosedMsg:
		m.logf("connection closed before hello: %s", disconnectReason(msg.reason))
		return m, waitForIPC(m.subChan)

	case lineRejectedMsg:
		who := "unregistered connection"
		if msg.plugin != nil {
			who = "plugin " + msg.plugin.Info.PluginID
		}
		m.logf("protocol error from %s: %s", who, msg.err.Payload().Message)
		return m, waitForIPC(m.subChan)

	case pluginRegisteredMsg:
		cp := msg.plugin
		err := huskipc.CheckProtocolVersion(cp.Info.ProtocolVersion)
		if err == nil {
			err = m.checkManifest(cp)
		}
		if err == nil {
			err = m.registerPublications(cp.Info)
		}
		if err != nil {
			m.rejectPublication(cp, "hello", err)
			m.logf("rejected hello from plugin %q: %v", cp.Info.PluginID, err)
			// Flush the protocol_error before closing.
			if cp.Writer.Queued() {
				_ = cp.Writer.Close()
			} else if cp.Closer != nil {
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
				ProtocolVersion: huskipc.ProtocolVersion,
				HostVersion:     hostVersion,
				Palette:         huskipc.Palette(m.palette),
				SlotSizes:       m.currentSlotSizes(),
				FocusedSlot:     m.focusedSlot,
				Context:         m.wsContext,
				Modifiers:       m.modifiers,
			},
		})
		// Every slot the plugin owns, not just those in its hello: manifest
		// declarations may have gathered contributions before it connected.
		m.deliverPublications(m.registry.ownedSlotDeliveries(cp.Info.PluginID))
		return m, waitForIPC(m.subChan)

	case tea.BlurMsg:
		m.handleBlur()
		return m, nil

	case tea.FocusMsg:
		m.blurred = false
		return m, m.startModifierTick()

	case hookSweepMsg:
		m.hookSweepOn = false
		m.deliverPublications(m.registry.expireHooks(m.registry.Clock.Now()))
		return m, m.startHookSweep()

	case modifierTickMsg:
		if m.blurred {
			m.modTickOn = false
			return m, nil
		}
		osMods, polled := m.pollMods()
		if polled {
			if m.altLatched {
				osMods.Alt = true
			}
			m.syncModifierTransitions(osMods)
		}
		now := time.Now()
		ttl := heldKeyFallbackTTL
		if m.keyReleases {
			ttl = heldKeyReleaseTTL
		}
		for k, ts := range m.heldKeys {
			if now.Sub(ts) > ttl {
				delete(m.heldKeys, k)
				m.broadcastKeyState(huskipc.KeyPhaseUp, k, false)
			}
		}
		if !polled && len(m.heldKeys) == 0 {
			// No OS modifier source and nothing to expire: let the chain lapse
			// so an idle process does no work. A key press restarts it.
			m.modTickOn = false
			return m, nil
		}
		return m, waitForModifierTick(m.modifierTickInterval(now))

	case ipcEventMsg:
		cp := m.plugins[msg.pluginID]
		if cp == nil || (msg.connection != nil && msg.connection != cp) {
			return m, waitForIPC(m.subChan)
		}
		m.totalIPCEvents++
		cp.FramesRecv++
		if m.routePublication(msg.pluginID, msg.env) {
			return m, tea.Batch(waitForIPC(m.subChan), m.startHookSweep())
		}
		if msg.env.Type == "protocol_error" {
			// The plugin rejected a host message. Log it and never answer
			// with another protocol_error: two strict peers would loop.
			if pe := msg.env.ProtocolError; pe != nil {
				m.logf("plugin %s rejected a %q message: %s", msg.pluginID, pe.MessageType, pe.Message)
			} else {
				m.logf("plugin %s sent a protocol_error without a payload", msg.pluginID)
			}
			return m, waitForIPC(m.subChan)
		}
		if err := checkPluginMessage(msg.env); err != nil {
			m.rejectPublication(cp, msg.env.Type, err)
			m.logf("protocol error from plugin %s: %v", msg.pluginID, err)
			return m, waitForIPC(m.subChan)
		}
		if msg.env.CloseModal {
			m.closeModal()
			if m.activePlacement == huskipc.PlacementFullCanvas {
				m.activateHomeWorkbench()
			}
		}
		switch msg.env.Type {
		case "slot_update":
			if su := msg.env.SlotUpdate; su != nil {
				if m.surfaceOwner(su.Slot) != msg.pluginID {
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
		m.lastInput = time.Now()
		return m.handleMouse(msg)

	case tea.KeyPressMsg:
		m.lastInput = time.Now()
		next, cmd := m.handleKey(msg)
		if len(m.heldKeys) > 0 {
			// A lapsed chain (no OS modifier source) restarts to expire the held key.
			cmd = tea.Batch(cmd, m.startModifierTick())
		}
		return next, cmd

	case tea.PasteMsg:
		// v1 delivered bracketed paste as a key whose string was wrapped in
		// brackets, preventing pasted text from triggering host shortcuts.
		return m.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "[" + msg.Content + "]"})

	case tea.KeyboardEnhancementsMsg:
		// The renderer re-queries on every keyboard change, so this can fall
		// back to false mid-session; the shorter expiry then applies again from
		// the next tick, restarted by the next key press.
		m.keyReleases = msg.SupportsEventTypes()
		return m, nil

	case tea.KeyReleaseMsg:
		key := legacyKeyString(msg)
		if _, held := m.heldKeys[key]; held {
			delete(m.heldKeys, key)
			m.broadcastKeyState(huskipc.KeyPhaseUp, key, false)
		}
		return m, nil
	}

	return m, nil
}

func (m *WorkbenchModel) activateHomeWorkbench() {
	m.activeRailID = ""
	m.activePlacement = ""
	m.closeModal()
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
		if m.modalOwner().RailID == ri.ID {
			m.closeModal()
			return
		}
		m.openModal(ri.PluginID, ri.ID)
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
		m.closeModal()
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
		m.closeModal()
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

// modalState identifies the open modal: the plugin that owns it and the rail
// item that opened it. The zero value means no modal is open.
type modalState struct {
	PluginID string
	RailID   string
}

// Open reports whether a modal is showing.
func (s modalState) Open() bool { return s.PluginID != "" || s.RailID != "" }

// modalOwner is the single accessor for the open modal's owner. Everything that
// needs to know whether a modal is up, who owns it or which rail item opened it
// reads it here; nothing else keeps a copy.
func (m *WorkbenchModel) modalOwner() modalState { return m.modal }

// openModal makes pluginID's rail item railID the open modal.
func (m *WorkbenchModel) openModal(pluginID, railID string) {
	m.modal = modalState{PluginID: pluginID, RailID: railID}
}

// closeModal dismisses the open modal, if any.
func (m *WorkbenchModel) closeModal() { m.modal = modalState{} }

// surfaceOwner is the plugin that holds slot's claim: the registry occupant
// ("" when the host draws it). Write authorisation, published-content cleanup
// and rendering all resolve through the claim, including for the modal surface;
// modalOwner only says which modal is open and which rail item opened it.
func (m *WorkbenchModel) surfaceOwner(slot huskipc.SlotID) string {
	return m.registry.Occupants[slot]
}
