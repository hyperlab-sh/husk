package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

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

// hostRunEndedMsg reports that host.Run returned. A nil err is a clean stop
// (the context ended or Close ran).
type hostRunEndedMsg struct{ err error }

type toastExpireMsg struct {
	id int
}

// toastPollMsg re-reads the engine's toast queue: the engine expires toasts on
// its own clock and posts nothing when one lapses.
type toastPollMsg struct{}

const toastPollInterval = 500 * time.Millisecond

type modifierTickMsg struct{}

// surfaceCatalog is what the model reads of the host's resolution: who
// occupies each surface, the frames plugins declared, and the published slots
// with their contributions. refresh rebuilds it from State and the Frontend;
// tests set it directly.
type surfaceCatalog struct {
	Occupants map[protocol.SurfaceID]string
	Slots     map[string]*publishedSlot
	Frames    map[string]map[protocol.SurfaceID]protocol.FrameSpec
}

func newSurfaceCatalog() *surfaceCatalog {
	return &surfaceCatalog{
		Occupants: map[protocol.SurfaceID]string{},
		Slots:     map[string]*publishedSlot{},
		Frames:    map[string]map[protocol.SurfaceID]protocol.FrameSpec{},
	}
}

// publishedSlot is one extension point a plugin declared: inline (anchors in
// the owner's rows) or region (a rectangle of a claimed surface).
type publishedSlot struct {
	ID            string
	Owner         string
	Kind          string
	Rect          *contracts.PublishedRect
	Contributions []contribution
}

// contribution is one entry another plugin put in a published slot. Content
// is the rendered text; OnClick is the contributor's action id.
type contribution struct {
	PluginID string
	ID       string
	Content  string
	OnClick  string
}

type WorkbenchModel struct {
	// host and fe are nil for the built-in demos and the hermetic tests: the
	// model then draws from the caches below, which tests fill directly.
	host      host.Looped
	fe        contracts.Frontend
	keys      keySource
	closeGate func()          // closes the post gate before Host.Close (D62)
	runCtx    context.Context // ends host.Run
	stopRun   context.CancelFunc
	closed    bool  // Host.Close ran; no further host calls
	hostErr   error // why host.Run ended, when it ended on its own

	slices      contracts.FrontendSlices
	slicesOwned bool
	lastSeq     uint64
	lastGeo     []byte
	lastFocus   []byte
	lastLayout  []byte
	lastView    []byte
	declared    []contracts.RailItem // every declared rail item, live plugin or not
	registered  map[string]bool      // rail action ids registered with the host

	actions map[string]contracts.HostActionHandler
	cmds    []tea.Cmd // commands host-action handlers queued during this Update

	plugins     map[string]bool                // live plugins
	podStatus   map[string]contracts.PodStatus // every Pod's status, for --dump-frame
	railItems   []contracts.RailItem
	slotUpdates map[protocol.SurfaceID]*protocol.SurfaceUpdateParams
	slotRects   map[protocol.SurfaceID]SlotRect
	modalRect   SlotRect
	demoFrame   bool
	catalog     *surfaceCatalog

	width        int
	height       int
	palette      theme.Palette
	focusedSlot  protocol.SurfaceID
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

	// focusFallback is the side slot focus was moved off when its column
	// vanished; it comes back when the column does.
	focusFallback protocol.SurfaceID

	// pendingKeys is the prefix of a multi-key binding typed so far.
	pendingKeys []string

	modifiers  protocol.ModifierState
	altLatched bool
	blurred    bool      // terminal reported focus loss
	modTickOn  bool      // a modifier tick is in flight
	lastInput  time.Time // last key or mouse event, for tick cadence
	pollMods   func() (protocol.ModifierState, bool)
	heldKeys   map[string]time.Time
	// heldAliases maps a held key's key-code name to the text name it was
	// held under, for presses whose text came from the layout (AltGr+Q held
	// as "@", a dead key as "é"). Releases carry no associated text, so they
	// arrive under the code name ("q", "e") and are looked up here.
	heldAliases map[string]string
	// keyReleases is true once the terminal confirmed it reports key release
	// events; held keys are then cleared by KeyReleaseMsg, not the timeout.
	keyReleases bool
	// termMods holds one bit per bare modifier key (offset from
	// tea.KeyLeftShift) the terminal reported down and not yet released.
	termMods uint16

	activeRailID    string // "" (Home), or rail item ID
	activePlacement contracts.RailPlacement
	modal           modalState // the one source of truth for the open modal; use modalOwner/openModal/closeModal

	showHotspots   bool
	stageMode      string // "plugin" or "hotspots"
	hotspotCursor  int
	showPalette    bool
	paletteQuery   string
	paletteCursor  int
	activeToast    *contracts.Toast
	engineToast    bool // activeToast came from the engine queue
	toastPollOn    bool
	toastSeq       int
	refusalsShown  int      // host refusals already reported
	failures       []string // plugin failures noted this settle, toasted together
	totalIPCEvents uint64   // surface updates the host routed here

	logOut io.Writer // host log lines, such as launch failures
}

