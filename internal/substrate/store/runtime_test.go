package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/internal/instance"
)

func TestPublicOpenAndInstanceSocketDelegation(t *testing.T) {
	e := fixture(t)
	opts := e.opts("notes", true)
	called := false
	opts.Env.RuntimeDir = func() (string, error) { called = true; p := filepath.Join(e.root, "runtime"); return p, privateDir(p) }
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if !called || s.RuntimeDir() != filepath.Join(e.root, "runtime") || s.SocketPath() != instance.SocketPath(s.RuntimeDir(), "notes") {
		t.Fatal("runtime/socket delegation failed")
	}
}
func TestPublicOpenRejectsRuntimeFailureAndUnsafeDirectory(t *testing.T) {
	e := fixture(t)
	opts := e.opts("notes", true)
	want := errors.New("runtime refused")
	opts.Env.RuntimeDir = func() (string, error) { return "", want }
	if _, err := Open(opts); !errors.Is(err, want) {
		t.Fatal("runtime error lost", err)
	}
	if err := os.Chmod(e.root, 0755); err != nil {
		t.Fatal(err)
	}
	opts.Env.RuntimeDir = func() (string, error) { return e.root, nil } // Explicitly non-private, independent of the process umask.
	if _, err := Open(opts); err == nil {
		t.Fatal("unsafe runtime accepted")
	}
}
