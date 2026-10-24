package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV2VendorObject(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"object", `,"x-fixture":{"nested":[{"value":1}]}`, true},
		{"null", `,"x-fixture":null`, false},
		{"array", `,"x-fixture":[]`, false},
		{"scalar", `,"x-fixture":"value"`, false},
		{"multiple", `,"x-first":{},"x-second":{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := []byte(`{"manifest_version":2,"protocol":2,"id":"vendor","name":"Vendor","version":"1","command":["./plugin"]` + tc.fields + `}`)
			if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0600); err != nil {
				t.Fatal(err)
			}
			spec, _, err := NewRegistry().LoadPath(dir)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			catalog, err := spec.CatalogJSON()
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(catalog, &doc); err != nil || string(doc["x-fixture"]) != `{"nested":[{"value":1}]}` {
				t.Fatalf("catalog=%s err=%v", catalog, err)
			}
		})
	}
}
