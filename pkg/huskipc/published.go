package huskipc

// PublishedRect is relative to the publisher's surface content (zero-based cells).
// Surface is a slot held by the publisher, e.g. main-stage.
type PublishedRect struct {
	Surface SlotID `json:"surface"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// SlotDeclarePayload publishes an extension point, not a new host layout pane.
type SlotDeclarePayload struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Kind        string         `json:"kind"` // inline | region
	Rect        *PublishedRect `json:"rect,omitempty"`
}

type SlotContributePayload struct {
	SlotID  string `json:"slot_id"`
	ID      string `json:"id"`
	Content string `json:"content"`
	OnClick string `json:"on_click,omitempty"`
}

// SlotContribution includes the authenticated contributor identity.
type SlotContribution struct {
	SlotContributePayload
	PluginID string `json:"plugin_id"`
}

type SlotContributionsPayload struct {
	SlotID        string             `json:"slot_id"`
	Contributions []SlotContribution `json:"contributions"`
}

type HookDeclarePayload struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type HookSubscribePayload struct {
	ID string `json:"id"`
}

type HookCallPayload struct {
	ID        string         `json:"id"`
	RequestID string         `json:"request_id"`
	Data      map[string]any `json:"data,omitempty"`
}

// Results stream asynchronously, one per subscriber. PluginID is set by Husk.
type HookResultPayload struct {
	ID        string         `json:"id"`
	RequestID string         `json:"request_id"`
	PluginID  string         `json:"plugin_id,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type EventDeclarePayload struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type EventSubscribePayload struct {
	ID string `json:"id"`
}

// ProtocolErrorPayload rejects an invalid operation without dropping the connection.
type ProtocolErrorPayload struct {
	MessageType string `json:"message_type"`
	Message     string `json:"message"`
}
