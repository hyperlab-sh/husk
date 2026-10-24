package protocol

import (
	"encoding/json"
	"testing"
)

func TestRowHitColumnWireRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		cols string
	}{
		{"legacy", ""},
		{"zero start", `,"col_start":0,"col_end":1`},
		{"positive start", `,"col_start":3,"col_end":8`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"line":1,"id":"row","kind":"cell","action":"example.select"` + tc.cols + `}`
			var hit RowHitTarget
			if err := json.Unmarshal([]byte(raw), &hit); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(hit)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) || got["col_start"] != want["col_start"] || got["col_end"] != want["col_end"] {
				t.Fatalf("column fields did not round trip: got %s, want %s", encoded, raw)
			}
		})
	}
}