const activityRailWidth = 5

func newWorkbenchModel() *WorkbenchModel {
	m := &WorkbenchModel{
		catalog:      newSurfaceCatalog(),
		keys:         defaultKeys{},
		registered:   map[string]bool{},
		plugins:      make(map[string]bool),
		podStatus:    make(map[string]contracts.PodStatus),
		slotUpdates:  make(map[protocol.SurfaceID]*protocol.SurfaceUpdateParams),
		slotRects:    make(map[protocol.SurfaceID]SlotRect),
		width:        144,
		height:       40,
		palette:      theme.Default(),
		focusedSlot:  protocol.SurfaceSidebarList,
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
		stageMode:    "plugin",
		logOut:       os.Stderr,
	}
	m.actions = m.hostActionTable()
	return m
}

func (m *WorkbenchModel) logf(format string, args ...any) {
	if m.logOut != nil {
		fmt.Fprintf(m.logOut, "husk: "+format+"\n", args...)
	}
}

// attach connects the model to a running-to-be host. It is called once, before
// the program starts.
func (m *WorkbenchModel) attach(h host.Looped, closeGate func()) {
	m.host, m.fe, m.keys = h, h.Frontend(), h.Frontend()
	m.closeGate = closeGate
	m.runCtx, m.stopRun = context.WithCancel(context.Background())
}

// content is the host's content sink: every accepted surface.update arrives
// here on the loop. The engine already checked occupancy, so the plugin id is
// the authority and the update's own plugin_id is overwritten.
func (m *WorkbenchModel) content(plugin string, p protocol.SurfaceUpdateParams) {
	m.totalIPCEvents++
	update := p
	update.PluginID = plugin
	update.Lines = slices.Clone(p.Lines)
	m.slotUpdates[p.Surface] = &update
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
	m.termMods = 0
	m.syncModifierTransitions(protocol.ModifierState{Alt: m.altLatched})
	for k := range m.heldKeys {
		delete(m.heldKeys, k)
		m.broadcastKeyState(protocol.KeyPhaseUp, k, false)
	}
	clear(m.heldAliases)
}

// Init attaches the model's State slices, registers the host actions and
// starts the host. The program goroutine is the loop, so the loop-only
// Frontend calls are legal here.
func (m *WorkbenchModel) Init() tea.Cmd {
	m.attachSlices()
	m.registerActions()
	m.refresh()
	m.recomputeLayout()
	m.settle()
	cmds := []tea.Cmd{m.startModifierTick(), tea.Batch(m.cmds...)}
	m.cmds = nil
	if m.host != nil {
		cmds = append(cmds, m.runHost)
	}
	return tea.Batch(cmds...)
}

