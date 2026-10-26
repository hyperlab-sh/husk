package plugins_test

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
)

// Every bundled plugin must load together under --plugin-dir ./plugins, so no
// two bundled manifests may declare the same surface: the second claim would
// be refused at run time.
func TestBundledManifestsClaimDistinctSurfaces(t *testing.T) {
	results, err := manifest.NewRegistry().LoadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no bundled manifests found")
	}
	claimed := map[string]string{}
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("%s: %v", res.Dir, res.Err)
			continue
		}
		for _, s := range res.Spec.Surfaces {
			id := string(s.ID)
			if other, dup := claimed[id]; dup {
				t.Errorf("%s claimed by %s and %s", id, other, res.Spec.ID)
				continue
			}
			claimed[id] = res.Spec.ID
		}
	}
}
