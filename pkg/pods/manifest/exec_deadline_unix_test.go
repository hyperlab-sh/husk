//go:build linux || darwin

package manifest

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
)

func TestExecConverterRealProcessDeadline(t *testing.T) {
	clock := testclock.New()
	converter := execFixture(t, ExecOptions{Clock: clock})
	dir := filepath.Join(t.TempDir(), "real-deadline")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Open both ends so opening does not wait for the child. Its byte proves the
	// real converter reached its blocking work before simulated time advances.
	ready, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	started := make(chan error, 1)
	go func() { var b [1]byte; _, err := io.ReadFull(ready, b[:]); started <- err }()
	done := make(chan error, 1)
	go func() { _, _, err := converter.Convert(Source{Path: dir, Dir: dir}); done <- err }()
	if err := testclock.Receive(t, started); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if err := testclock.Receive(t, done); err == nil || !strings.Contains(err.Error(), "deadline exceeded after 5s") {
		t.Fatalf("real process deadline=%v", err)
	}
}
