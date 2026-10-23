package protocol

import (
	"encoding/json"
	"testing"
)

func TestProblemWireShape(t *testing.T) {
	for _, tc := range []struct {
		in   Problem
		want string
	}{
		{Problem{Severity: "error", Code: "invalid_value", Layer: "user", Key: "a.b", Message: "m", Source: &Source{Layer: "user", File: "config.json", Line: 3}},
			`{"severity":"error","code":"invalid_value","layer":"user","key":"a.b","message":"m","source":{"layer":"user","file":"config.json","line":3}}`},
		{Problem{Severity: "warning", Code: "unknown_key", Message: "m"},
			`{"severity":"warning","code":"unknown_key","layer":"","message":"m"}`},
		{Problem{Severity: "warning", Code: "duplicate", Layer: "plugin:n", Message: "m", Source: &Source{Layer: "focus-local", Plugin: "n", Order: 2}},
			`{"severity":"warning","code":"duplicate","layer":"plugin:n","message":"m","source":{"layer":"focus-local","plugin":"n","order":2}}`},
	} {
		b, err := json.Marshal(tc.in)
		if err != nil || string(b) != tc.want {
			t.Errorf("got %s, %v\nwant %s", b, err, tc.want)
		}
	}
}
