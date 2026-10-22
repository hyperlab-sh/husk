// Package protocol defines Husk's version 2 JSON-lines wire format. It imports
// only the standard library; transports and request correlation live elsewhere.
//
//go:generate go run github.com/hyperlab-sh/husk/cmd/genschema
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// ID correlates a request and reply. Its original JSON spelling is retained.
// Use String as an opaque map key, not as the decoded string value.
type ID struct{ raw json.RawMessage }

// NewStringID constructs a string id.
func NewStringID(s string) ID {
	b, _ := json.Marshal(s)
	return ID{raw: b}
}

// NewIntID constructs an integer id, rejecting values outside |n| < 2^53.
func NewIntID(n int64) (ID, error) {
	var id ID
	err := id.UnmarshalJSON([]byte(strconv.FormatInt(n, 10)))
	return id, err
}

// String returns the original JSON representation, suitable as a correlation key.
func (id ID) String() string { return string(id.raw) }

// MarshalJSON returns the retained JSON representation. A zero ID is invalid.
func (id ID) MarshalJSON() ([]byte, error) {
	if len(id.raw) == 0 {
		return nil, invalid("id is unset")
	}
	return bytes.Clone(id.raw), nil
}

// UnmarshalJSON accepts only a string or an integer with |n| < 2^53.
func (id *ID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if !utf8.Valid(b) || !json.Valid(b) || len(b) == 0 {
		return invalid("id must be a JSON string or integer")
	}
	if b[0] != '"' {
		n, err := strconv.ParseInt(string(b), 10, 64)
		if err != nil || n <= -(1<<53) || n >= 1<<53 {
			return invalid("id must be a string or integer with |n| < 2^53")
		}
	}
	id.raw = bytes.Clone(b)
	return nil
}

// Message is a request, notification, or reply. A nil Result is absent;
// json.RawMessage("null") is a present, successful result.
type Message struct {
	ID           *ID             `json:"id,omitempty"`
	Method       string          `json:"method,omitempty"`
	Params       json.RawMessage `json:"params,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        *Error          `json:"error,omitempty"`
	Token        string          `json:"token,omitempty"`
	tokenPresent bool
}

// HasToken distinguishes absent credentials from a decoded explicit empty token.
// Programmatically constructed messages with a nonempty Token also have one.
func (m Message) HasToken() bool { return m.tokenPresent || m.Token != "" }

// Kind identifies one of the three message forms.
type Kind int

const (
	// KindRequest expects a correlated reply.
	KindRequest Kind = iota + 1
	// KindNotification does not expect a reply.
	KindNotification
	// KindReply completes a request with a result or error.
	KindReply
)

// Kind classifies m, returning CodeInvalidMessage for an invalid envelope.
// Parameter semantics are checked separately by Validate.
func (m Message) Kind() (Kind, error) {
	if m.ID != nil && len(m.ID.raw) == 0 {
		return 0, invalid("id is unset")
	}
	if m.Method != "" {
		if err := ValidateMethod(m.Method); err != nil {
			return 0, err
		}
		if m.Result != nil || m.Error != nil {
			return 0, invalid("a request or notification cannot carry result or error")
		}
		if m.ID != nil {
			return KindRequest, nil
		}
		return KindNotification, nil
	}
	if m.ID == nil || m.Params != nil || m.HasToken() || (m.Result == nil) == (m.Error == nil) {
		return 0, invalid("a reply requires id and exactly one of result or error, without params")
	}
	return KindReply, nil
}

// Validate checks the envelope and the object shape of params and error.data.
// It does not validate method-specific payloads.
func (m Message) Validate() error {
	if _, err := m.Kind(); err != nil {
		return err
	}
	if m.Params != nil && !jsonObject(m.Params) {
		return &Error{Code: CodeInvalidParams, Message: "params must be an object"}
	}
	if m.Result != nil && !json.Valid(m.Result) {
		return invalid("result is not valid JSON")
	}
	if m.Error != nil {
		if !m.Error.Code.Valid() {
			return invalid("error code is not defined")
		}
		if m.Error.Data != nil && !jsonObject(m.Error.Data) {
			return invalid("error data must be an object")
		}
	}
	return nil
}

// UnmarshalJSON decodes and validates a message, retaining field presence.
// Unknown fields are ignored. Explicit null is not an absent id or error.
func (m *Message) UnmarshalJSON(b []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if fields == nil {
		return invalid("message must be an object")
	}
	for _, name := range []string{"id", "method", "error", "token"} {
		if raw, ok := fields[name]; ok && bytes.Equal(raw, []byte("null")) {
			return invalid(fmt.Sprintf("%s cannot be null", name))
		}
	}
	// Decode exact wire names only. Decoding the entire struct would also
	// accept case-insensitive aliases, turning unknown members into fields.
	var candidate Message
	for _, field := range []struct {
		name string
		dest any
	}{
		{"id", &candidate.ID}, {"method", &candidate.Method},
		{"params", &candidate.Params}, {"result", &candidate.Result},
		{"error", &candidate.Error}, {"token", &candidate.Token},
	} {
		if raw, exists := fields[field.name]; exists {
			if err := json.Unmarshal(raw, field.dest); err != nil {
				return err
			}
		}
	}
	_, candidate.tokenPresent = fields["token"]
	if _, exists := fields["method"]; exists && candidate.Method == "" {
		return invalid("method cannot be empty")
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*m = candidate
	return nil
}

func invalid(message string) *Error { return &Error{Code: CodeInvalidMessage, Message: message} }

func jsonObject(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}
