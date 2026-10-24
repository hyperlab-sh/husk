package schema

import "testing"

func TestRowHitColumnSchema(t *testing.T) {
	s := compileDef(t, "#/$defs/RowHitTarget")
	for _, tc := range []struct {
		name  string
		cols  map[string]any
		valid bool
	}{
		{"legacy", nil, true},
		{"zero start", map[string]any{"col_start": 0, "col_end": 1}, true},
		{"positive start", map[string]any{"col_start": 3, "col_end": 8}, true},
		{"missing end", map[string]any{"col_start": 0}, false},
		{"missing start", map[string]any{"col_end": 4}, false},
		{"negative start", map[string]any{"col_start": -1, "col_end": 4}, false},
		{"negative end", map[string]any{"col_start": 0, "col_end": -1}, false},
		{"zero end", map[string]any{"col_start": 0, "col_end": 0}, false},
		{"noninteger", map[string]any{"col_start": 0.5, "col_end": 4}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{"line": 1, "id": "row", "kind": "cell", "action": "example.select"}
			for key, value := range tc.cols {
				v[key] = value
			}
			err := s.Validate(wire(t, v))
			if (err == nil) != tc.valid {
				t.Fatalf("schema error = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}
