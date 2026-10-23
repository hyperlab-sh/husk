//go:build unix

package instance

import (
	"bufio"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// shortRunDir returns a private 0700 directory under /tmp: a socket path
// must fit sun_path, which t.TempDir() on macOS does not.
func shortRunDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// deadSocket leaves a socket file at <id>.sock with nothing listening: the
// state a crash leaves behind.
func deadSocket(t *testing.T, dir, id string) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: SocketPath(dir, id), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
}

// liveSocket binds <id>.sock and answers every line with a reply, as a live
// instance answers session.ping. It leaves the file in place on cleanup.
func liveSocket(t *testing.T, dir, id string) *net.UnixListener {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: SocketPath(dir, id), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if _, err := bufio.NewReader(c).ReadString('\n'); err == nil {
					_, _ = c.Write([]byte(`{"id":1,"result":{}}` + "\n"))
				}
			}()
		}
	}()
	return ln
}

func entryFor(t *testing.T, dir, id string, pid int) Entry {
	t.Helper()
	e := Entry{ID: id, PID: pid, Socket: SocketPath(dir, id), StartedAt: fixedTime(), ProtocolVersion: "2.0"}
	if err := writeEntryFile(dir, e); err != nil {
		t.Fatal(err)
	}
	return e
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestRecoverStaleRemovesEntryAndSocket(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	deadSocket(t, dir, "notes")
	classes, err := RecoverStale(dir, []*Entry{&e})
	if err != nil || len(classes) != 1 || classes[0] != VerdictStale {
		t.Fatalf("classes = %v, err = %v; want [stale]", classes, err)
	}
	if exists(EntryPath(dir, "notes")) || exists(SocketPath(dir, "notes")) {
		t.Fatal("stale entry and socket must both be gone")
	}
}

func TestRecoverStaleMissingSocketRemovesEntry(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	classes, err := RecoverStale(dir, []*Entry{&e})
	if err != nil || classes[0] != VerdictStale || exists(EntryPath(dir, "notes")) {
		t.Fatalf("classes = %v, err = %v, entry present = %v", classes, err, exists(EntryPath(dir, "notes")))
	}
}

func TestRecoverStaleLeavesALiveHolderAlone(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	liveSocket(t, dir, "notes")
	classes, err := RecoverStale(dir, []*Entry{&e})
	if err != nil || classes[0] != VerdictLive {
		t.Fatalf("classes = %v, err = %v; want [live]", classes, err)
	}
	if !exists(EntryPath(dir, "notes")) || !exists(SocketPath(dir, "notes")) {
		t.Fatal("a live holder lost a file")
	}
}

func TestRecoverStaleLeavesAnUnresponsiveHolderAlone(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	deadSocket(t, dir, "notes") // would be removed if the probe called it stale
	d := &Deps{Ping: func(string) error { return ErrPingTimeout }}
	classes, err := RecoverStaleWith(dir, []*Entry{&e}, d)
	if err != nil || classes[0] != VerdictUnresponsive {
		t.Fatalf("classes = %v, err = %v; want [unresponsive]", classes, err)
	}
	if !exists(EntryPath(dir, "notes")) || !exists(SocketPath(dir, "notes")) {
		t.Fatal("an unresponsive holder lost a file")
	}
}

// Step 3: the entry file is removed only if it still holds the probed pid and
// started_at; the socket is removed regardless, since nothing listens on it.
func TestRecoverStaleKeepsAnEntryThatChangedOwner(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "notes", 222) // re-registered by another run
	deadSocket(t, dir, "notes")
	probed := Entry{ID: "notes", PID: 111, StartedAt: fixedTime()}
	if _, err := RecoverStale(dir, []*Entry{&probed}); err != nil {
		t.Fatal(err)
	}
	if !exists(EntryPath(dir, "notes")) {
		t.Fatal("an entry with a different pid must stay")
	}
	probed.PID, probed.StartedAt = 222, fixedTime().Add(time.Minute)
	if _, err := RecoverStale(dir, []*Entry{&probed}); err != nil || !exists(EntryPath(dir, "notes")) {
		t.Fatal("an entry with a different started_at must stay", err)
	}
}

