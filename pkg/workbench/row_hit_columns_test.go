package workbench

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPublisherMouseColumnRanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		hits string
		x, y int
		want string
	}{
		{"first range start", `[{"line":1,"id":"first","col_start":0,"col_end":4},{"line":1,"id":"second","col_start":4,"col_end":8}]`, 0, 1, "first"},
		{"second range start", `[{"line":1,"id":"first","col_start":0,"col_end":4},{"line":1,"id":"second","col_start":4,"col_end":8}]`, 4, 1, "second"},
		{"second range inside", `[{"line":1,"id":"first","col_start":0,"col_end":4},{"line":1,"id":"second","col_start":4,"col_end":8}]`, 7, 1, "second"},
		{"end excluded", `[{"line":1,"id":"range","col_start":2,"col_end":5}]`, 5, 1, ""},
		{"before start", `[{"line":1,"id":"range","col_start":2,"col_end":5}]`, 1, 1, ""},
		{"start included", `[{"line":1,"id":"range","col_start":2,"col_end":5}]`, 2, 1, "range"},
		{"wrong row", `[{"line":1,"id":"range","col_start":2,"col_end":5}]`, 2, 2, ""},
		{"range before fallback", `[{"line":1,"id":"range","col_start":2,"col_end":5},{"line":1,"id":"fallback"}]`, 3, 1, "range"},
		{"fallback outside range", `[{"line":1,"id":"range","col_start":2,"col_end":5},{"line":1,"id":"fallback"}]`, 5, 1, "fallback"},
		{"overlap keeps declaration order", `[{"line":1,"id":"first","col_start":2,"col_end":6},{"line":1,"id":"second","col_start":3,"col_end":8}]`, 4, 1, "first"},
		{"legacy whole row", `[{"line":1,"id":"legacy"}]`, 40, 1, "legacy"},
		{"whole row before range", `[{"line":1,"id":"legacy"},{"line":1,"id":"range","col_start":2,"col_end":5}]`, 3, 1, "legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits []protocol.RowHitTarget
			if err := json.Unmarshal([]byte(tc.hits), &hits); err != nil {
				t.Fatal(err)
			}
			for i := range hits {
				hits[i].Kind = "cell"
				hits[i].Action = "example.select"
				hits[i].Payload = map[string]string{"target": hits[i].ID}
			}
			m := newWorkbenchModel()
			m.slotUpdates[protocol.SurfaceMainStage] = &protocol.SurfaceUpdateParams{HitTargets: hits}
			got := m.publisherMousePayload(protocol.SurfaceMainStage, tc.x, tc.y, "left")
			want := protocol.SurfaceMouseParams{Surface: protocol.SurfaceMainStage, X: tc.x, Y: tc.y, Button: "left"}
			if tc.want != "" {
				want.HitID, want.HitKind, want.HitAction = tc.want, "cell", "example.select"
				want.HitPayload = map[string]string{"target": tc.want}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("mouse payload = %+v, want %+v", got, want)
			}
		})
	}
}
