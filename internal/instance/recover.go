package instance

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// LockWait is how long stale recovery waits for the runtime-directory lock:
// the connect bound plus the probe bound.
const LockWait = 2500 * time.Millisecond

// ErrLocked means the runtime-directory lock was not acquired within the
// deadline. Nothing was removed and no classes are returned.
var ErrLocked = errors.New("runtime dir is locked")

// NotSocketError reports a path at <id>.sock that exists but is not a
// socket. Recovery leaves it alone.
type NotSocketError struct {
	// Path is the offending path.
	Path string
}

// Error implements error.
func (e *NotSocketError) Error() string {
	return fmt.Sprintf("%s exists and is not a socket; refusing to remove it", e.Path)
}

// RecoverStale runs the spec's stale recovery for all entries under one
// directory lock: it probes each socket again, and for those still refused or
// missing removes <id>.json when it still holds the probed pid and
// started_at, then unlinks <id>.sock when lstat says it is a socket. An entry
// with pid 0 and an empty started_at (the Server's &Entry{ID: id}) matches
// any entry file of that id. The class per entry is what the re-probe found:
// a live or unresponsive entry is left untouched and the caller acts on it.
//
// ErrLocked means the lock was not acquired in time: the classes are nil and
// nothing was removed. A path at <id>.sock that is not a socket is left
// alone and reported as a *NotSocketError (joined if several); the other
// entries are still recovered and their classes returned.
func RecoverStale(dir string, entries []*Entry) ([]Class, error) {
	return RecoverStaleWith(dir, entries, nil)
}

// RecoverStaleWith is RecoverStale with the test seams in d; nil is
// production.
func RecoverStaleWith(dir string, entries []*Entry, d *Deps) ([]Class, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	for _, e := range entries {
		if e == nil {
			return nil, errors.New("recover stale: nil entry")
		}
		if err := ValidID(e.ID); err != nil {
			return nil, err
		}
	}
	unlock, err := lockDir(dir, d.lockWait())
	if err != nil {
		return nil, err
	}
	defer unlock()

	classes := make([]Class, len(entries))
	missing := make([]bool, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := d.pingFor(SocketPath(dir, e.ID), ProbeTimeout)
			classes[i] = pingVerdict(err)
			missing[i] = errors.Is(err, ErrSocketMissing) || errors.Is(err, os.ErrNotExist)
		}()
	}
	wg.Wait()

	var errs []error
	for i, e := range entries {
		if classes[i] != VerdictStale {
			continue
		}
		sock := SocketPath(dir, e.ID)
		// Refuse a path that is not a socket before removing anything: the
		// entry stays, so every survey meets it and warns again.
		if !missing[i] {
			if err := checkSocket(sock); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := removeEntryIfMatches(dir, e, d); err != nil {
			errs = append(errs, err)
		}
		// A socket the probe found missing is not removed, whatever lstat
		// says now: the claimant that unlinked it may have bound a new one
		// since, outside the lock, and only a path that existed (and
		// refused) at the probe is certainly still the dead file, because
		// no bind can succeed over it while the lock is held.
		if missing[i] {
			continue
		}
		if err := removeSocket(sock, d); err != nil {
			errs = append(errs, err)
		}
	}
	return classes, errors.Join(errs...)
}

// removeEntryIfMatches removes <id>.json when it still holds e's pid and
// started_at, or whatever it holds when e carries neither. A missing or
// corrupt file is left alone: a corrupt entry is replaced by the next
// Register for its id.
func removeEntryIfMatches(dir string, e *Entry, d *Deps) error {
	cur, err := readEntry(dir, e.ID+".json")
	if err != nil {
		return nil
	}
	if anyEntry := e.PID == 0 && e.StartedAt.IsZero(); !anyEntry &&
		(cur.PID != e.PID || !cur.StartedAt.Equal(e.StartedAt)) {
		return nil
	}
	if err := d.remove(EntryPath(dir, e.ID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// checkSocket returns nil when path is absent or a socket, a *NotSocketError
// when it is anything else, and the lstat error when it cannot be inspected.
func checkSocket(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Mode().Type() != os.ModeSocket {
		return &NotSocketError{Path: path}
	}
	return nil
}

// removeSocket unlinks path only if lstat says it is a socket.
func removeSocket(path string, d *Deps) error {
	if err := checkSocket(path); err != nil {
		return err
	}
	if err := d.remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
