package manifest

import (
	"encoding/json"
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

// HookDecl declares a plugin-owned hook point.
type HookDecl struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// HookRegistration subscribes this plugin to a hook point.
type HookRegistration struct {
	ID string `json:"id"`
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
}

// Suggestion is an ignored legacy shortcut, never a routing declaration.
type Suggestion struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

// FocusKeymap is the normalized declared focus-keymap document.
type FocusKeymap struct {
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
	for _, key := range []string{"protocol", "command", "work_dir", "binding_problems", "suggestions"} {
		delete(doc, key)
	}
	return json.Marshal(doc)
}

// FocusKeymapJSON returns version 1, an always-present bind array and diagnostics.
func (s Spec) FocusKeymapJSON() ([]byte, error) {
	bind := s.FocusBindings
	if bind == nil {
		bind = []FocusBinding{}
	}
	return json.Marshal(FocusKeymap{Version: 1, Bind: bind, Problems: s.BindingProblems, Suggestions: s.Suggestions})
}