// runHost runs host.Run until the context ends. It is a command, so it runs
// on its own goroutine, and its result comes back as a message.
func (m *WorkbenchModel) runHost() tea.Msg {
	return hostRunEndedMsg{err: m.host.Run(m.runCtx)}
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

// broadcastKeyState tells every live plugin about a key or modifier
// transition. Delivery failures (a plugin that just exited) are not errors.
func (m *WorkbenchModel) broadcastKeyState(phase protocol.KeyPhase, key string, held bool) {
	if m.fe == nil {
		return
	}
	params := protocol.KeyStateParams{
		Phase:     phase,
		Key:       key,
		Held:      held,
		Modifiers: m.modifiers,
		HeldKeys:  m.currentHeldKeys(),
		Surface:   m.focusedSlot,
	}
	for _, pid := range slices.Sorted(maps.Keys(m.plugins)) {
		_ = m.fe.SendKeyState(pid, params)
	}
}

func (m *WorkbenchModel) syncModifierTransitions(next protocol.ModifierState) {
	prev := m.modifiers
	m.modifiers = next

	phase := func(down bool) protocol.KeyPhase {
		if down {
			return protocol.KeyPhaseDown
		}
		return protocol.KeyPhaseUp
	}
	if prev.Alt != next.Alt {
		m.broadcastKeyState(phase(next.Alt), "alt", next.Alt)
	}
	if prev.Ctrl != next.Ctrl {
		m.broadcastKeyState(phase(next.Ctrl), "ctrl", next.Ctrl)
	}
	if prev.Shift != next.Shift {
		m.broadcastKeyState(phase(next.Shift), "shift", next.Shift)
	}
	if prev.Super != next.Super {
		m.broadcastKeyState(phase(next.Super), "super", next.Super)
	}
}

// Update runs one message, then settles: derived state is re-read from the
// host, frontend-owned State is written, and the host ticks so those writes
// flush in this same Update.
func (m *WorkbenchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.cmds = nil
	next, cmd := m.update(msg)
	if !m.closed {
		m.settle()
	}
	cmds := append([]tea.Cmd{cmd}, m.cmds...)
	m.cmds = nil
	return next, tea.Batch(cmds...)
}

func (m *WorkbenchModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(0, msg.Width)
		m.height = max(0, msg.Height)
		if m.tooSmall() {
			// Keep the last valid layout (and plugin surface sizes); View
			// renders the too-small screen until the terminal grows back.
			return m, nil
		}
		m.recomputeLayout()
		return m, nil

	case hostRunEndedMsg:
		if msg.err != nil && !m.closed {
			// A host that cannot listen or register leaves nothing to
			// show: stop, and let main print the cause once the terminal is
			// restored.
			m.hostErr = msg.err
			return m, m.quitCmd()
		}
		return m, nil

	case tea.BlurMsg:
		m.handleBlur()
		return m, nil

	case tea.FocusMsg:
		m.blurred = false
		return m, m.startModifierTick()

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
				m.dropHeldAliases(k)
				m.broadcastKeyState(protocol.KeyPhaseUp, k, false)
			}
		}
		if !polled && len(m.heldKeys) == 0 {
			// No OS modifier source and nothing to expire: let the chain lapse
			// so an idle process does no work. A key press restarts it.
			m.modTickOn = false
			return m, nil
		}
		return m, waitForModifierTick(m.modifierTickInterval(now))

	case toastExpireMsg:
		if msg.id == m.toastSeq && !m.engineToast {
			m.activeToast = nil
		}
		return m, nil

	case toastPollMsg:
		m.toastPollOn = false
		return m, nil // settle re-reads the queue and re-arms the poll

	case tea.MouseMsg:
		m.lastInput = time.Now()
		return m.handleMouse(msg)

	case tea.KeyPressMsg:
		m.lastInput = time.Now()
		// Bare modifiers are tracked (and key_state broadcast) even while the
		// too-small frame freezes the layout, as the darwin poll does, so the
		// state is right when the layout returns. Only handleKey is gated.
		if m.trackModifierKey(msg.Key(), true) {
			return m, nil
		}
		next, cmd := m.handleKey(normalizeTextEvent(msg))
		if len(m.heldKeys) > 0 {
			// A lapsed chain (no OS modifier source) restarts to expire the held key.
			cmd = tea.Batch(cmd, m.startModifierTick())
		}
		return next, cmd

	case tea.PasteMsg:
		if m.showPalette && !m.layoutFrozen() {
			return m.pastePalette(msg.Content)
		}
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
		if m.trackModifierKey(msg.Key(), false) {
			return m, nil
		}
		key := m.releasedHeldKey(legacyKeyString(msg))
		if _, held := m.heldKeys[key]; held {
			delete(m.heldKeys, key)
			m.broadcastKeyState(protocol.KeyPhaseUp, key, false)
		}
		return m, nil
	}

	// Anything else is the host's: its worker results, Calls replies and
	// sweeps, Pod notices.
	if m.host != nil && !m.closed {
		m.host.Update(msg)
	}
	return m, nil
}

// quitCmd stops the host and the program. The post gate closes first so the
// workers Close waits for never block on a loop that no longer reads (D62).
func (m *WorkbenchModel) quitCmd() tea.Cmd {
	m.shutdownHost()
	return tea.Quit
}

// shutdownHost closes the post gate, then the host; both are idempotent.
func (m *WorkbenchModel) shutdownHost() {
	if m.closeGate != nil {
		m.closeGate()
	}
	if m.stopRun != nil {
		m.stopRun()
	}
	if m.host != nil && !m.closed {
		m.closed = true
		if err := m.host.Close(); err != nil {
			m.logf("closing the host: %v", err)
		}
	}
}

