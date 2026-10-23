package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginDirLoadForms(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "alpha")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, FileName)
	if err := os.WriteFile(file, []byte(`{"manifest_version":2,"protocol":2,"id":"alpha","name":"Alpha","version":"1","command":["./plugin"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "bare")
	if err := os.WriteFile(exe, []byte("executable fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	for _, tc := range []struct {
		name, arg, want string
		scan            bool
	}{
		{"manifest directory", dir, dir, false},
		{"manifest file", file, dir, false},
		{"bare executable", exe, root, false},
		{"implicit command line", exe + " --flag", "", false},
		{"plugin directory scan", root, dir, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var spec *Spec
			var err error
			if tc.scan {
				var results []Result
				results, err = r.LoadDir(tc.arg)
				if err == nil && len(results) == 1 {
					spec, err = results[0].Spec, results[0].Err
				}
			} else {
				spec, _, err = r.Load(tc.arg)
			}
			if err != nil || spec == nil {
				t.Fatalf("load: %v", err)
			}
			if spec.Dir != tc.want || spec.WorkDir == "" {
				t.Fatalf("Dir=%q want=%q cwd=%q", spec.Dir, tc.want, spec.WorkDir)
			}
			if spec.Clone().Dir != tc.want {
				t.Fatal("clone lost directory")
			}
		})
	}
}
