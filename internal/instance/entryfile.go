package instance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// MaxEntryBytes is the registration entry size limit; a larger file is
// ignored as corrupt wherever entries are read.
const MaxEntryBytes = 4096

// Register writes e as the instance's registration entry, atomically: the
// JSON document is written to a temporary file in dir and renamed onto
// <id>.json, so a reader never sees a partial entry. The file gets mode
// 0600.
//
// If an entry for the same id already belongs to a different, live process
// — its socket answers a ping and its pid differs from e's — Register fails
// with the spec's taken-name error and writes nothing. A stale or corrupt
// entry is replaced: the Server reaches Register only after bind made the
// name its own, so whatever entry is left there belongs to a dead run. A
// zero StartedAt is stamped from Deps.Now before writing.
func Register(dir string, e Entry, d *Deps) error {
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
	if old, err := readEntry(path); err == nil && old.PID != e.PID && d.classify(old) == VerdictLive {
		return &Error{
			Kind:    KindTaken,
			Message: fmt.Sprintf("instance %q is already running (pid %d)", displayName(old), old.PID),
		}
	}

	if e.StartedAt.IsZero() {
		e.StartedAt = d.now().UTC()
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
	if _, err := f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("instance %s: write entry: %w", e.ID, err)
	}
	return nil
}

// Unregister removes the instance's entry and then its socket. A missing
// file is not an error, so Unregister is idempotent and safe to call when a
// crash already removed one of the files. It is the Server's Close step.
func Unregister(dir, id string) error {
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
	cur, err := readEntry(EntryPath(dir, e.ID))
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
		e, err := readEntry(filepath.Join(dir, name.Name()))
		if err != nil {
			continue // corrupt or unreadable: ignored as corrupt
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// readEntry reads and validates one registration entry file. Every failure
// mode — missing, not a regular file, over MaxEntryBytes, bad JSON,
// incomplete fields — is an error; callers treat them as "no usable entry".
func readEntry(path string) (Entry, error) {
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
	if e.ID == "" || e.PID <= 0 || e.Socket == "" {
		return Entry{}, fmt.Errorf("%s: incomplete entry", path)
	}
	return e, nil
}

func displayName(e Entry) string {
	if e.Name != "" {
		return e.Name
	}
	return e.ID
}
