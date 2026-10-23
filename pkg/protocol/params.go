package protocol

import (
	"strings"
	"unicode"
)

// SurfaceID identifies a host layout surface.
type SurfaceID string

const (
	// SurfaceActivityRail is the activity rail.
	SurfaceActivityRail SurfaceID = "activity-rail"
	// SurfaceSidebarNavigation is the sidebar navigation band.
	SurfaceSidebarNavigation SurfaceID = "sidebar-navigation"
	// SurfaceSidebarList is the sidebar list.
	SurfaceSidebarList SurfaceID = "sidebar-list"
	// SurfaceThreadRowStatus is the row status band.
	SurfaceThreadRowStatus SurfaceID = "thread-row-status"
	// SurfaceSidebarFooter is the sidebar footer.
	SurfaceSidebarFooter SurfaceID = "sidebar-footer"
	// SurfaceMainHeader is the main header.
	SurfaceMainHeader SurfaceID = "main-header"
	// SurfaceMainStage is the central canvas.
	SurfaceMainStage SurfaceID = "main-stage"
	// SurfaceSidePanel is the right drawer.
	SurfaceSidePanel SurfaceID = "side-panel"
	// SurfaceAppOverlay is the host overlay layer.
	SurfaceAppOverlay SurfaceID = "app-overlay"
	// SurfaceModal is the floating plugin surface.
	SurfaceModal SurfaceID = "modal-surface"
	// SurfaceFullCanvas occupies the full canvas.
	SurfaceFullCanvas SurfaceID = "full-canvas"
	// SurfaceWorkspaceStage occupies the workspace stage.
	SurfaceWorkspaceStage SurfaceID = "workspace-stage"
)

// Valid reports whether s is a defined host layout surface.
func (s SurfaceID) Valid() bool {
	switch s {
	case SurfaceActivityRail, SurfaceSidebarNavigation, SurfaceSidebarList,
		SurfaceThreadRowStatus, SurfaceSidebarFooter, SurfaceMainHeader,
		SurfaceMainStage, SurfaceSidePanel, SurfaceAppOverlay, SurfaceModal,
		SurfaceFullCanvas, SurfaceWorkspaceStage:
		return true
	}
	return false
}

// Humanised returns the surface id with every hyphen replaced by a space and
// its first rune upper-cased, leaving all other runes unchanged.
func (s SurfaceID) Humanised() string {
	runes := []rune(strings.ReplaceAll(string(s), "-", " "))
	if len(runes) > 0 {
		runes[0] = unicode.ToUpper(runes[0])
	}
	return string(runes)
}

// FrameSpec declares independent border, label and close decorations.
type FrameSpec struct {
	Border bool   `json:"border"`
	Label  string `json:"label,omitempty"`
	Close  bool   `json:"close,omitempty"`
}

// Palette carries generic colour roles as #RRGGBB strings.
type Palette struct {
	Name            string `json:"name"`
	Canvas          string `json:"canvas"`
	Surface         string `json:"surface"`
	SurfaceElevated string `json:"surface_elevated"`
	SurfaceSelected string `json:"surface_selected"`
	SurfaceHover    string `json:"surface_hover"`
	Slot            string `json:"slot_background,omitempty"`
	Border          string `json:"border"`
	BorderSubtle    string `json:"border_subtle"`
	BorderFocused   string `json:"border_focused"`
	Ink             string `json:"ink"`
	InkMuted        string `json:"ink_muted"`
	InkSubtle       string `json:"ink_subtle"`
	Accent          string `json:"accent"`
	AccentSecondary string `json:"accent_secondary"`
	AccentSoft      string `json:"accent_soft"`
	Success         string `json:"success"`
	Warning         string `json:"warning"`
	Danger          string `json:"danger"`
	Info            string `json:"info"`
}

