package instance

import (
	"encoding/json"
	"os"
	"testing"
)

// writeEntryFile writes e directly to its entry path, bypassing Register:
// fixtures for List, Survey and Resolve tests.
func writeEntryFile(dir string, e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(EntryPath(dir, e.ID), data, 0o600)
}

// assertPrivateDir fails unless dir exists as a 0700 directory.
func assertPrivateDir(t *testing.T, dir string) {
	t.Helper()
	st, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat %s: %v", dir, err)
	}
	if !st.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if got := st.Mode().Perm(); got != 0o700 {
		t.Fatalf("%s mode is %o, want 700", dir, got)
	}
}

// readJSONFields decodes a JSON object file into its top-level fields.
func readJSONFields(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// runDir returns a private 0700 runtime directory for Register fixtures:
// a fresh subdirectory of a per-test temp dir. t.TempDir() itself is
// group/world-traversable (0755) on this platform, which the runtime-dir
// rule rightly refuses.
func runDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir() + "/run"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
