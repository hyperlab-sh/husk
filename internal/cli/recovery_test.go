package cli

import (
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// holdDirLock takes the runtime-directory lock on its own descriptor, as
// another recovering process would.
func holdDirLock(t *testing.T, dir string) {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
}

// A refused connect on an explicit target runs stale recovery, which removes
// the socket as well as the entry.
func TestExplicitStaleTargetLosesEntryAndSocket(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version)
	deadSocket(t, instance.SocketPath(dir, "ghost"))
	h := newHarness(t, dir, nil)
	if got := h.run("ping", "--instance", "ghost", "--format", "table"); got != output.ExitNoInstance {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
	if h.errb.String() != "husk: instance \"ghost\" is not running (stale entry removed)\n" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
	for _, p := range []string{instance.EntryPath(dir, "ghost"), instance.SocketPath(dir, "ghost")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived recovery: %v", p, err)
		}
	}
}

// The re-probe under the lock found the instance up: exit 3, nothing removed,
// and the command is not retried against a socket it did not resolve.
func TestStaleTargetThatAnswersTheReprobeIsNotRemovedOrRetried(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version)
	h := newHarness(t, dir, nil)
	h.env.Deps = &instance.Deps{Ping: func(string) error { return nil }}
	if got := h.run("ping", "--instance", "ghost", "--format", "table"); got != output.ExitNoInstance {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
	if h.errb.String() != "husk: instance \"ghost\" started while this command ran; retry\n" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
	if _, err := os.Lstat(instance.EntryPath(dir, "ghost")); err != nil {
		t.Fatal("entry removed although the instance answered", err)
	}
}

func TestStaleTargetWithTheDirectoryLockedIsExit5AndRemovesNothing(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version)
	h := newHarness(t, dir, nil)
	h.env.Deps = &instance.Deps{LockWait: 100 * time.Millisecond}
	holdDirLock(t, dir)
	if got := h.run("ping", "--instance", "ghost", "--format", "table"); got != output.ExitTransport {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
	if h.errb.String() != "husk: instance \"ghost\" is not answering and the runtime dir is locked; retry\n" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
	if _, err := os.Lstat(instance.EntryPath(dir, "ghost")); err != nil {
		t.Fatal("entry removed without the lock", err)
	}
}

func TestInstancesWithTheDirectoryLockedWarnsAndListsUnresponsive(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version)
	h := newHarness(t, dir, nil)
	h.env.Deps = &instance.Deps{LockWait: 100 * time.Millisecond}
	holdDirLock(t, dir)
	if got := h.run("instances", "--format", "json"); got != 0 {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
	if !strings.Contains(h.out.String(), `"status":"unresponsive"`) {
		t.Fatalf("stdout = %q", h.out.String())
	}
	if !strings.Contains(h.errb.String(), "is locked; instance \\\"ghost\\\" left as is") {
		t.Fatalf("stderr = %q", h.errb.String())
	}
}

func TestInstancesJSONCarriesHostVersion(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "up", instance.SocketPath(dir, "up"), protocol.Version)
	h := newHarness(t, dir, nil)
	h.env.Deps = &instance.Deps{Ping: func(string) error { return nil }}
	if got := h.run("instances", "--format", "json"); got != 0 {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
	if !strings.Contains(h.out.String(), `"host_version":`) {
		t.Fatalf("stdout = %q, want a host_version member", h.out.String())
	}
}

// deadSocket leaves a socket file at path with nothing listening: a crash.
func deadSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
}

// A registered entry whose socket path is a regular file is never unlinked;
// both `instances` and discovery skip it and warn naming the path.
func TestNonSocketEntryIsSkippedWithAWarningNamingThePath(t *testing.T) {
	setup := func(t *testing.T) (string, string) {
		dir := shortDir(t)
		register(t, dir, "odd", instance.SocketPath(dir, "odd"), protocol.Version)
		if err := os.WriteFile(instance.SocketPath(dir, "odd"), []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, instance.SocketPath(dir, "odd") + " is not a socket; instance \"odd\" skipped"
	}
	t.Run("instances", func(t *testing.T) {
		dir, want := setup(t)
		h := newHarness(t, dir, nil)
		if got := h.run("instances", "--format", "json"); got != 0 {
			t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
		}
		if strings.TrimSpace(h.out.String()) != "[]" {
			t.Fatalf("stdout = %q, want no rows", h.out.String())
		}
		if !strings.Contains(h.errb.String(), strings.ReplaceAll(want, `"`, `\"`)) {
			t.Fatalf("stderr = %q", h.errb.String())
		}
		if b, err := os.ReadFile(instance.SocketPath(dir, "odd")); err != nil || string(b) != "precious" {
			t.Fatal("the non-socket must be left alone", err)
		}
	})
	t.Run("discovery", func(t *testing.T) {
		dir, want := setup(t)
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--format", "table"); got != 3 {
			t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
		}
		if !strings.Contains(h.errb.String(), want) {
			t.Fatalf("stderr = %q, want %q", h.errb.String(), want)
		}
	})
}

// A removal failure while recovering a stale entry (EACCES here) is warned
// about naming the path, in both `instances` and discovery; the entry stays
// and the stuck instance is never listed.
func TestRemovalFailureIsWarnedAboutNamingThePath(t *testing.T) {
	setup := func(t *testing.T) (*harness, string, string) {
		dir := shortDir(t)
		sock := instance.SocketPath(dir, "stuck")
		entry := instance.EntryPath(dir, "stuck")
		register(t, dir, "stuck", sock, protocol.Version)
		deadSocket(t, sock)
		h := newHarness(t, dir, nil)
		h.env.Deps = &instance.Deps{Remove: func(path string) error {
			if path == entry {
				return &fs.PathError{Op: "remove", Path: path, Err: syscall.EACCES}
			}
			return os.Remove(path)
		}}
		return h, dir, "cannot remove " + entry + ": permission denied; instance \"stuck\" skipped"
	}
	stays := func(t *testing.T, dir string) {
		if _, err := os.Lstat(instance.EntryPath(dir, "stuck")); err != nil {
			t.Fatalf("the entry must stay: %v", err)
		}
	}
	t.Run("instances", func(t *testing.T) {
		h, dir, want := setup(t)
		if got := h.run("instances", "--format", "json"); got != 0 {
			t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
		}
		if strings.TrimSpace(h.out.String()) != "[]" {
			t.Fatalf("stdout = %q, want no rows", h.out.String())
		}
		if !strings.Contains(h.errb.String(), strings.ReplaceAll(want, `"`, `\"`)) {
			t.Fatalf("stderr = %q, want %q", h.errb.String(), want)
		}
		stays(t, dir)
	})
	t.Run("discovery", func(t *testing.T) {
		h, dir, want := setup(t)
		if got := h.run("ping", "--format", "table"); got != 3 {
			t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
		}
		if !strings.Contains(h.errb.String(), want) {
			t.Fatalf("stderr = %q, want %q", h.errb.String(), want)
		}
		stays(t, dir)
	})
}