// The Server passes only the id: zero pid and empty started_at match any
// entry file of that id.
func TestRecoverStaleIDOnlyEntryMatchesAnyEntryFile(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "notes", 987)
	deadSocket(t, dir, "notes")
	classes, err := RecoverStale(dir, []*Entry{{ID: "notes"}})
	if err != nil || classes[0] != VerdictStale {
		t.Fatalf("classes = %v, err = %v", classes, err)
	}
	if exists(EntryPath(dir, "notes")) || exists(SocketPath(dir, "notes")) {
		t.Fatal("the id-only entry must clear whatever the id left")
	}
}

func TestRecoverStaleRefusesANonSocket(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	if err := os.WriteFile(SocketPath(dir, "notes"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := RecoverStale(dir, []*Entry{&e})
	var ns *NotSocketError
	if !errors.As(err, &ns) || ns.Path != SocketPath(dir, "notes") {
		t.Fatalf("err = %v, want a NotSocketError naming the path", err)
	}
	if b, rerr := os.ReadFile(SocketPath(dir, "notes")); rerr != nil || string(b) != "precious" {
		t.Fatal("a non-socket must be left alone", rerr)
	}
}

// A non-socket at <id>.sock is refused before anything is removed: the entry
// stays, so every survey meets it and warns again.
func TestRecoverStaleRefusesANonSocketBeforeRemovingTheEntry(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	if err := os.WriteFile(SocketPath(dir, "notes"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		statuses, err := Survey(dir, nil)
		if err != nil || len(statuses) != 1 || statuses[0].NotSocket != SocketPath(dir, "notes") {
			t.Fatalf("statuses = %+v, err = %v; every survey must flag the non-socket", statuses, err)
		}
		if !exists(EntryPath(dir, "notes")) {
			t.Fatal("the entry was removed before the non-socket was refused")
		}
	}
	if _, err := RecoverStale(dir, []*Entry{&e}); !exists(EntryPath(dir, "notes")) || err == nil {
		t.Fatalf("RecoverStale err = %v, entry kept = %v", err, exists(EntryPath(dir, "notes")))
	}
}

// A removal failure other than a non-socket (EACCES, EBUSY) is carried out of
// Survey on the entry's status, naming the path; the survey continues.
func TestSurveyReportsRemovalFailuresAndContinues(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "a-stuck", 111)
	deadSocket(t, dir, "a-stuck")
	entryFor(t, dir, "b-dead", 222)
	deadSocket(t, dir, "b-dead")
	stuck := SocketPath(dir, "a-stuck")
	d := &Deps{Remove: func(path string) error {
		if path == stuck {
			return &fs.PathError{Op: "remove", Path: path, Err: syscall.EACCES}
		}
		return os.Remove(path)
	}}
	statuses, err := Survey(dir, d)
	if err != nil || len(statuses) != 1 {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	st := statuses[0]
	var pe *fs.PathError
	if st.Entry.ID != "a-stuck" || !errors.As(st.RecoverErr, &pe) || pe.Path != stuck || !errors.Is(st.RecoverErr, syscall.EACCES) {
		t.Fatalf("status = %+v, want a-stuck with the EACCES path error", st)
	}
	if exists(EntryPath(dir, "b-dead")) || exists(SocketPath(dir, "b-dead")) {
		t.Fatal("the survey must go on and recover the entry beside it")
	}
}

// holdLock takes the directory lock the way a recovering process would, on
// its own descriptor, and returns the release.
func holdLock(t *testing.T, dir string) (release func()) {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	var once atomic.Bool
	release = func() {
		if once.CompareAndSwap(false, true) {
			_ = f.Close()
		}
	}
	t.Cleanup(release)
	return release
}

func TestRecoverStaleLockContentionReturnsErrLockedAndRemovesNothing(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	deadSocket(t, dir, "notes")
	release := holdLock(t, dir)

	probed := atomic.Int32{}
	d := &Deps{LockWait: 120 * time.Millisecond, Ping: func(string) error { probed.Add(1); return ErrSocketRefused }}
	classes, err := RecoverStaleWith(dir, []*Entry{&e}, d)
	if !errors.Is(err, ErrLocked) || classes != nil {
		t.Fatalf("classes = %v, err = %v; want nil, ErrLocked", classes, err)
	}
	if probed.Load() != 0 {
		t.Fatal("nothing may be probed without the lock")
	}
	if !exists(EntryPath(dir, "notes")) || !exists(SocketPath(dir, "notes")) {
		t.Fatal("nothing may be removed without the lock")
	}

	// No blocking flock was left behind: once the holder lets go, the lock
	// is free to take immediately, not held by an abandoned waiter.
	release()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock taken by an abandoned waiter: %v", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	// And recovery works again once the lock is free.
	if classes, err := RecoverStale(dir, []*Entry{&e}); err != nil || classes[0] != VerdictStale {
		t.Fatalf("after release: classes = %v, err = %v", classes, err)
	}
}

// A holder that waits out part of the deadline and then lets go is waited for:
// the lock is retried, not tried once.
func TestRecoverStaleRetriesTheLockUntilTheHolderLetsGo(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	deadSocket(t, dir, "notes")
	release := holdLock(t, dir)
	time.AfterFunc(80*time.Millisecond, release)
	classes, err := RecoverStaleWith(dir, []*Entry{&e}, &Deps{LockWait: 5 * time.Second})
	if err != nil || classes[0] != VerdictStale {
		t.Fatalf("classes = %v, err = %v", classes, err)
	}
}

// Every entry in the batch is probed and removed while one lock is held.
func TestRecoverStaleBatchRunsUnderOneLock(t *testing.T) {
	dir := shortRunDir(t)
	var batch []*Entry
	for _, id := range []string{"a", "b", "c"} {
		e := entryFor(t, dir, id, 111)
		deadSocket(t, dir, id)
		batch = append(batch, &e)
	}
	liveSocket(t, dir, "d")
	live := entryFor(t, dir, "d", 111)
	batch = append(batch, &live)

	var locked atomic.Int32
	d := &Deps{Ping: func(p string) error {
		// Another descriptor cannot take the lock while recovery holds it.
		f, err := os.Open(dir)
		if err != nil {
			t.Error(err)
			return err
		}
		defer f.Close()
		if errors.Is(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB), syscall.EWOULDBLOCK) {
			locked.Add(1)
		}
		return DefaultPing(p, ProbeTimeout)
	}}
	classes, err := RecoverStaleWith(dir, batch, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []Class{VerdictStale, VerdictStale, VerdictStale, VerdictLive}
	for i := range want {
		if classes[i] != want[i] {
			t.Fatalf("classes = %v, want %v", classes, want)
		}
	}
	if locked.Load() != int32(len(batch)) {
		t.Fatalf("lock held during %d of %d probes", locked.Load(), len(batch))
	}
	for _, id := range []string{"a", "b", "c"} {
		if exists(EntryPath(dir, id)) || exists(SocketPath(dir, id)) {
			t.Fatalf("%s survived the batch", id)
		}
	}
	if !exists(EntryPath(dir, "d")) || !exists(SocketPath(dir, "d")) {
		t.Fatal("the live entry in the batch lost a file")
	}
}

func TestSurveyRecoversStaleEntriesWithTheirSockets(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "dead", 111)
	deadSocket(t, dir, "dead")
	entryFor(t, dir, "up", 222)
	liveSocket(t, dir, "up")
	statuses, err := Survey(dir, nil)
	if err != nil || len(statuses) != 1 || statuses[0].Entry.ID != "up" || statuses[0].Verdict != VerdictLive {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	if exists(EntryPath(dir, "dead")) || exists(SocketPath(dir, "dead")) {
		t.Fatal("survey left a stale instance's files")
	}
}

func TestSurveyReportsANonSocketEntryAndLeavesItAlone(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "odd", 111)
	if err := os.WriteFile(SocketPath(dir, "odd"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	entryFor(t, dir, "dead", 222)
	deadSocket(t, dir, "dead")
	statuses, err := Survey(dir, nil)
	if err != nil || len(statuses) != 1 {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	if st := statuses[0]; st.Entry.ID != "odd" || st.NotSocket != SocketPath(dir, "odd") || st.Verdict != VerdictStale {
		t.Fatalf("status = %+v, want odd stale with NotSocket set", st)
	}
	if b, err := os.ReadFile(SocketPath(dir, "odd")); err != nil || string(b) != "precious" {
		t.Fatal("a non-socket path must be left alone", err)
	}
	if exists(EntryPath(dir, "dead")) {
		t.Fatal("the stale entry beside it must still be recovered")
	}
}

func TestSurveyWithTheLockHeldLeavesStaleEntriesUnresponsive(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "dead", 111)
	deadSocket(t, dir, "dead")
	holdLock(t, dir)
	statuses, err := Survey(dir, &Deps{LockWait: 100 * time.Millisecond})
	if err != nil || len(statuses) != 1 {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	if st := statuses[0]; st.Verdict != VerdictUnresponsive || !st.Locked {
		t.Fatalf("status = %+v, want unresponsive and Locked", st)
	}
	if !exists(EntryPath(dir, "dead")) || !exists(SocketPath(dir, "dead")) {
		t.Fatal("nothing may be removed without the lock")
	}
}

func TestSocketIdentityAndUnregister(t *testing.T) {
	dir := shortRunDir(t)
	if _, err := SocketIdentity(SocketPath(dir, "notes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing socket: err = %v, want ErrNotExist", err)
	}
	// Unregister on nothing is not an error, and idempotent.
	if err := Unregister(dir, "notes", Identity{}); err != nil {
		t.Fatal(err)
	}

	liveSocket(t, dir, "notes")
	entryFor(t, dir, "notes", 111)
	ident, err := SocketIdentity(SocketPath(dir, "notes"))
	if err != nil || ident == (Identity{}) {
		t.Fatalf("identity = %+v, err = %v", ident, err)
	}
	for i := 0; i < 2; i++ {
		if err := Unregister(dir, "notes", ident); err != nil {
			t.Fatal(err)
		}
	}
	if exists(EntryPath(dir, "notes")) || exists(SocketPath(dir, "notes")) {
		t.Fatal("Unregister left a file of its own")
	}
}

func TestUnregisterLeavesAReplacementSocketAlone(t *testing.T) {
	dir := shortRunDir(t)
	liveSocket(t, dir, "notes")
	entryFor(t, dir, "notes", 111)
	ident, err := SocketIdentity(SocketPath(dir, "notes"))
	if err != nil {
		t.Fatal(err)
	}
	// Another process takes the name: our file moves out of the way (it stays
	// on disk, so the new socket cannot reuse its inode) and a replacement
	// binds at the path.
	if err := os.Rename(SocketPath(dir, "notes"), filepath.Join(dir, "ours.old")); err != nil {
		t.Fatal(err)
	}
	liveSocket(t, dir, "notes")
	if err := Unregister(dir, "notes", ident); err != nil {
		t.Fatal(err)
	}
	if !exists(SocketPath(dir, "notes")) {
		t.Fatal("Unregister unlinked a replacement socket")
	}
	// Nor is the replacement's entry its business.
	if !exists(EntryPath(dir, "notes")) {
		t.Fatal("Unregister removed the replacement owner's entry")
	}
	// A zero identity never matches either.
	if err := Unregister(dir, "notes", Identity{}); err != nil || !exists(SocketPath(dir, "notes")) {
		t.Fatal("a zero identity must not unlink", err)
	}
}

// A socket the probe found missing is never unlinked, even if one appears
// before the unlink: it is the winner's new socket, bound outside the lock.
func TestRecoverStaleDoesNotUnlinkASocketBoundAfterAMissingProbe(t *testing.T) {
	dir := shortRunDir(t)
	e := entryFor(t, dir, "notes", 111)
	var ln *net.UnixListener
	d := &Deps{Ping: func(p string) error {
		var err error
		ln, err = net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
		if err != nil {
			t.Error(err)
		}
		ln.SetUnlinkOnClose(false)
		return ErrSocketMissing // what the probe saw before the bind
	}}
	if _, err := RecoverStaleWith(dir, []*Entry{&e}, d); err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !exists(SocketPath(dir, "notes")) {
		t.Fatal("recovery unlinked a socket bound after its probe")
	}
}

// After a crash removed the socket, the entry is still this instance's to
// remove.
func TestUnregisterRemovesTheEntryWhenTheSocketIsAlreadyGone(t *testing.T) {
	dir := shortRunDir(t)
	entryFor(t, dir, "notes", 111)
	if err := Unregister(dir, "notes", Identity{Dev: 1, Ino: 2}); err != nil || exists(EntryPath(dir, "notes")) {
		t.Fatalf("err = %v, entry present = %v", err, exists(EntryPath(dir, "notes")))
	}
}
