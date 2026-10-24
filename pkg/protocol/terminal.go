package protocol

import (
	"encoding/base64"
	"encoding/json"
)

// CaptureMode is the normalized key-capture policy stored by the host.
type CaptureMode string

const (
	// CaptureNone leaves host bindings active.
	CaptureNone CaptureMode = ""
	// CaptureText keeps unmodified text keys for the occupant.
	CaptureText CaptureMode = "text"
	// CaptureAll keeps all keys except release for the occupant.
	CaptureAll CaptureMode = "all"
)

// CaptureValue preserves the four accepted JSON spellings of capture.
type CaptureValue string

const (
	// CaptureFalse disables capture (the original boolean spelling).
	CaptureFalse CaptureValue = "false"
	// CaptureTrue enables text capture (the original boolean spelling).
	CaptureTrue CaptureValue = "true"
	// CaptureValueText enables text capture.
	CaptureValueText CaptureValue = "text"
	// CaptureValueAll enables full capture.
	CaptureValueAll CaptureValue = "all"
)

// Ptr returns an optional capture value for a wire parameter.
func (c CaptureValue) Ptr() *CaptureValue { return &c }

// CaptureBool migrates an optional boolean capture declaration to the wire type.
func CaptureBool(on bool) *CaptureValue {
	if on {
		return CaptureTrue.Ptr()
	}
	return CaptureFalse.Ptr()
}

// Valid reports whether c is one of the wire spellings.
func (c CaptureValue) Valid() bool {
	return c == CaptureFalse || c == CaptureTrue || c == CaptureValueText || c == CaptureValueAll
}

// Mode returns the normalized policy.
func (c CaptureValue) Mode() CaptureMode {
	switch c {
	case CaptureTrue, CaptureValueText:
		return CaptureText
	case CaptureValueAll:
		return CaptureAll
	}
	return CaptureNone
}

// MarshalJSON retains boolean compatibility without widening strings.
func (c CaptureValue) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, invalid("capture must be true, false, text or all")
	}
	if c == CaptureTrue || c == CaptureFalse {
		return []byte(c), nil
	}
	return json.Marshal(string(c))
}

// UnmarshalJSON accepts exactly the four specified values.
func (c *CaptureValue) UnmarshalJSON(b []byte) error {
	var value CaptureValue
	if string(b) == "true" || string(b) == "false" {
		value = CaptureValue(b)
	} else {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return invalid("capture must be true, false, text or all")
		}
		if s != "text" && s != "all" {
			return invalid("capture string must be text or all")
		}
		value = CaptureValue(s)
	}
	if !value.Valid() {
		return invalid("capture must be true, false, text or all")
	}
	*c = value
	return nil
}

// MaxTerminalBytes bounds one decoded terminal byte chunk.
const MaxTerminalBytes = 64 * 1024

// TerminalAttachParams attaches an emulator to an occupied surface.
type TerminalAttachParams struct {
	Surface    SurfaceID     `json:"surface"`
	Capture    *CaptureValue `json:"capture,omitempty"`
	Scrollback *int          `json:"scrollback,omitempty"`
}

// Validate checks the attachment's limits and capture policy.
func (p TerminalAttachParams) Validate() error {
	if !p.Surface.Valid() {
		return invalid("terminal.attach requires a valid surface")
	}
	if p.Capture != nil && !p.Capture.Valid() {
		return invalid("capture must be true, false, text or all")
	}
	if p.Scrollback != nil && (*p.Scrollback < 0 || *p.Scrollback > 20000) {
		return invalid("scrollback must be between 0 and 20000")
	}
	return nil
}

// TerminalAttachResult is the emulator's inner size in cells.
type TerminalAttachResult struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// TerminalWriteParams supplies base64 terminal output to an attached surface.
type TerminalWriteParams struct {
	Surface SurfaceID `json:"surface"`
	Data    string    `json:"data"`
}

// Decode validates and decodes a bounded terminal chunk.
func (p TerminalWriteParams) Decode() ([]byte, error) {
	if !p.Surface.Valid() || len(p.Data) > base64.StdEncoding.EncodedLen(MaxTerminalBytes) {
		return nil, invalid("invalid terminal byte chunk")
	}
	b, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil || len(b) > MaxTerminalBytes {
		return nil, invalid("terminal data must be base64 and at most 65536 decoded bytes")
	}
	return b, nil
}

// TerminalDetachParams returns a surface to empty line rendering.
type TerminalDetachParams struct {
	Surface SurfaceID `json:"surface"`
}

// TerminalInputParams carries encoded input and emulator query replies.
type TerminalInputParams struct {
	Surface SurfaceID `json:"surface"`
	Data    string    `json:"data"`
}
