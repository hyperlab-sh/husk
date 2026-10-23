//go:build linux || darwin

package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/instance"
)

// deadSocketFor leaves a refused socket file at s's path: a crashed run.
func deadSocketFor(t *testing.T, s *SocketServer) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
}

func present(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestClaimRecoversAStaleEntryAndSocket(t *testing.T) {
	s := inertServer(t)
	stale := instance.Entry{ID: "test", PID: 1, Socket: s.SocketPath(), StartedAt: time.Now().Add(-time.Hour).UTC(), ProtocolVersion: "2.0"}
	if err := instance.Register(s.cfg.RuntimeDir, stale); err != nil {
		t.Fatal(err)
	}
	deadSocketFor(t, s)
	start(t, s)
	entries, err := instance.List(s.cfg.RuntimeDir)
	if err != nil || len(entries) != 1 || entries[0].PID != os.Getpid() {
		t.Fatalf("entries = %+v, err = %v; want the new run's entry only", entries, err)
	}
	c, r := dial(t, s)
	write(t, c, `{"id":1,"method":"session.ping"}`)
	if m := read(t, r); m.Error != nil {
		t.Fatal(m.Error)
	}
}

func TestClaimFailsOnAnUnresponsiveHolderAndRemovesNothing(t *testing.T) {
	holder := inertServer(t)
	start(t, holder)
	s := New(holder.cfg)
	s.probe = func(string) error { return instance.ErrPingTimeout }
	err := s.Start()
	if err == nil || err.Error() != `instance "test" did not answer; it may be busy` {
		t.Fatalf("err = %v", err)
	}
	if !present(holder.SocketPath()) || !present(instance.EntryPath(holder.cfg.RuntimeDir, "test")) {
		t.Fatal("a busy holder lost its files")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !present(holder.SocketPath()) || !present(instance.EntryPath(holder.cfg.RuntimeDir, "test")) {
		t.Fatal("the failed claimant's Close removed the holder's files")
	}
}

// The first probe finds a dead socket, then the holder wakes up and is only
// unresponsive when recovery probes again under the lock: still not evicted.
func TestClaimReprobesUnderTheLock(t *testing.T) {
	s := inertServer(t)
	deadSocketFor(t, s)
	calls := 0
	s.probe = func(path string) error {
		calls++
		if calls == 1 {
			return instance.ErrSocketRefused
		}
		return instance.ErrPingTimeout
	}
	err := s.Start()
	if err == nil || !strings.Contains(err.Error(), "did not answer; it may be busy") {
		t.Fatalf("err = %v", err)
	}
	if !present(s.SocketPath()) {
		t.Fatal("recovery removed a socket whose holder answered the re-probe with a timeout")
	}
}

func TestClaimFailsWhenTheRuntimeDirIsLocked(t *testing.T) {
	s := inertServer(t)
	deadSocketFor(t, s)
	f, err := os.Open(s.cfg.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	s.lockWait = 100 * time.Millisecond
	err = s.Start()
	if err == nil || err.Error() != "runtime dir "+s.cfg.RuntimeDir+" is locked" {
		t.Fatalf("err = %v", err)
	}
	if !present(s.SocketPath()) {
		t.Fatal("nothing may be removed without the lock")
	}
}

func TestClaimRefusesANonSocketAtThePath(t *testing.T) {
	s := inertServer(t)
	if err := os.WriteFile(s.SocketPath(), []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.Start()
	if err == nil || !strings.Contains(err.Error(), s.SocketPath()) {
		t.Fatalf("err = %v, want one naming %s", err, s.SocketPath())
	}
	if b, rerr := os.ReadFile(s.SocketPath()); rerr != nil || string(b) != "not a socket" {
		t.Fatal("the non-socket was touched", rerr)
	}
}

func TestCloseLeavesAReplacementSocketAtTheSamePath(t *testing.T) {
	s := inertServer(t)
	start(t, s)
	// Another process takes the path. Our file stays on disk under another
	// name so the replacement cannot reuse its inode.
	aside := filepath.Join(s.cfg.RuntimeDir, "ours.old")
	if err := os.Rename(s.SocketPath(), aside); err != nil {
		t.Fatal(err)
	}
	repl, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	repl.SetUnlinkOnClose(false)
	defer repl.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !present(s.SocketPath()) {
		t.Fatal("Close unlinked a replacement socket")
	}
	if !present(instance.EntryPath(s.cfg.RuntimeDir, "test")) {
		t.Fatal("Close removed an entry that now belongs to the replacement's owner")
	}
}

func TestIdentityIsTakenFromTheSocketTheRecoveryBindCreated(t *testing.T) {
	s := inertServer(t)
	deadSocketFor(t, s)
	old, err := instance.SocketIdentity(s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	// Keep the dead file's inode occupied so a new socket cannot reuse it.
	if err := os.Link(s.SocketPath(), filepath.Join(s.cfg.RuntimeDir, "dead.keep")); err != nil {
		t.Skipf("hard-linking a socket is not supported here: %v", err)
	}
	start(t, s)
	now, err := instance.SocketIdentity(s.SocketPath())
	if err != nil || now == old {
		t.Fatalf("identity %+v after recovery vs dead %+v, err = %v", now, old, err)
	}
	if s.ident != now {
		t.Fatalf("Server kept %+v, the live socket is %+v", s.ident, now)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if present(s.SocketPath()) {
		t.Fatal("Close did not remove its own socket")
	}
}

func TestHostVersionReachesTheEntryAndSessionPing(t *testing.T) {
	for _, version := range []string{"1.2.3", ""} {
		s := inertServer(t)
		s.cfg.Entry.HostVersion = version
		start(t, s)
		raw, err := os.ReadFile(instance.EntryPath(s.cfg.RuntimeDir, "test"))
		if err != nil {
			t.Fatal(err)
		}
		var entry map[string]any
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		if v, ok := entry["host_version"]; !ok || v != version {
			t.Fatalf("entry host_version = %v (present %v), want %q", v, ok, version)
		}
		c, r := dial(t, s)
		write(t, c, `{"id":1,"method":"session.ping"}`)
		var res map[string]any
		if err := json.Unmarshal(read(t, r).Result, &res); err != nil {
			t.Fatal(err)
		}
		if v, ok := res["host_version"]; !ok || v != version {
			t.Fatalf("ping host_version = %v (present %v), want %q", v, ok, version)
		}
	}
}
