package huskipc

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

type SlotID string

const (
	SlotActivityRail      SlotID = "activity-rail"
	SlotSidebarNavigation SlotID = "sidebar-navigation"
	SlotSidebarList       SlotID = "sidebar-list"
	SlotThreadRowStatus   SlotID = "thread-row-status"
	SlotSidebarFooter     SlotID = "sidebar-footer"
	SlotMainHeader        SlotID = "main-header"
	SlotMainStage         SlotID = "main-stage"
	SlotSidePanel         SlotID = "side-panel"
	SlotAppOverlay        SlotID = "app-overlay"

	// Dynamic Surfaces for Activity Rail Plugins:
	SlotModalSurface   SlotID = "modal-surface"
	SlotFullCanvas     SlotID = "full-canvas"
	SlotWorkspaceStage SlotID = "workspace-stage"
)

type RailPlacement string

const (
	PlacementModal      RailPlacement = "modal"       // Case 1: Floating modal (tv, telescope, session search)
	PlacementFullCanvas RailPlacement = "full-canvas" // Case 2: Entire canvas minus activity rail (dashboards, plugin mgr)
	PlacementStage      RailPlacement = "stage"       // Case 3: Canvas minus sidebar (lazygit, gh dash, vim)
)

type RailItemSpec struct {
	ID          string        `json:"id"`
	PluginID    string        `json:"plugin_id,omitempty"`
	Icon        string        `json:"icon"`
	Badge       string        `json:"badge,omitempty"`
	Label       string        `json:"label"`
	Shortcut    string        `json:"shortcut,omitempty"`
	Placement   RailPlacement `json:"placement"`
	Description string        `json:"description,omitempty"`

	// Frame declares the chrome Husk draws around the surface opened by this
	// rail item (border / label / close). Absent means a blank canvas.
	Frame *frame.Spec `json:"frame,omitempty"`
}

