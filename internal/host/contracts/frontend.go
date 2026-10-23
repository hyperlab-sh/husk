package contracts

import (
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Frontend is what a frontend (the TUI, later any embedding application)
// drives the host through. The host root implements it and Host.Frontend
// returns it. A frontend never imports Stem, Services, Pods or Store; it
// depends on this contract and on pkg/protocol.
//
// Every method documents which goroutine may call it. "Loop" means the Bubble
// Tea loop goroutine, from inside Update (or a command it runs on the loop);
// "any" means any goroutine. A loop-only method called elsewhere is a data
// race, not an error the host can detect.
//
// Protocol values (surface ids, key, mouse and key-state params, the palette)
// are the pkg/protocol types themselves. The Keybinding results are the one
// exception: the Services types cannot be imported here, so the small
// KeyContext, KeyResolution and KeyHint shapes below carry them, reusing
// KeyEntry for the binding.
type Frontend interface {
	// Snapshot returns the latest read-only State view. Any goroutine.
	Snapshot() *state.Snapshot

	// StartupReports returns a detached copy of the five construction-time
	// startup reports, available as soon as New returns. It does not change
	// after later reloads. The frontend uses error reports for its startup
	// toast; the startup flight predates event subscribers. Any goroutine.
	StartupReports() []ReloadReport

	// Palette returns the active theme palette. Any goroutine.
	Palette() protocol.Palette

	// Catalog returns a detached view of the optional catalog cache. Any
	// goroutine; never performs Store I/O. Warmed off-loop after startup and
	// every services.reloaded event, then published on the loop.
	Catalog() CatalogResult

	// Resolve resolves the typed key sequence against the effective keymap.
	// It is pure and lock-free. Any goroutine.
	Resolve(ctx KeyContext, keys []string) KeyResolution
	// Hints returns at most four effective footer hints for ctx. Any
	// goroutine.
	Hints(ctx KeyContext) []KeyHint
	// KeysFor returns the effective spellings of action, strongest first.
	// Any goroutine.
	KeysFor(action string) []string

	// InvokeAction runs action through the Dispatcher as kind: CallerInput for
	// a key binding Input resolved, with focus, the FocusContext Input read on
	// that key press (the Dispatcher uses it instead of State's), or
	// CallerHost for an action the host starts itself (a palette choice, a rail
	// item), with a nil focus. The two mismatches (input without focus, host
	// with focus) are composition bugs: nothing is dispatched, reply runs
	// synchronously with invalid_params and the host logs one line naming the
	// kind. Any other kind is denied. reply is called exactly once on the
	// loop, possibly before InvokeAction returns (always before, for a host
	// action); err is nil on success or a CodedError (denied, not_found,
	// unavailable, timeout ...), and declined is true when a host handler
	// returned ErrDeclined, in which case result and err are empty. Loop only.
	InvokeAction(action string, args map[string]any, kind protocol.CallerKind, focus *protocol.FocusContext, reply func(result map[string]any, declined bool, err error))

	// RegisterHostAction binds an in-process handler for a husk.* action id or
	// for the id of a rail item some declared plugin carries (RailItems), and
	// lists it in the action catalog under the host with its id and no title
	// or description. Any other id is refused with invalid_params, and an id
	// that a declared plugin already owns as an action is refused with
	// conflict: the Dispatcher consults host actions before plugin documents,
	// so accepting it would shadow the plugin's action. The handler runs on
	// the loop when the Dispatcher routes an invocation of id to it
	// (InvokeAction, the CLI's action.invoke); its error is reported with its
	// CodedError code, internal otherwise. A later registration of the same id
	// replaces the earlier one; a nil handler removes the binding and never
	// fails. A rail item action stays listed while its plugin is dead; the
	// Dispatcher's own liveness check answers an invocation for a dead
	// plugin's surface, not this catalog. Plugins cannot invoke host actions.
	// Loop only.
	RegisterHostAction(id string, handler HostActionHandler) error

	// SendKey delivers input.key to a live plugin's Pod. It returns a
	// CodedError: not_found for an unknown plugin, unavailable for one that
	// is not live, invalid_params for params that fail validation. Loop only.
	SendKey(plugin string, params protocol.SurfaceKeyParams) error
	// SendMouse delivers input.mouse; errors as SendKey. Loop only.
	SendMouse(plugin string, params protocol.SurfaceMouseParams) error
	// SendKeyState delivers input.key_state; errors as SendKey. Loop only.
	SendKeyState(plugin string, params protocol.KeyStateParams) error

	// RailItems lists the rail items every declared plugin carries, from the
	// declared plugins documents (the catalog, pods.md "Pod and manifest
	// conversion"): sorted by plugin id, then in manifest order. The list
	// changes when a launch gate or a teardown re-declares a plugin; read it
	// again after such a tick. Any goroutine.
	RailItems() []RailItem

	// PublishedSlots lists every published extension point the declared
	// plugins documents carry (kind inline or region, with the region's rect),
	// sorted by plugin id, then in manifest order. Any goroutine.
	PublishedSlots() []PublishedSlot

	// Actions lists every action the declared plugins documents carry, sorted
	// by plugin id, then in manifest order. The caller filters by liveness.
	// Any goroutine.
	Actions() []PluginAction

	// SurfaceFrames lists the frame each plugin declared for a surface it
	// claims (manifest surfaces[].frame), keyed by plugin id and surface; a
	// surface without a declared frame is absent. Any goroutine.
	SurfaceFrames() map[string]map[protocol.SurfaceID]protocol.FrameSpec

	// ActiveToast expires stale toasts on the host clock and returns the
	// oldest live one (plugin and CLI toast.show calls queue there); false when
	// none is showing. Loop only.
	ActiveToast() (Toast, bool)

	// Refusals lists the plugins the host refused before a Pod existed
	// (invalid manifest, duplicate plugin id, protocol major mismatch), in
	// the order they were refused. The list only grows; a frontend remembers
	// how many it has shown and reports the rest as the same failure toast a
	// launch failure gets. Any goroutine.
	Refusals() []PluginRefusal

	// LatestConfigNotice reports the newest reload the user did not start from
	// the CLI (startup, profile, plugin) that finished with at least one error
	// problem, and how many such reloads there have been. seq only grows; a
	// frontend toasts when it is above the seq it last saw. Any goroutine.
	LatestConfigNotice() (seq uint64, last ConfigNotice)

	// OwnSlices registers the frontend's owner handles (husk.layout,
	// husk.geometry, husk.focus). It may be called once, before the loop
	// runs or on it after Run has started (a persistent husk.layout hydrates
	// from Store's preloaded values either way); a second call fails. Handle
	// methods are loop only.
	OwnSlices() (FrontendSlices, error)

	// UpdateSurfaceView applies fn to the frontend-owned half of
	// husk.surfaces (visibility, placement, overlays) and commits it; the
	// engine keeps writing occupants and contributions. Loop only.
	UpdateSurfaceView(fn func(*SurfaceView)) error
}

// PublishedSlot is one published extension point a plugin declared in its
// manifest. Kind is "inline" or "region"; Rect is set for a region.
type PublishedSlot struct {
	Plugin string         `json:"plugin"`
	ID     string         `json:"id"`
	Kind   string         `json:"kind"`
	Rect   *PublishedRect `json:"rect,omitempty"`
}

// PublishedRect places a region inside a claimed host surface, in the
// surface's content cells.
type PublishedRect struct {
	Surface protocol.SurfaceID `json:"surface"`
	X       int                `json:"x"`
	Y       int                `json:"y"`
	Width   int                `json:"width"`
	Height  int                `json:"height"`
}

// Toast is one transient notice: a plugin's or the CLI's toast.show. Plugin
// is empty for a notice the host attributes to itself.
type Toast struct {
	Plugin  string
	Level   string
	Title   string
	Message string
}

// PluginRefusal is one plugin the host refused before a Pod existed, so no
// husk.pods entry carries the failure. Plugin is empty when the manifest was
// too broken to name one; Reason then names the manifest.
type PluginRefusal struct {
	Plugin string
	Reason string
}

// ConfigNotice is one reload that ended with error problems the user has not
// seen: Reason is the reload's trigger and Errors the error count across the
// five reports.
type ConfigNotice struct {
	Reason string
	Errors int
}

// HostActionHandler serves one host action on the loop. args is the
// invocation's JSON object, possibly nil; a nil result is an empty object. A
// handler that has nothing to do for a key press returns ErrDeclined.
type HostActionHandler func(args map[string]any) (result map[string]any, err error)

// RailPlacement is how a rail item opens its surface. It has the same wire
// values as the manifest's rail item placement; a test pins the two shapes
// together, because the pods package may not import this one.
type RailPlacement string

const (
	// PlacementModal opens the surface as a modal overlay.
	PlacementModal RailPlacement = "modal"
	// PlacementFullCanvas opens it over everything right of the rail.
	PlacementFullCanvas RailPlacement = "full-canvas"
	// PlacementStage opens it as the workspace stage.
	PlacementStage RailPlacement = "stage"
)

// Valid reports whether p is a known rail placement.
func (p RailPlacement) Valid() bool {
	switch p {
	case PlacementModal, PlacementFullCanvas, PlacementStage:
		return true
	}
	return false
}

// RailItem is one activity-rail item a plugin declared in its manifest
// (rail_items[]). ID is "<plugin_id>.<name>" and doubles as the action id
// that activates the item. Placement is modal, full-canvas or stage. Frame is
// the chrome drawn around the surface the item opens, nil for a blank canvas.
type RailItem struct {
	Plugin      string              `json:"plugin"`
	ID          string              `json:"id"`
	Icon        string              `json:"icon"`
	Badge       string              `json:"badge,omitempty"`
	Label       string              `json:"label"`
	Placement   RailPlacement       `json:"placement"`
	Description string              `json:"description,omitempty"`
	Frame       *protocol.FrameSpec `json:"frame,omitempty"`
}

// FrontendSlices are the State slices the frontend owns, as untyped owner
// handles holding JSON objects: husk.layout (persistent dock preferences),
// husk.geometry (terminal size and computed rects) and husk.focus (focused
// surface). Writing husk.geometry, husk.focus or the visibility in
// husk.surfaces is what drives surface.resized to the occupants.
type FrontendSlices struct {
	// Layout is husk.layout: instance scope, persistent, keep-latest.
	Layout *state.Handle
	// Geometry is husk.geometry: instance scope, not persistent.
	Geometry *state.Handle
	// Focus is husk.focus: instance scope, not persistent.
	Focus *state.Handle
}

// SurfaceView is the frontend-owned half of the husk.surfaces value.
type SurfaceView struct {
	// Visible lists the surfaces the layout currently shows.
	Visible []protocol.SurfaceID
	// Placement is the active placement.
	Placement Placement
	// Overlays are the active host overlays.
	Overlays Overlays
}

// Placement is the active placement (husk.surfaces placement).
type Placement struct {
	RailItem string `json:"rail_item"`
	Kind     string `json:"kind"`
}

// Overlays are the active host overlays (husk.surfaces overlays).
type Overlays struct {
	Modal        ModalOverlay `json:"modal"`
	Palette      bool         `json:"palette"`
	SlotExplorer bool         `json:"slot_explorer"`
}

// ModalOverlay is the modal overlay: which plugin's rail item is open.
type ModalOverlay struct {
	Plugin   string `json:"plugin"`
	RailItem string `json:"rail_item"`
}

// KeyContext carries the focus and overlay facts a key resolution needs.
type KeyContext struct {
	FocusSurface string
	FocusOwner   string
	ModalOpen    bool
	// ModalAction is the open modal's rail action id.
	ModalAction string
	// Capture is set while the focused occupant takes text input on
	// FocusSurface (husk.surfaces.capture).
	Capture bool
	Frozen  bool
}

// KeyOutcome is the result of resolving one typed sequence.
type KeyOutcome uint8

const (
	// KeyNoMatch hands the original key to the occupant.
	KeyNoMatch KeyOutcome = iota
	// KeyPrefix means wait for the next key.
	KeyPrefix
	// KeyMatch means dispatch Binding's action.
	KeyMatch
)

// KeyResolution is one lookup. Binding is set only for KeyMatch.
type KeyResolution struct {
	Outcome KeyOutcome
	Binding *KeyEntry
}

// KeyHint is one effective footer advertisement.
type KeyHint struct {
	Keys   []string `json:"keys"`
	Action string   `json:"action"`
	Text   string   `json:"hint"`
}

// PluginAction is one declared plugin action as the palette lists it.
type PluginAction struct {
	Plugin      string
	ID          string
	Title       string
	Description string
}