// attachSlices registers the frontend-owned State slices and takes the
// persisted dock preferences from husk.layout.
func (m *WorkbenchModel) attachSlices() {
	if m.fe == nil || m.slicesOwned {
		return
	}
	s, err := m.fe.OwnSlices()
	if err != nil {
		m.logf("owning State slices: %v", err)
		return
	}
	m.slices, m.slicesOwned = s, true
	if snap := m.fe.Snapshot(); snap != nil {
		if e, ok := snap.Slices[state.LayoutNamespace]; ok && len(e.Value) > 0 {
			m.applyLayout(e.Value)
		}
	}
	m.lastLayout = m.layoutJSON()
}

// layoutDoc is the value of husk.layout: the user's dock preferences.
type layoutDoc struct {
	Sidebar struct {
		Shown        bool `json:"shown"`
		Width        int  `json:"width"`
		NavHeight    int  `json:"nav_height"`
		FooterHeight int  `json:"footer_height"`
	} `json:"sidebar"`
	SidePanel struct {
		Shown bool `json:"shown"`
		Width int  `json:"width"`
	} `json:"side_panel"`
	MainHeaderHeight int `json:"main_header_height"`
}

func (m *WorkbenchModel) layoutJSON() []byte {
	var d layoutDoc
	d.Sidebar.Shown, d.Sidebar.Width = m.showSidebar, m.sidebarWidth
	d.Sidebar.NavHeight, d.Sidebar.FooterHeight = m.sidebarNavH, m.sidebarFootH
	d.SidePanel.Shown, d.SidePanel.Width = m.showSidePane, m.sidePaneW
	d.MainHeaderHeight = m.mainHeaderH
	b, _ := json.Marshal(d)
	return b
}

// applyLayout adopts a persisted husk.layout value. A missing or out-of-range
// field keeps the current default.
func (m *WorkbenchModel) applyLayout(raw json.RawMessage) {
	var d layoutDoc
	d.Sidebar.Shown, d.Sidebar.Width = m.showSidebar, m.sidebarWidth
	d.Sidebar.NavHeight, d.Sidebar.FooterHeight = m.sidebarNavH, m.sidebarFootH
	d.SidePanel.Shown, d.SidePanel.Width = m.showSidePane, m.sidePaneW
	d.MainHeaderHeight = m.mainHeaderH
	if json.Unmarshal(raw, &d) != nil {
		return
	}
	m.showSidebar, m.showSidePane = d.Sidebar.Shown, d.SidePanel.Shown
	if d.Sidebar.Width >= 24 {
		m.sidebarWidth = d.Sidebar.Width
	}
	if d.Sidebar.NavHeight >= 3 {
		m.sidebarNavH = d.Sidebar.NavHeight
	}
	if d.Sidebar.FooterHeight >= 3 {
		m.sidebarFootH = d.Sidebar.FooterHeight
	}
	if d.SidePanel.Width >= 24 {
		m.sidePaneW = d.SidePanel.Width
	}
	if d.MainHeaderHeight >= 3 {
		m.mainHeaderH = d.MainHeaderHeight
	}
}

// podsDoc is the part of husk.pods the model reads.
type podsDoc struct {
	Plugins map[string]struct {
		Status    contracts.PodStatus `json:"status"`
		LastError string              `json:"last_error"`
	} `json:"plugins"`
}

// notePodTransition reports a Pod's move to a terminal status: a failure or
// crash raises a warning toast with the recorded cause, a clean exit only
// logs (pods.md, failure table). Only a transition counts, so a Pod that
// stays failed across refreshes toasts once.
func (m *WorkbenchModel) notePodTransition(id string, from, to contracts.PodStatus, lastError string) {
	if from == to {
		return
	}
	switch to {
	case contracts.PodFailed:
		msg := id + ": " + lastError
		if lastError == "" {
			msg = id + ": failed"
		}
		m.logf("plugin %s failed: %s", id, lastError)
		m.failures = append(m.failures, msg)
	case contracts.PodExited:
		m.logf("plugin %s exited", id)
	}
}

// noteRefusals queues each plugin the host refused before a Pod existed
// (invalid manifest, duplicate id, protocol mismatch) the way a launch
// failure is: no husk.pods entry exists to carry the failure, so the host
// lists them and the model reports each one once. It then shows everything
// queued this settle, refusals and failed Pods together.
func (m *WorkbenchModel) noteRefusals() {
	if m.fe == nil {
		return
	}
	refused := m.fe.Refusals()
	for _, r := range refused[min(m.refusalsShown, len(refused)):] {
		msg := r.Reason
		if r.Plugin != "" {
			msg = r.Plugin + ": " + r.Reason
		}
		m.logf("plugin refused: %s", msg)
		m.failures = append(m.failures, msg)
	}
	m.refusalsShown = len(refused)
	m.toastFailures()
}