type SlotMeta struct {
	ID          SlotID `json:"id"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	BBHotspot   string `json:"bb_hotspot"`
	Description string `json:"description"`
}

var AllSlots = []SlotMeta{
	{
		ID:          SlotActivityRail,
		Number:      0,
		Title:       "Activity Bar / Icon Rail",
		BBHotspot:   "sidebar-navigation (rail)",
		Description: "Far-left vertical icon column launching Modal (1), Full-Canvas (2), or Stage-minus-Sidebar (3) plugins.",
	},
	{
		ID:          SlotSidebarNavigation,
		Number:      1,
		Title:       "Sidebar Navigation",
		BBHotspot:   "sidebar-navigation (header)",
		Description: "Top switcher above the sidebar list (Spaces, Tree, Agents, Sort/Filter).",
	},
	{
		ID:          SlotSidebarList,
		Number:      2,
		Title:       "Sidebar List Surface",
		BBHotspot:   "thread-list",
		Description: "Primary sidebar surface for Spaces, Worktrees, Tabs, Panes, and live Agents.",
	},
	{
		ID:          SlotThreadRowStatus,
		Number:      3,
		Title:       "Row Status & Telemetry",
		BBHotspot:   "thread-row-status",
		Description: "Per-row agent status spinners, context % gauges, and worktree pills.",
	},
	{
		ID:          SlotSidebarFooter,
		Number:      4,
		Title:       "Sidebar Footer",
		BBHotspot:   "sidebar-footer",
		Description: "Persistent bottom sidebar bar for daemon health, socket telemetry, and quick toggles.",
	},
	{
		ID:          SlotMainHeader,
		Number:      5,
		Title:       "Main Header",
		BBHotspot:   "thread-header",
		Description: "Stage header displaying active Workspace, Worktree branch, Tab pills, and quick actions.",
	},
	{
		ID:          SlotMainStage,
		Number:      6,
		Title:       "Main Stage / Project Canvas",
		BBHotspot:   "nav-panel / stage",
		Description: "Central canvas rendering Workspace Topology or active Stage plugin (lazygit, gh dash).",
	},
	{
		ID:          SlotSidePanel,
		Number:      7,
		Title:       "Side Panel Drawer",
		BBHotspot:   "thread-panel",
		Description: "Collapsible right-hand inspector for deep Agent context, tokens, & Husk IPC telemetry.",
	},
	{
		ID:          SlotAppOverlay,
		Number:      8,
		Title:       "Command Palette & Modal Overlays",
		BBHotspot:   "command-palette / app-overlay",
		Description: "Modal Command Palette (Ctrl+P), Rail Modal plugins (tv/telescope), and Toast stack.",
	},
}

type SlotSize struct {
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Focused bool `json:"focused"`
	Visible bool `json:"visible"`
}

type CommandSpec struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Shortcut    string `json:"shortcut,omitempty"`
	Description string `json:"description,omitempty"`
}

type KeybindSpec struct {
	Key         string `json:"key"`
	CommandID   string `json:"command_id"`
	Description string `json:"description"`
	WhenSlot    SlotID `json:"when_slot,omitempty"`
}

type RowHitTarget struct {
	Line    int               `json:"line"`
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Payload map[string]string `json:"payload,omitempty"`
}

type WorkspaceContextPayload struct {
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceLabel string `json:"workspace_label"`
	TabID          string `json:"tab_id,omitempty"`
	PaneID         string `json:"pane_id,omitempty"`
	Cwd            string `json:"cwd"`
	RepoName       string `json:"repo_name,omitempty"`
}

type RailActivatedPayload struct {
	RailID    string                  `json:"rail_id"`
	Placement RailPlacement           `json:"placement"`
	Active    bool                    `json:"active"`
	Width     int                     `json:"width"`
	Height    int                     `json:"height"`
	Context   WorkspaceContextPayload `json:"context"`
}

type Envelope struct {
	Type string `json:"type"`

	Hello             *HelloPayload             `json:"hello,omitempty"`
	Welcome           *WelcomePayload           `json:"welcome,omitempty"`
	SlotUpdate        *SlotUpdatePayload        `json:"slot_update,omitempty"`
	SlotResize        *SlotResizePayload        `json:"slot_resize,omitempty"`
	SlotKey           *SlotKeyPayload           `json:"slot_key,omitempty"`
	KeyState          *KeyStatePayload          `json:"key_state,omitempty"`
	SlotMouse         *SlotMousePayload         `json:"slot_mouse,omitempty"`
	CommandInvoke     *CommandInvokePayload     `json:"command_invoke,omitempty"`
	ThemeChanged      *ThemeChangedPayload      `json:"theme_changed,omitempty"`
	WorkspaceContext  *WorkspaceContextPayload  `json:"workspace_context,omitempty"`
	RailActivated     *RailActivatedPayload     `json:"rail_activated,omitempty"`
	CloseModal        bool                      `json:"close_modal,omitempty"`
	EventEmit         *EventPayload             `json:"event,omitempty"`
	Toast             *ToastPayload             `json:"toast,omitempty"`
	SlotDeclare       *SlotDeclarePayload       `json:"slot_declare,omitempty"`
	SlotContribute    *SlotContributePayload    `json:"slot_contribute,omitempty"`
	SlotContributions *SlotContributionsPayload `json:"slot_contributions,omitempty"`
	HookDeclare       *HookDeclarePayload       `json:"hook_declare,omitempty"`
	HookSubscribe     *HookSubscribePayload     `json:"hook_subscribe,omitempty"`
	HookCall          *HookCallPayload          `json:"hook_call,omitempty"`
	HookResult        *HookResultPayload        `json:"hook_result,omitempty"`
	EventDeclare      *EventDeclarePayload      `json:"event_declare,omitempty"`
	EventSubscribe    *EventSubscribePayload    `json:"event_subscribe,omitempty"`
	ProtocolError     *ProtocolErrorPayload     `json:"protocol_error,omitempty"`
}

type HelloPayload struct {
	PluginID       string                `json:"plugin_id"`
	Name           string                `json:"name"`
	Version        string                `json:"version"`
	Language       string                `json:"language"`
	PID            int                   `json:"pid"`
	Description    string                `json:"description"`
	Slots          []SlotID              `json:"slots"`
	RailItems      []RailItemSpec        `json:"rail_items,omitempty"`
	Commands       []CommandSpec         `json:"commands,omitempty"`
	Keybindings    []KeybindSpec         `json:"keybindings,omitempty"`
	PublishedSlots []SlotDeclarePayload  `json:"published_slots,omitempty"`
	Hooks          []HookDeclarePayload  `json:"hooks,omitempty"`
	Events         []EventDeclarePayload `json:"events,omitempty"`

	// Frames declares per-slot chrome (border / label / close) keyed by slot
	// ID. Absent entry (or absent map) means the surface is a blank canvas.
	Frames map[SlotID]frame.Spec `json:"frames,omitempty"`
}

type ModifierState struct {
	Alt   bool `json:"alt"`
	Ctrl  bool `json:"ctrl"`
	Shift bool `json:"shift"`
	Super bool `json:"super"`
}

type KeyStatePayload struct {
	Phase     string        `json:"phase"`               // "down" | "up" | "repeat" | "sync"
	Key       string        `json:"key"`                 // "alt", "ctrl", "shift", "super", or key identifier
	Held      bool          `json:"held"`                // True while this key/modifier is actively held down
	Modifiers ModifierState `json:"modifiers"`           // Authoritative snapshot of all currently held modifiers
	HeldKeys  []string      `json:"held_keys,omitempty"` // All keys/modifiers currently held down
	Slot      SlotID        `json:"slot,omitempty"`      // Active slot when transition occurred
}

type WelcomePayload struct {
	HostVersion string                  `json:"host_version"`
	Palette     theme.Palette           `json:"palette"`
	SlotSizes   map[SlotID]SlotSize     `json:"slot_sizes"`
	FocusedSlot SlotID                  `json:"focused_slot"`
	Context     WorkspaceContextPayload `json:"context"`
	Modifiers   ModifierState           `json:"modifiers"`
}

type SlotUpdatePayload struct {
	PluginID   string         `json:"plugin_id"`
	Slot       SlotID         `json:"slot"`
	Title      string         `json:"title,omitempty"`
	Subtitle   string         `json:"subtitle,omitempty"`
	Badge      string         `json:"badge,omitempty"`
	Lines      []string       `json:"lines"`
	HitTargets []RowHitTarget `json:"hit_targets,omitempty"`
	StatusMeta map[string]any `json:"status_meta,omitempty"`
}

type SlotResizePayload struct {
	SlotSizes   map[SlotID]SlotSize `json:"slot_sizes"`
	FocusedSlot SlotID              `json:"focused_slot"`
}

type SlotKeyPayload struct {
	Slot      string        `json:"slot"`
	Key       string        `json:"key"`
	Modifiers ModifierState `json:"modifiers"`
}

type SlotMousePayload struct {
	Slot       SlotID            `json:"slot"`
	X          int               `json:"x"`
	Y          int               `json:"y"`
	Button     string            `json:"button"`
	HitID      string            `json:"hit_id,omitempty"`
	HitKind    string            `json:"hit_kind,omitempty"`
	HitAction  string            `json:"hit_action,omitempty"`
	HitPayload map[string]string `json:"hit_payload,omitempty"`
}

type CommandInvokePayload struct {
	CommandID string            `json:"command_id"`
	Args      map[string]string `json:"args,omitempty"`
}

type ThemeChangedPayload struct {
	Palette theme.Palette `json:"palette"`
}

type EventPayload struct {
	PluginID string         `json:"plugin_id"`
	Topic    string         `json:"topic"`
	Data     map[string]any `json:"data,omitempty"`
}

type ToastPayload struct {
	PluginID string `json:"plugin_id"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

// ConnWriter writes envelopes as JSON lines. A writer made with NewConnWriter
// writes synchronously on the caller's goroutine; one made with
// NewQueuedConnWriter never blocks the caller (see queue.go).
type ConnWriter struct {
	w  io.Writer
	mu sync.Mutex

	q *sendQueue // nil for a synchronous writer
}

// NewConnWriter returns a synchronous writer: Send blocks until the line has
// been written to w.
func NewConnWriter(w io.Writer) *ConnWriter {
	return &ConnWriter{w: w}
}

func (w *ConnWriter) Send(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if w.q != nil {
		return w.q.enqueue(data)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(data)
	return err
}

func ReadLoop(r io.Reader, onMessage func(Envelope)) error {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		onMessage(env)
	}
	return scanner.Err()
}
