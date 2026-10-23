package protocol

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCaptureRejectsOtherJSONValues(t *testing.T) {
	for _, raw := range []string{`"true"`, `"false"`, `null`, `0`, `{}`, `[]`, `"none"`} {
		t.Run(raw, func(t *testing.T) {
			var value CaptureValue
			if err := json.Unmarshal([]byte(raw), &value); err == nil {
				t.Fatalf("accepted capture %s", raw)
			}
		})
	}
}

func TestTerminalPayloadValidation(t *testing.T) {
	negative, limit, over := -1, 20000, 20001
	for _, tc := range []struct {
		name  string
		p     TerminalAttachParams
		valid bool
	}{
		{"default", TerminalAttachParams{Surface: SurfaceMainStage}, true},
		{"text", TerminalAttachParams{Surface: SurfaceMainStage, Capture: CaptureText}, true},
		{"all", TerminalAttachParams{Surface: SurfaceMainStage, Capture: CaptureAll}, true},
		{"invalid-surface", TerminalAttachParams{Surface: "missing"}, false},
		{"invalid-capture", TerminalAttachParams{Surface: SurfaceMainStage, Capture: "unknown"}, false},
		{"negative-history", TerminalAttachParams{Surface: SurfaceMainStage, Scrollback: &negative}, false},
		{"history-limit", TerminalAttachParams{Surface: SurfaceMainStage, Scrollback: &limit}, true},
		{"history-over-limit", TerminalAttachParams{Surface: SurfaceMainStage, Scrollback: &over}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); (err == nil) != tc.valid {
				t.Fatalf("validation %v, valid %v", err, tc.valid)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		p      TerminalWriteParams
		valid  bool
		length int
	}{
		{"empty", TerminalWriteParams{Surface: SurfaceMainStage}, true, 0},
		{"maximum", TerminalWriteParams{Surface: SurfaceMainStage, Data: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", MaxTerminalBytes)))}, true, MaxTerminalBytes},
		{"oversize", TerminalWriteParams{Surface: SurfaceMainStage, Data: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", MaxTerminalBytes+1)))}, false, 0},
		{"bad-base64", TerminalWriteParams{Surface: SurfaceMainStage, Data: "!"}, false, 0},
		{"bad-surface", TerminalWriteParams{Surface: "missing", Data: "eA=="}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.p.Decode()
			if (err == nil) != tc.valid || len(b) != tc.length {
				t.Fatalf("decode length %d, error %v", len(b), err)
			}
		})
	}
}
