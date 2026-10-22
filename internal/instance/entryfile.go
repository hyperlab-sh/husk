package instance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxEntryBytes is the registration entry size limit; a larger file is
// ignored as corrupt wherever entries are read.
const MaxEntryBytes = 4096

// Register writes e as the instance's registration entry, atomically: the
// JSON document is written to a temporary file in dir and renamed onto
// <id>.json, so a reader never sees a partial entry. The file gets mode
// 0600.
//
// Register does no liveness probing: the claim on the name is made by
// binding the socket, not by this file. The Server reaches Register only
// after bind made the name its own, so any entry still on disk belongs to
// a dead run — stale, corrupt, or the same pid re-registering — and is
// replaced. (Probing here would ping the new server's own freshly bound
// socket, see a different pid in the old entry, and wrongly refuse: a
// named instance could then never restart after a crash.) A zero
// StartedAt is stamped from the wall clock before writing.
func Register(dir string, e Entry) error {
	if err := ValidID(e.ID); err != nil {
		return err
	}
	if e.Name != "" {
		if err := ValidName(e.Name); err != nil {
			return err
		}
	}
	if e.PID <= 0 {
		return fmt.Errorf("instance %s: entry pid must be positive", e.ID)
	}
	if want := SocketPath(dir, e.ID); e.Socket != want {
		return fmt.Errorf("instance %s: entry socket %q is not the instance's socket %q", e.ID, e.Socket, want)
	}
	if err := ensureDir(dir, os.Getuid()); err != nil {
		return err
	}

	path := EntryPath(dir, e.ID)

	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("instance %s: encode entry: %w", e.ID, err)
	}
	data = append(data, '\n')

	f, err := os.CreateTemp(dir, "."+e.ID+".json.tmp-")
	if err != nil {
		return fmt.Errorf("instance %s: create entry temp file: %w", e.ID, err)
	}
	tmp := f.Name()
	fail := func(stage string, cause error) error {
		_ = os.Remove(tmp)
		return fmt.Errorf("instance %s: %s entry: %w", e.ID, stage, cause)
	}
	if _, err := f.Write(data); err != nil {
		return fail("write", err)
	}
	if err := f.Sync(); err != nil {
		return fail("sync", err)
	}
	if err := f.Close(); err != nil {
		return fail("close", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail("rename", err)
	}
	return nil
}

// Unregister removes the instance's entry and then its socket. A missing
// file is not an error, so Unregister is idempotent and safe to call when a
// crash already removed one of the files. It is the Server's Close step.
// The id is validated first: ids become file names, so a mangled id must
// be refused, not joined onto dir.
func Unregister(dir, id string) error {
	if err := ValidID(id); err != nil {
		return err
	}
	for _, path := range []string{EntryPath(dir, id), SocketPath(dir, id)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RemoveIfStale removes e's entry and socket only if the entry file still
// holds the same pid and the same started timestamp as e. That closes the
// race where a new instance reuses the name between the failed ping and the
// unlink: whoever re-registered wrote a different pid or started_at, and
// this call becomes a no-op. A missing or corrupt file is not an error and
// is left alone.
func RemoveIfStale(dir string, e Entry) error {
	cur, err := readEntry(dir, e.ID+".json")
	if err != nil {
		return nil // missing or corrupt: nothing that holds e is on disk
	}
	if cur.PID != e.PID || !cur.StartedAt.Equal(e.StartedAt) {
		return nil
	}
	return Unregister(dir, e.ID)
}

// List reads every registration entry in dir, sorted by instance id, with
// no liveness check. Corrupt, oversized and unreadable files are skipped, so
// the result holds only entries the caller can act on. A missing directory
// lists empty: discovery then reports that no instance is running.
func List(dir string) ([]Entry, error) {
	names, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) != ".json" {
			continue
		}
		e, err := readEntry(dir, name.Name())
		if err != nil {
			continue // corrupt or unreadable: ignored as corrupt
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// readEntry reads and validates one registration entry file named base
// ("notes.json") in dir. Beyond the file-level checks — a regular file
// within MaxEntryBytes holding valid JSON with a positive pid and a
// socket — the entry must be self-consistent: its id must be a valid id
// equal to the base name, and its socket must be this instance's own
// SocketPath. Every mismatch is corrupt: replaceable by Register,
// removable by stale cleanup. Deriving the paths from the validated id is
// also what keeps a mangled id in a hand-edited entry from steering
// callers outside dir.
func readEntry(dir, base string) (Entry, error) {
	path := filepath.Join(dir, base)
	st, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxEntryBytes {
		return Entry{}, fmt.Errorf("%s: not a regular entry file or over %d bytes", path, MaxEntryBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, fmt.Errorf("%s: corrupt entry: %w", path, err)
	}
	if e.PID <= 0 || e.Socket == "" {
		return Entry{}, fmt.Errorf("%s: incomplete entry", path)
	}
	id := strings.TrimSuffix(base, ".json")
	if err := ValidID(id); err != nil || e.ID != id {
		return Entry{}, fmt.Errorf("%s: corrupt entry: id %q does not match the file name %q", path, e.ID, id)
	}
	if want := SocketPath(dir, e.ID); e.Socket != want {
		return Entry{}, fmt.Errorf("%s: corrupt entry: socket %q is not the instance's socket %q", path, e.Socket, want)
	}
	return e, nil
}
