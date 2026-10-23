package cli

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSettingsSetParsesJSONOtherwiseString(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"evening", `"evening"`},
		{"true", `true`},
		{`"true"`, `"true"`},
		{"42", `42`},
		{"9223372036854775807", `9223372036854775807`},
		{`["one","two"]`, `["one","two"]`},
		{`{"key":true}`, `{"key":true}`},
		{`null`, `null`},
		{`not valid {`, `"not valid {"`},
	} {
		t.Run(tc.text, func(t *testing.T) {
			srv, h := serviceServer(t, serviceReports, nil)
			if code := h.run("settings", "set", "dist.value", tc.text, "--format", "json"); code != 0 {
				t.Fatalf("exit %d: %s", code, h.errb.String())
			}
			req := lastRequest(t, srv)
			var p struct {
				Key   string
				Value json.RawMessage
			}
			if req.Method != protocol.MethodSettingsSet || json.Unmarshal(req.Params, &p) != nil || p.Key != "dist.value" || string(p.Value) != tc.want {
				t.Fatalf("method=%s params=%s", req.Method, req.Params)
			}
		})
	}
}
func TestThemeSetAlwaysSendsString(t *testing.T) {
	for _, name := range []string{"true", "42", "null", "evening"} {
		t.Run(name, func(t *testing.T) {
			srv, h := serviceServer(t, serviceReports, nil)
			if code := h.run("theme", "set", name); code != 0 {
				t.Fatalf("exit %d", code)
			}
			var p struct{ Key, Value string }
			req := lastRequest(t, srv)
			if req.Method != protocol.MethodSettingsSet || json.Unmarshal(req.Params, &p) != nil || p.Key != "theme.name" || p.Value != name {
				t.Fatalf("params=%s", req.Params)
			}
		})
	}
}