// toastFailures shows the failures noted since the last call as one toast, so
// several arriving in one settle (refusals all land at boot) cannot replace
// each other; the log holds each one in full.
func (m *WorkbenchModel) toastFailures() {
	switch len(m.failures) {
	case 0:
		return
	case 1:
		m.cmds = append(m.cmds, m.showLocalToast("warn", "Plugin failed", m.failures[0]))
	default:
		m.cmds = append(m.cmds, m.showLocalToast("warn", fmt.Sprintf("%d plugins failed", len(m.failures)), strings.Join(m.failures, "; ")))
	}
	m.failures = nil
}

// derived is everything refresh rebuilds from the host; a change in any of it
// re-lays the workbench out.
type derived struct {
	Plugins   map[string]bool
	RailItems []contracts.RailItem
	Catalog   *surfaceCatalog
}

// refresh re-reads Pods, occupancy, rail items, published slots and frames
// from State and the Frontend when State moved. It reports whether anything
// the layout or the draw depends on changed, and has already re-laid out then.
func (m *WorkbenchModel) refresh() bool {
	if m.fe == nil {
		return false
	}
	changed := false
	if pal := theme.Palette(m.fe.Palette()); pal != m.palette {
		m.palette = pal
		changed = true
	}
	snap := m.fe.Snapshot()
	if snap == nil || snap.Seq == m.lastSeq && m.lastSeq != 0 {
		return changed
	}
	m.lastSeq = snap.Seq

	var pods podsDoc
	if e, ok := snap.Slices[state.PodsNamespace]; ok {
		_ = json.Unmarshal(e.Value, &pods)
	}
	live := map[string]bool{}
	status := map[string]contracts.PodStatus{}
	for id, p := range pods.Plugins {
		status[id] = p.Status
		m.notePodTransition(id, m.podStatus[id], p.Status, p.LastError)
		if p.Status == contracts.PodLive {
			live[id] = true
		}
	}
	var res surfaces.Surfaces
	if e, ok := snap.Slices[state.SurfacesNamespace]; ok {
		_ = json.Unmarshal(e.Value, &res)
	}

	cat := newSurfaceCatalog()
	for id, occ := range res.Occupants {
		cat.Occupants[id] = occ.Plugin
	}
	for plugin, frames := range m.fe.SurfaceFrames() {
		if live[plugin] {
			cat.Frames[plugin] = frames
		}
	}
	for _, ps := range m.fe.PublishedSlots() {
		if live[ps.Plugin] {
			cat.Slots[ps.ID] = &publishedSlot{ID: ps.ID, Owner: ps.Plugin, Kind: ps.Kind, Rect: ps.Rect}
		}
	}
	for slot, entries := range res.Contributions {
		if ps := cat.Slots[slot]; ps != nil {
			for _, c := range entries {
				ps.Contributions = append(ps.Contributions, contribution{PluginID: c.Plugin, ID: c.ID, Content: c.Label, OnClick: c.OnClick})
			}
		}
	}
	m.declared = m.fe.RailItems()
	var rails []contracts.RailItem
	for _, ri := range m.declared {
		if live[ri.Plugin] {
			rails = append(rails, ri)
		}
	}
	m.podStatus = status

	next := derived{Plugins: live, RailItems: rails, Catalog: cat}
	prev := derived{Plugins: m.plugins, RailItems: m.railItems, Catalog: m.catalog}
	if reflect.DeepEqual(next, prev) {
		return changed
	}
	m.plugins, m.railItems, m.catalog = live, rails, cat
	m.registerActions()
	m.pruneContent()
	m.dropGoneSurfaces()
	m.recomputeLayout()
	return true
}

// pruneContent forgets the last frame of every surface whose occupant changed
// or left: a departed plugin's content must not linger.
func (m *WorkbenchModel) pruneContent() {
	for s, su := range m.slotUpdates {
		if m.catalog.Occupants[s] != su.PluginID {
			delete(m.slotUpdates, s)
		}
	}
}

// dropGoneSurfaces leaves a placement or modal whose plugin is no longer live.
func (m *WorkbenchModel) dropGoneSurfaces() {
	if m.modal.PluginID != "" && !m.plugins[m.modal.PluginID] {
		m.closeModal()
	}
	if m.activeRailID != "" {
		if _, ok := m.railItemByID(m.activeRailID); !ok {
			m.activateHomeWorkbench()
		}
	}
}