// SurfaceSize is one surface's current inner geometry and visibility.
type SurfaceSize struct {
	Surface SurfaceID `json:"surface"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	Focused bool      `json:"focused"`
	Visible bool      `json:"visible"`
}

// SurfaceResizedParams delivers all affected sizes in one consistent snapshot.
type SurfaceResizedParams struct {
	Surfaces []SurfaceSize `json:"surfaces"`
}

// RowHitTarget declares a zero-based row target in rendered content.
type RowHitTarget struct {
	Line    int               `json:"line"`
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Payload map[string]string `json:"payload,omitempty"`
}

// SurfaceUpdateParams replaces a surface's content. The host ignores PluginID
// from a sender and derives identity from the authenticated channel.
type SurfaceUpdateParams struct {
	PluginID   string         `json:"plugin_id,omitempty"`
	Surface    SurfaceID      `json:"surface"`
	Title      string         `json:"title,omitempty"`
	Subtitle   string         `json:"subtitle,omitempty"`
	Badge      string         `json:"badge,omitempty"`
	Lines      []string       `json:"lines"`
	HitTargets []RowHitTarget `json:"hit_targets,omitempty"`
	StatusMeta map[string]any `json:"status_meta,omitempty"`
	// Capture sets the key policy. Nil leaves it unchanged; boolean spellings
	// retain their original meaning, and text/all select explicit modes.
	Capture *CaptureValue `json:"capture,omitempty"`
}

// SurfaceCloseParams closes the named dynamic surface.
type SurfaceCloseParams struct {
	Surface SurfaceID `json:"surface"`
}

// ModifierState is the authoritative modifier snapshot.
type ModifierState struct {
	Alt   bool `json:"alt"`
	Ctrl  bool `json:"ctrl"`
	Shift bool `json:"shift"`
	Super bool `json:"super"`
}

// KeyPhase identifies a key-state transition.
type KeyPhase string

const (
	// KeyPhaseDown reports a press.
	KeyPhaseDown KeyPhase = "down"
	// KeyPhaseUp reports a release or expired hold.
	KeyPhaseUp KeyPhase = "up"
	// KeyPhaseRepeat reports auto-repeat.
	KeyPhaseRepeat KeyPhase = "repeat"
	// KeyPhaseSync reports a modifier resynchronization.
	KeyPhaseSync KeyPhase = "sync"
)

// Valid reports whether p is a defined key-state phase.
func (p KeyPhase) Valid() bool {
	switch p {
	case KeyPhaseDown, KeyPhaseUp, KeyPhaseRepeat, KeyPhaseSync:
		return true
	}
	return false
}

// KeyStateParams reports modifiers and best-effort ordinary held keys.
type KeyStateParams struct {
	Phase     KeyPhase      `json:"phase"`
	Key       string        `json:"key"`
	Held      bool          `json:"held"`
	Modifiers ModifierState `json:"modifiers"`
	HeldKeys  []string      `json:"held_keys,omitempty"`
	Surface   SurfaceID     `json:"surface,omitempty"`
}

// Validate checks enumerated key-state fields.
func (p KeyStateParams) Validate() error {
	if !p.Phase.Valid() || p.Key == "" || p.Surface != "" && !p.Surface.Valid() {
		return &Error{Code: CodeInvalidParams, Message: "key state requires a valid phase, key and optional surface"}
	}
	return nil
}

// SurfaceKeyParams delivers a key in the unchanged legacy key spelling.
type SurfaceKeyParams struct {
	Surface   SurfaceID     `json:"surface"`
	Key       string        `json:"key"`
	Modifiers ModifierState `json:"modifiers"`
}

// Validate checks the surface and nonempty key.
func (p SurfaceKeyParams) Validate() error {
	if !p.Surface.Valid() || p.Key == "" {
		return &Error{Code: CodeInvalidParams, Message: "key delivery requires a valid surface and key"}
	}
	return nil
}

// SurfaceMouseParams delivers surface-local coordinates and optional hit data.
type SurfaceMouseParams struct {
	Surface    SurfaceID         `json:"surface"`
	X          int               `json:"x"`
	Y          int               `json:"y"`
	Button     string            `json:"button"`
	HitID      string            `json:"hit_id,omitempty"`
	HitKind    string            `json:"hit_kind,omitempty"`
	HitAction  string            `json:"hit_action,omitempty"`
	HitPayload map[string]string `json:"hit_payload,omitempty"`
}

// ThemeChangedParams delivers the complete palette.
type ThemeChangedParams struct {
	Palette Palette `json:"palette"`
	// ANSI maps palette roles (never name) to indices 0..15. It is absent
	// when no role is an ANSI index.
	ANSI ANSIRoles `json:"ansi,omitempty"`
}

// ToastShowParams displays a toast. PluginID is assigned by Husk, never trusted
// from the sender.
type ToastShowParams struct {
	PluginID string `json:"plugin_id,omitempty"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

// SlotContributeParams contributes content to a published extension point,
// not a host surface. See design/message-kinds.md.
type SlotContributeParams struct {
	SlotID  string `json:"slot_id"`
	ID      string `json:"id"`
	Content string `json:"content"`
	OnClick string `json:"on_click,omitempty"`
}

// SlotContribution includes the authenticated contributor identity.
type SlotContribution struct {
	SlotContributeParams
	PluginID string `json:"plugin_id"`
}

// SlotContributionsParams reports a published extension point's contributions.
type SlotContributionsParams struct {
	SlotID        string             `json:"slot_id"`
	Contributions []SlotContribution `json:"contributions"`
}
