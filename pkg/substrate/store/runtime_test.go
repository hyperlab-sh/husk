package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/instance"
)

func TestOpenTakesRuntimeDirFromItsOpener(t *testing.T) {
	e := fixture(t)
	opts := e.opts("notes")
	opts.RuntimeDir = filepath.Join(e.root, "runtime")
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if s.RuntimeDir() != opts.RuntimeDir || s.SocketPath() != instance.SocketPath(opts.RuntimeDir, "notes") {
		t.Fatal("runtime/socket delegation failed", s.RuntimeDir(), s.SocketPath())
	}
}

// Store resolves nothing itself: a missing runtime directory is the opener's
// error, and Store creates none.
func TestOpenRejectsMissingAndUnsafeRuntimeDirectory(t *testing.T) {
	e := fixture(t)
	opts := e.opts("notes")
	opts.RuntimeDir = filepath.Join(e.root, "absent")
	if _, err := Open(opts); err == nil {
		t.Fatal("missing runtime dir accepted")
	}
	if _, err := os.Lstat(opts.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("Store created the runtime dir", err)
	}
	if err := os.Chmod(e.root, 0755); err != nil {
		t.Fatal(err)
	}
	opts.RuntimeDir = e.root // Explicitly non-private, independent of the process umask.
	if _, err := Open(opts); err == nil {
		t.Fatal("unsafe runtime accepted")
	}
}

// An empty RuntimeDir means no instance is registered: Open succeeds, nothing
// is created, SocketPath is empty and log GC protects only the caller's id.
func TestOpenWithoutRuntimeDirMeansNoInstanceRegistered(t *testing.T) {
	e := fixture(t)
	opts := e.opts("notes")
	opts.RuntimeDir = ""
	pings := 0
	opts.InstanceDeps = &instance.Deps{Ping: func(string) error { pings++; return nil }}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if s.RuntimeDir() != "" || s.SocketPath() != "" {
		t.Fatal("expected no runtime paths", s.RuntimeDir(), s.SocketPath())
	}
	logs := filepath.Join(s.Paths().State, "logs")
	for _, id := range []string{"notes", "other"} {
		if err := os.MkdirAll(filepath.Join(logs, id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	report := &GCReport{}
	s.collectLogs(context.Background(), report)
	if pings != 0 || len(report.Problems) != 0 {
		t.Fatal("no runtime dir must mean no survey", pings, report.Problems)
	}
}

func TestInstanceRefNamedIsDerivedFromTheID(t *testing.T) {
	for id, want := range map[string]bool{"notes": true, "a-1": true, "4242": false, "": false} {
		if got := (InstanceRef{ID: id}).Named(); got != want {
			t.Errorf("Named(%q) = %v, want %v", id, got, want)
		}
	}
}
