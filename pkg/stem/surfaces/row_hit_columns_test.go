package surfaces

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSurfaceUpdateRowHitColumnValidation(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("example")
	pods.claim("example", string(protocol.SurfaceMainStage))
	settle(st, eng)

	for _, tc := range []struct {
		name string
		hit  string
		want protocol.Code
	}{
		{"legacy", `{}`, ""},
		{"zero start", `{"col_start":0,"col_end":1}`, ""},
		{"positive start", `{"col_start":3,"col_end":8}`, ""},
		{"missing end", `{"col_start":0}`, protocol.CodeInvalidParams},
		{"missing start", `{"col_end":4}`, protocol.CodeInvalidParams},
		{"negative start", `{"col_start":-1,"col_end":4}`, protocol.CodeInvalidParams},
		{"negative end", `{"col_start":0,"col_end":-1}`, protocol.CodeInvalidParams},
		{"empty range", `{"col_start":0,"col_end":0}`, protocol.CodeInvalidParams},
		{"equal positive bounds", `{"col_start":4,"col_end":4}`, protocol.CodeInvalidParams},
		{"reversed range", `{"col_start":5,"col_end":2}`, protocol.CodeInvalidParams},
		{"invalid later target", `{}, {"col_start":5,"col_end":2}`, protocol.CodeInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p protocol.SurfaceUpdateParams
			if err := json.Unmarshal([]byte(`{"surface":"main-stage","lines":["row"],"capture":true,"hit_targets":[`+tc.hit+`]}`), &p); err != nil {
				t.Fatal(err)
			}
			// Clear capture so a rejected update must not turn it back on.
			if err := eng.SurfaceUpdate("example", protocol.SurfaceUpdateParams{Surface: p.Surface, Capture: protocol.CaptureBool(false)}); err != nil {
				t.Fatal(err)
			}
			var got protocol.Code
			if err := eng.SurfaceUpdate("example", p); err != nil {
				got = codeOf(t, err)
			}
			if got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
			if tc.want != "" && resolution(t, st).Capture[p.Surface] != protocol.CaptureNone {
				t.Fatal("invalid hit range changed capture")
			}
		})
	}
}