// settle is the end of an Update: derived state is re-read, frontend-owned
// State is written, and the host ticks so the writes flush now. It repeats
// while a pass changes something (a new occupant can open a side column),
// bounded so a flapping host cannot spin the loop.
func (m *WorkbenchModel) settle() {
	for range 4 {
		changed := m.refresh()
		wrote := m.flushState()
		if wrote && m.host != nil {
			m.host.Tick()
		}
		m.noteRefusals()
		m.pollToast()
		if !changed && !wrote {
			return
		}
	}
}

// pollToast shows the oldest live engine toast unless a local one is up, and
// keeps a poll running while one is showing.
func (m *WorkbenchModel) pollToast() {
	if m.fe == nil {
		return
	}
	t, ok := m.fe.ActiveToast()
	switch {
	case ok && (m.activeToast == nil || m.engineToast):
		cp := t
		m.activeToast, m.engineToast = &cp, true
	case !ok && m.engineToast:
		m.activeToast, m.engineToast = nil, false
	}
	if m.engineToast && !m.toastPollOn {
		m.toastPollOn = true
		m.cmds = append(m.cmds, tea.Tick(toastPollInterval, func(time.Time) tea.Msg { return toastPollMsg{} }))
	}
}

type geoSize struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type geoRect struct {
	geoSize
	Inner geoSize `json:"inner"`
}

func toGeo(r SlotRect) geoRect {
	return geoRect{
		geoSize: geoSize{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height},
		Inner:   geoSize{X: r.InnerX, Y: r.InnerY, Width: r.InnerW, Height: r.InnerH},
	}
}

