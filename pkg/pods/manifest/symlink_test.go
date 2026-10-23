package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDirFollowsSymlinkedPluginAndKeepsEscapeBoundary(t *testing.T) {
	for _, state := range []string{"directory", "file", "dangling", "manifest escapes"} {
		t.Run(state, func(t *testing.T) {
			root, target := t.TempDir(), t.TempDir()
			raw := `{"manifest_version":2,"protocol":2,"id":"example","name":"Example","version":"1","command":["./plugin"]}`
			manifest := filepath.Join(target, FileName)
			if state == "manifest escapes" {
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(outside, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, manifest); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(manifest, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			dest := target
			if state == "file" {
				dest = manifest
			}
			if state == "dangling" {
				dest = filepath.Join(target, "missing")
			}
			if err := os.Symlink(dest, filepath.Join(root, "example")); err != nil {
				t.Fatal(err)
			}
			results, err := NewRegistry().LoadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if state == "file" || state == "dangling" {
				if len(results) != 0 {
					t.Fatalf("non-directory loaded: %+v", results)
				}
				return
			}
			if len(results) != 1 {
				t.Fatalf("results=%+v", results)
			}
			result := results[0]
			if state == "manifest escapes" {
				if result.Err == nil || !strings.Contains(result.Err.Error(), "manifest escapes its plugin directory") {
					t.Fatalf("escape accepted: %+v", result)
				}
				return
			}
			if result.Err != nil || result.Spec == nil || result.Spec.ID != "example" || result.Spec.Command[0] != filepath.Join(root, "example", "plugin") {
				t.Fatalf("directory symlink: %+v", result)
			}
		})
	}
}
