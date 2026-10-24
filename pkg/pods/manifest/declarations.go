package manifest

import (
	"encoding/json"
	"slices"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// ActionDecl is one manifest-owned catalog action.
type ActionDecl struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	ArgsSchema  json.RawMessage `json:"args_schema,omitempty"`
	Public      bool            `json:"public"`
}

// SurfaceDecl claims a host surface and its independent decorations.
type SurfaceDecl struct {
	ID    protocol.SurfaceID  `json:"id"`
	Frame *protocol.FrameSpec `json:"frame,omitempty"`
}

// RailPlacement is how a rail item opens its surface.
type RailPlacement string

const (
	// PlacementModal opens the surface as a modal overlay.
	PlacementModal RailPlacement = "modal"
	// PlacementFullCanvas replaces the whole canvas with the surface.
	PlacementFullCanvas RailPlacement = "full-canvas"
	// PlacementStage opens the surface in the main stage.
	PlacementStage RailPlacement = "stage"
)

// Valid reports whether p is a placement the frontend knows how to open.
func (p RailPlacement) Valid() bool {
	switch p {
	case PlacementModal, PlacementFullCanvas, PlacementStage:
		return true
	}
	return false
}

// RailItemDecl declares one activity-rail item of the plugin. The shape is the
// legacy rail item minus its shortcut (a key is a keymap binding, never a
// declaration, see keybindings.md). The id is "<plugin_id>.<name>" and doubles
// as the action id that activates the item. Frame is the chrome drawn around
// the surface the item opens; absent means a blank canvas.
type RailItemDecl struct {
	ID          string              `json:"id"`
	Icon        string              `json:"icon"`
	Badge       string              `json:"badge,omitempty"`
	Label       string              `json:"label"`
	Placement   RailPlacement       `json:"placement"`
	Description string              `json:"description,omitempty"`
	Frame       *protocol.FrameSpec `json:"frame,omitempty"`
}

// FocusBinding is a focus-local suggestion; Services alone parses key spelling.
type FocusBinding struct {
	Key    string          `json:"key,omitempty"`
	Keys   []string        `json:"keys,omitempty"`
	Action string          `json:"action"`
	Focus  string          `json:"focus"`
	Hint   string          `json:"hint,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// EventDecl describes a plugin event. Nil Coalesce means the default, true.
type EventDecl struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Coalesce    *bool  `json:"coalesce,omitempty"`
}

// HookKind is the contract a hook's owner declares: what subscribers may do.
type HookKind string

const (
	// HookTransform lets subscribers replace a value, in turn.
	HookTransform HookKind = "transform"
	// HookCollect lets subscribers contribute items.
	HookCollect HookKind = "collect"
	// HookGate lets subscribers allow or veto.
	HookGate HookKind = "gate"
)

// Valid reports whether k is one of the three kinds.
func (k HookKind) Valid() bool {
	return k == HookTransform || k == HookCollect || k == HookGate
}

// Hook timeout bounds in milliseconds: the Calls MinTimeout and DefaultTimeout.
const (
	MinHookTimeoutMs     = 1000
	DefaultHookTimeoutMs = 10000
	// MaxHookTimeoutMs equals the default: the Calls default is also the cap.
	MaxHookTimeoutMs = DefaultHookTimeoutMs
)

// Hook subscription priority bounds; lower runs first.
const (
	MinHookPriority     = 0
	MaxHookPriority     = 100
	DefaultHookPriority = 10
)

// HookDecl declares a plugin-owned hook point. Conversion writes the resolved
// TimeoutMs; Kind has no default.
type HookDecl struct {
	ID          string   `json:"id"`
	Kind        HookKind `json:"kind"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	TimeoutMs   int      `json:"timeout_ms"`
}

// HookRegistration subscribes this plugin to a hook point. Conversion writes
// the resolved Priority.
type HookRegistration struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

// PublishedRect places an extension region inside a claimed host surface.
type PublishedRect struct {
	Surface protocol.SurfaceID `json:"surface"`
	X       int                `json:"x"`
	Y       int                `json:"y"`
	Width   int                `json:"width"`
	Height  int                `json:"height"`
}

// SlotDecl declares an inline or rectangular published extension point.
type SlotDecl struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Kind        string         `json:"kind"`
	Rect        *PublishedRect `json:"rect,omitempty"`
}

// RetentionDecl follows State's persistence fields; TTL is integer nanoseconds.
type RetentionDecl struct {
	Policy string `json:"policy"`
	N      int    `json:"n,omitempty"`
	TTL    int64  `json:"ttl,omitempty"`
}

// SliceDecl configures the Pod-owned state slice, never its owner identity.
type SliceDecl struct {
	Scope      string         `json:"scope"`
	Persistent bool           `json:"persistent"`
	Retention  *RetentionDecl `json:"retention,omitempty"`
	MaxBytes   int            `json:"max_bytes,omitempty"`
}

// SecretRequest asks for a user-approved grant; it contains no secret value.
type SecretRequest struct {
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
	Required bool   `json:"required"`
}

// SettingDecl is a plugin setting schema. Default retains explicit null/zero;
// nil is absent and rejected. Duration defaults and bounds are milliseconds.
type SettingDecl struct {
	Key     string          `json:"key"`
	Type    string          `json:"type"`
	Default json.RawMessage `json:"default"`
	Enum    []string        `json:"enum,omitempty"`
	Min     *int64          `json:"min,omitempty"`
	Max     *int64          `json:"max,omitempty"`
	Doc     string          `json:"doc,omitempty"`
	Apply   string          `json:"apply,omitempty"`
}

// BindingProblem preserves a conversion rejection for keys --problems.
type BindingProblem struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Key names the offending manifest member when Index is not a binding
	// position, e.g. "events.subscribe[1]".
	Key string `json:"key,omitempty"`
}

// Suggestion is an ignored legacy shortcut, never a routing declaration.
type Suggestion struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

// Keymap is the normalized declared keymap document: the focus-local bindings a
// plugin's manifest declares (D64). It is not the layered keymap (distribution and
// user layers) that Store.Layers("keymap", profile) returns and
// pkg/services/keybinding resolves (D90); this document is read through
// Store.Declared("keymap").
type Keymap struct {
	Version     int              `json:"version"`
	Bind        []FocusBinding   `json:"bind"`
	Problems    []BindingProblem `json:"problems,omitempty"`
	Suggestions []Suggestion     `json:"suggestions,omitempty"`
}

// CatalogJSON returns the normalized plugins document, without process secrets,
// argv or host paths. Store/Services integration supplies its own Declare call.
func (s Spec) CatalogJSON() ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	for _, key := range []string{"protocol", "command", "work_dir", "dir", "binding_problems", "subscription_problems", "suggestions"} {
		delete(doc, key)
	}
	for key, value := range s.Vendor {
		doc[key] = value
	}
	return json.Marshal(doc)
}

// KeymapJSON returns version 1, an always-present bind array and diagnostics.
func (s Spec) KeymapJSON() ([]byte, error) {
	bind := s.Keybindings
	if bind == nil {
		bind = []FocusBinding{}
	}
	problems := append(slices.Clone(s.BindingProblems), s.SubscriptionProblems...)
	return json.Marshal(Keymap{Version: 1, Bind: bind, Problems: problems, Suggestions: s.Suggestions})
}
