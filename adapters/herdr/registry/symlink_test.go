package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCollisionScopeResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, registered, converted string }{{"registry_alias", alias, real}, {"converted_alias", real, alias}} {
		t.Run(tc.name, func(t *testing.T) {
			records := []Record{{ID: "sample:tool", Root: tc.registered, Enabled: true}, {ID: "sample-tool", Root: filepath.Join(root, "other"), Enabled: true}}
			if got := IDsFor(records, tc.converted); !reflect.DeepEqual(got, []string{"sample:tool", "sample-tool"}) {
				t.Fatalf("symlink lost collision scope: %v", got)
			}
		})
	}
}