// geometryJSON is the value of husk.geometry: the terminal size and every
// laid-out rect (the modal's only while it is open).
func (m *WorkbenchModel) geometryJSON() []byte {
	rects := make(map[protocol.SurfaceID]geoRect, len(m.slotRects)+1)
	for id, r := range m.slotRects {
		rects[id] = toGeo(r)
	}
	if m.modalOwner().Open() {
		rects[protocol.SurfaceModal] = toGeo(m.modalRect)
	}
	doc := struct {
		Terminal struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"terminal"`
		TooSmall bool                           `json:"too_small"`
		Rects    map[protocol.SurfaceID]geoRect `json:"rects"`
	}{TooSmall: m.tooSmall(), Rects: rects}
	doc.Terminal.Width, doc.Terminal.Height = m.width, m.height
	b, _ := json.Marshal(doc)
	return b
}

// focusJSON is the value of husk.focus.
func (m *WorkbenchModel) focusJSON() []byte {
	focused := m.focusedSlot
	if m.modalOwner().Open() {
		focused = protocol.SurfaceModal
	}
	b, _ := json.Marshal(struct {
		Surface         protocol.SurfaceID `json:"surface"`
		Fallback        protocol.SurfaceID `json:"fallback"`
		TerminalFocused bool               `json:"terminal_focused"`
	}{focused, m.focusFallback, !m.blurred})
	return b
}

// surfaceView is the frontend half of husk.surfaces: what the layout shows.
func (m *WorkbenchModel) surfaceView() contracts.SurfaceView {
	var v contracts.SurfaceView
	for id := range m.slotRects {
		v.Visible = append(v.Visible, id)
	}
	if m.modalOwner().Open() {
		v.Visible = append(v.Visible, protocol.SurfaceModal)
	}
	slices.Sort(v.Visible)
	v.Placement = contracts.Placement{RailItem: m.activeRailID, Kind: string(m.activePlacement)}
	v.Overlays = contracts.Overlays{
		Modal:        contracts.ModalOverlay{Plugin: m.modal.PluginID, RailItem: m.modal.RailID},
		Palette:      m.showPalette,
		SlotExplorer: m.stageMode == "hotspots",
	}
	return v
}

// flushState writes the frontend-owned State that changed and reports whether
// it wrote anything. Equal values are not written again, so a quiet frame
// costs no State revision.
func (m *WorkbenchModel) flushState() bool {
	if m.fe == nil || !m.slicesOwned {
		return false
	}
	wrote := false
	put := func(h *state.Handle, last *[]byte, next []byte) {
		if string(next) == string(*last) {
			return
		}
		if _, err := h.Set(next); err != nil {
			m.logf("writing %s: %v", h.Namespace(), err)
			return
		}
		*last, wrote = next, true
	}
	put(m.slices.Geometry, &m.lastGeo, m.geometryJSON())
	put(m.slices.Focus, &m.lastFocus, m.focusJSON())
	put(m.slices.Layout, &m.lastLayout, m.layoutJSON())
	view := m.surfaceView()
	if b, _ := json.Marshal(view); string(b) != string(m.lastView) {
		if err := m.fe.UpdateSurfaceView(func(v *contracts.SurfaceView) { *v = view }); err != nil {
			m.logf("writing the surface view: %v", err)
		} else {
			m.lastView, wrote = b, true
		}
	}
	return wrote
}

// run invokes an action as the user: through the host's Dispatcher when one is
// attached, otherwise directly on the model's own handlers. A refusal toasts.
func (m *WorkbenchModel) run(action string, args map[string]any) {
	if m.fe == nil {
		if h := m.actions[action]; h != nil {
			_, _ = h(args)
		} else if ri, ok := m.railItemByID(action); ok {
			m.activateRailItem(ri)
		}
		return
	}
	m.fe.InvokeAction(action, args, func(_ map[string]any, err error) {
		if err == nil {
			return
		}
		m.logf("action %s: %v", action, err)
		m.cmds = append(m.cmds, m.showLocalToast("warn", "Action failed", action+": "+err.Error()))
	})
}

// registerActions binds the model's handlers with the host: the husk.* set
// once, and one handler per declared rail item, replaced as plugins come and
// go.
func (m *WorkbenchModel) registerActions() {
	if m.fe == nil {
		return
	}
	want := map[string]bool{}
	for id, h := range m.actions {
		want[id] = true
		if !m.registered[id] {
			if err := m.fe.RegisterHostAction(id, h); err != nil {
				m.logf("registering %s: %v", id, err)
				continue
			}
			m.registered[id] = true
		}
	}
	for _, ri := range m.declared {
		want[ri.ID] = true
		if m.registered[ri.ID] {
			continue
		}
		if err := m.fe.RegisterHostAction(ri.ID, func(map[string]any) (map[string]any, error) {
			if cur, ok := m.railItemByID(ri.ID); ok {
				m.activateRailItem(cur)
			}
			return nil, nil
		}); err != nil {
			m.logf("registering rail item %s: %v", ri.ID, err)
			continue
		}
		m.registered[ri.ID] = true
	}
	for id := range m.registered {
		if !want[id] {
			_ = m.fe.RegisterHostAction(id, nil)
			delete(m.registered, id)
		}
	}
}

// hostActionTable is the husk.* action set the model serves.
func (m *WorkbenchModel) hostActionTable() map[string]contracts.HostActionHandler {
	do := func(f func()) contracts.HostActionHandler {
		return func(map[string]any) (map[string]any, error) { f(); return nil, nil }
	}
	return map[string]contracts.HostActionHandler{
		"husk.quit":         do(func() { m.cmds = append(m.cmds, m.quitCmd()) }),
		"husk.home":         do(m.activateHomeWorkbench),
		"husk.palette.open": do(m.openPalette),
		"husk.hotspots.toggle_stage": do(func() {
			m.activeRailID = ""
			m.activePlacement = ""
			if m.stageMode == "plugin" {
				m.stageMode = "hotspots"
				m.focusedSlot = protocol.SurfaceMainStage
			} else {
				m.stageMode = "plugin"
				m.focusedSlot = protocol.SurfaceSidebarList
			}
			m.recomputeLayout()
		}),
		"husk.hotspots.badges": do(func() { m.showHotspots = !m.showHotspots }),
		"husk.sidebar.toggle": do(func() {
			m.showSidebar = !m.showSidebar
			m.recomputeLayout()
		}),
		"husk.sidepanel.toggle": do(func() {
			m.showSidePane = !m.showSidePane
			m.recomputeLayout()
		}),
		"husk.docks.cycle": do(func() {
			switch {
			case m.showSidebar && m.showSidePane:
				m.showSidePane = false
			case m.showSidebar && !m.showSidePane:
				m.showSidebar, m.showSidePane = false, true
			case !m.showSidebar && m.showSidePane:
				m.showSidePane = false
			default:
				m.showSidebar, m.showSidePane = true, true
			}
			m.recomputeLayout()
		}),
		"husk.layout.reset": do(func() {
			m.sidebarWidth, m.sidebarNavH, m.sidebarFootH = 38, 4, 4
			m.mainHeaderH, m.sidePaneW = 4, 36
			m.recomputeLayout()
			m.cmds = append(m.cmds, m.showLocalToast("info", "Layout Reset", "Restored default slot dimensions"))
		}),
		"husk.sidebar.narrower": do(func() {
			if m.leftSideShown() && m.sidebarWidth > 26 {
				m.sidebarWidth -= 2
				m.recomputeLayout()
			}
		}),
		"husk.sidebar.wider": do(func() {
			if m.leftSideShown() && m.sidebarWidth < 56 {
				m.sidebarWidth += 2
				m.recomputeLayout()
			}
		}),
		"husk.focus.next": do(m.focusNext),
		"husk.modifiers.toggle_alt_latch": do(func() {
			m.altLatched = !m.altLatched
			next := m.modifiers
			next.Alt = m.altLatched
			m.syncModifierTransitions(next)
		}),
		"husk.modal.close": do(m.closeModal),
	}
}

func (m *WorkbenchModel) openPalette() {
	m.showPalette = true
	m.paletteQuery = ""
	m.paletteCursor = 0
}

// focusNext moves focus to the next surface in tab order.
func (m *WorkbenchModel) focusNext() {
	if m.activePlacement == contracts.PlacementStage {
		if m.focusedSlot == protocol.SurfaceSidebarList {
			m.focusedSlot = protocol.SurfaceWorkspaceStage
		} else {
			m.focusedSlot = protocol.SurfaceSidebarList
		}
		return
	}
	order := []protocol.SurfaceID{
		protocol.SurfaceSidebarList,
		protocol.SurfaceMainStage,
		protocol.SurfaceSidePanel,
	}
	for i, s := range order {
		if s == m.focusedSlot {
			m.focusedSlot = order[(i+1)%len(order)]
			break
		}
	}
}

// homeApplies reports whether "home" has anywhere to go; otherwise the key
// belongs to the focused occupant.
func (m *WorkbenchModel) homeApplies() bool {
	return m.activeRailID != "" || m.stageMode == "hotspots"
}

func (m *WorkbenchModel) activateHomeWorkbench() {
	m.activeRailID = ""
	m.activePlacement = ""
	m.closeModal()
	m.stageMode = "plugin"
	m.focusedSlot = protocol.SurfaceSidebarList
	m.recomputeLayout()
}

// activateRailItem opens (or toggles closed) the surface of a rail item. The
// occupant learns of it through surface.resized, which the host pushes when
// the visibility and geometry land in State.
func (m *WorkbenchModel) activateRailItem(ri contracts.RailItem) {
	if !m.plugins[ri.Plugin] {
		return
	}

	switch ri.Placement {
	case contracts.PlacementModal:
		if m.modalOwner().RailID == ri.ID {
			m.closeModal()
			return
		}
		m.openModal(ri.Plugin, ri.ID)
		m.recomputeLayout()

	case contracts.PlacementFullCanvas:
		if m.activeRailID == ri.ID && m.activePlacement == contracts.PlacementFullCanvas {
			m.activateHomeWorkbench()
			return
		}
		m.closeModal()
		m.activeRailID = ri.ID
		m.activePlacement = contracts.PlacementFullCanvas
		m.focusedSlot = protocol.SurfaceFullCanvas
		m.recomputeLayout()

	case contracts.PlacementStage:
		if m.activeRailID == ri.ID && m.activePlacement == contracts.PlacementStage {
			m.activateHomeWorkbench()
			return
		}
		m.closeModal()
		m.activeRailID = ri.ID
		m.activePlacement = contracts.PlacementStage
		m.showSidebar = true
		m.focusedSlot = protocol.SurfaceWorkspaceStage
		m.recomputeLayout()
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

// surfaceOwner is the plugin that holds slot's claim: the occupant ("" when
// the host draws it). Write authorisation, published-content cleanup and
// rendering all resolve through the claim, including for the modal surface;
// modalOwner only says which modal is open and which rail item opened it.
func (m *WorkbenchModel) surfaceOwner(slot protocol.SurfaceID) string {
	return m.catalog.Occupants[slot]
}

// sendKey delivers a key to a plugin's surface; a plugin that just exited
// cannot receive it, which is not an error worth a toast.
func (m *WorkbenchModel) sendKey(plugin string, surface protocol.SurfaceID, key string) {
	if m.fe == nil || plugin == "" {
		return
	}
	if err := m.fe.SendKey(plugin, protocol.SurfaceKeyParams{Surface: surface, Key: key, Modifiers: m.modifiers}); err != nil {
		m.logf("key for %s: %v", plugin, err)
	}
}

// sendMouse delivers a mouse event to a plugin's surface.
func (m *WorkbenchModel) sendMouse(plugin string, p protocol.SurfaceMouseParams) {
	if m.fe == nil || plugin == "" {
		return
	}
	if err := m.fe.SendMouse(plugin, p); err != nil {
		m.logf("mouse for %s: %v", plugin, err)
	}
}
