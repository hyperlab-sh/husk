package protocol

import "encoding/json"

// Code is a closed error vocabulary for this protocol minor version.
type Code string

const (
	// CodeParseError means the line is not JSON or UTF-8.
	CodeParseError Code = "parse_error"
	// CodeInvalidMessage means the JSON is not a valid envelope.
	CodeInvalidMessage Code = "invalid_message"
	// CodeLineTooLong means a line exceeds MaxLineBytes.
	CodeLineTooLong Code = "line_too_long"
	// CodeVersionMismatch means an absent, unparsable or incompatible version.
	CodeVersionMismatch Code = "version_mismatch"
	// CodeUnknownMethod means the receiver has no handler.
	CodeUnknownMethod Code = "unknown_method"
	// CodeInvalidParams means the method's parameters failed validation.
	CodeInvalidParams Code = "invalid_params"
	// CodeNotFound means the named target does not exist.
	CodeNotFound Code = "not_found"
	// CodeDenied means the caller is not authorized.
	CodeDenied Code = "denied"
	// CodeUnavailable means the target cannot currently serve.
	CodeUnavailable Code = "unavailable"
	// CodeBusy means a pending-call cap or rate limit is full.
	CodeBusy Code = "busy"
	// CodeTimeout means the receiver did not reply in time.
	CodeTimeout Code = "timeout"
	// CodeCancelled means a request was dropped on exit or shutdown.
	CodeCancelled Code = "cancelled"
	// CodeConflict means a compare-and-set precondition failed.
	CodeConflict Code = "conflict"
	// CodeFailed means a handler reported an application failure.
	CodeFailed Code = "failed"
	// CodeInternal means a receiver bug or an unrecognized future code.
	CodeInternal Code = "internal"
)

// Valid reports whether c belongs to this protocol minor's vocabulary.
func (c Code) Valid() bool {
	switch c {
	case CodeParseError, CodeInvalidMessage, CodeLineTooLong, CodeVersionMismatch,
		CodeUnknownMethod, CodeInvalidParams, CodeNotFound, CodeDenied,
		CodeUnavailable, CodeBusy, CodeTimeout, CodeCancelled, CodeConflict,
		CodeFailed, CodeInternal:
		return true
	}
	return false
}

// UnmarshalJSON accepts a string code and maps unknown future codes to internal.
// Valid still reports whether an explicitly constructed Code belongs to 2.0.
func (c *Code) UnmarshalJSON(b []byte) error {
	var value string
	if len(b) == 0 || b[0] != '"' {
		return invalid("error code must be a string")
	}
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	decoded := Code(value)
	if !decoded.Valid() {
		decoded = CodeInternal
	}
	*c = decoded
	return nil
}

// MaxProtocolErrorsPerSecond bounds protocol.error notifications per channel.
// The host implements the limiter; the codec does not write responses itself.
const MaxProtocolErrorsPerSecond = 10

// Codes returns the vocabulary in specification order, as a fresh slice.
func Codes() []Code {
	return []Code{CodeParseError, CodeInvalidMessage, CodeLineTooLong,
		CodeVersionMismatch, CodeUnknownMethod, CodeInvalidParams, CodeNotFound,
		CodeDenied, CodeUnavailable, CodeBusy, CodeTimeout, CodeCancelled,
		CodeConflict, CodeFailed, CodeInternal}
}

// Error is a protocol failure. Programs branch on Code, not Message.
type Error struct {
	Code    Code            `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements error for protocol failures.
func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// UnmarshalJSON checks the error object and maps future codes to internal.
func (e *Error) UnmarshalJSON(b []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if fields == nil || fields["code"] == nil || fields["message"] == nil ||
		len(fields["code"]) == 0 || fields["code"][0] != '"' ||
		len(fields["message"]) == 0 || fields["message"][0] != '"' {
		return invalid("error requires string code and message")
	}
	var out Error
	for _, field := range []struct {
		name string
		dest any
	}{
		{"code", &out.Code}, {"message", &out.Message}, {"data", &out.Data},
	} {
		if raw, exists := fields[field.name]; exists {
			if err := json.Unmarshal(raw, field.dest); err != nil {
				return err
			}
		}
	}
	if out.Data != nil && !jsonObject(out.Data) {
		return invalid("error data must be an object")
	}
	if !out.Code.Valid() {
		out.Code = CodeInternal
	}
	*e = out
	return nil
}

// ProtocolErrorParams is the protocol.error notification payload. Peers must
// never answer this notification. Rate limiting and logging belong to the host.
type ProtocolErrorParams struct {
	Code    Code            `json:"code"`
	Message string          `json:"message"`
	Method  string          `json:"method"`
	Data    json.RawMessage `json:"data,omitempty"`
}
