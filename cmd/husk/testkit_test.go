package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/instance"
)

// shortRuntimeDir gives launch-configuration tests a private socket directory.
func shortRuntimeDir(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "r")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if instance.CheckSocketPath(instance.SocketPath(dir, id)) == nil {
		return dir
	}
	dir, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
