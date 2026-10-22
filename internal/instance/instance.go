// Package instance answers two questions for Husk: where a running
// instance advertises itself, and which instance a command should talk to.
//
// One Husk process is one instance: its own socket, its own plugins, its own
// state. An instance registers itself in the user's runtime directory
// ($XDG_RUNTIME_DIR/husk/ or /tmp/husk-<uid>/) as a pair of files, <id>.sock
// and <id>.json, and removes both on a clean exit. A crash leaves them
// behind; the liveness check (a ping) classifies such an entry as stale, and
// any CLI call that meets a stale entry removes it.
//
// The CLI resolves exactly one socket per command with Resolve, in the
// order: --socket flag, --instance flag, HUSK_SOCKET_PATH, HUSK_INSTANCE,
// discovery. The first rule that applies wins even when it then fails; with
// zero or several live instances and no selector, resolution fails instead
// of guessing.
//
// This package is a leaf: it imports the standard library only. The Pods
// Server and the CLI both import it; it imports neither. Spec:
// docs/docs/design/instances.md.
package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MaxNameBytes is the instance name length limit: 32 bytes keeps any socket
// path far under the sun_path limit and stays readable in a table.
const MaxNameBytes = 32

// MaxSocketPathBytes is the socket path length limit: 103 path bytes, since
// the sun_path limit on macOS is 104 including the terminator (Linux allows
// 107). The Server checks the path before it binds.
const MaxSocketPathBytes = 103

// Environment variable names the Wrapper sets in every plugin process,
// overwriting any inherited value. Defined here so the Wrapper imports this
// package for the names only.
const (
	// EnvSocketPath holds the absolute path of the instance's socket. The
	// name is kept from Husk v1 so deployed plugins keep working; the
	// CLI's resolution step reads it first.
	EnvSocketPath = "HUSK_SOCKET_PATH"

	// EnvInstance holds the instance's id, the CLI's fallback step.
	EnvInstance = "HUSK_INSTANCE"

	// EnvBinPath holds the absolute path of the running husk binary, so a
	// plugin can call the CLI without relying on PATH.
	EnvBinPath = "HUSK_BIN_PATH"
)

// Entry is one instance's registration entry: the JSON document the Server
// writes to <id>.json once its socket listens. An entry present in the
// runtime directory means the instance was ready when it was written; it is
// the only place an instance advertises itself.
type Entry struct {
	// ID is the instance id: the decimal pid for an unnamed instance, or
	// the --instance name. It is also the file name stem of the entry and
	// the socket.
	ID string `json:"id"`

	// Name is the instance name, absent for pid instances.
	Name string `json:"name,omitempty"`

	// PID is the Husk process's pid.
	PID int `json:"pid"`

	// Socket is the absolute path of the instance's listening socket.
	Socket string `json:"socket"`

	// StartedAt is when the instance started. RemoveIfStale compares it
	// to detect that an entry file still belongs to the instance that was
	// pinged.
	StartedAt time.Time `json:"started_at"`

	// ProtocolVersion is the wire version the instance's Server speaks,
	// for example "2.0".
	ProtocolVersion string `json:"protocol_version"`

	// HuskVersion is the Husk build version, informational.
	HuskVersion string `json:"husk_version,omitempty"`
}

// ValidName reports whether s is a legal instance name: it matches
// [a-z0-9][a-z0-9-]* and is at most MaxNameBytes bytes. A name made only of
// digits is rejected, so a name never equals a pid id and "husk --instance
// 4242" always means the pid instance.
func ValidName(s string) error {
	if s == "" || len(s) > MaxNameBytes {
		return nameError(s)
	}
	if isAllDigits(s) {
		return fmt.Errorf("invalid instance name %q: a name of digits only would collide with a pid id", s)
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return nameError(s)
		}
	}
	return nil
}

// ValidID validates an instance id, which is either a pid id (a non-empty
// run of decimal digits) or a name id (ValidName). Ids become file names in
// the runtime directory, so every lookup and registration validates first.
func ValidID(s string) error {
	if s == "" {
		return nameError(s)
	}
	if isAllDigits(s) {
		if len(s) > MaxNameBytes {
			return nameError(s)
		}
		return nil
	}
	return ValidName(s)
}

// SocketPath returns the instance's socket path inside dir: <id>.sock, mode
// 0600 once the Server chmods it after bind.
func SocketPath(dir, id string) string {
	return filepath.Join(dir, id+".sock")
}

// EntryPath returns the instance's registration entry path inside dir:
// <id>.json, mode 0600.
func EntryPath(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

// CheckSocketPath reports whether path fits the spec's sun_path budget of
// MaxSocketPathBytes bytes. The Server calls it before bind so an instance
// with a long --instance name or a long XDG_RUNTIME_DIR fails with a
// message naming the limit instead of an opaque bind error.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPathBytes {
		return fmt.Errorf("socket path is %d bytes, over the %d-byte limit: %s", len(path), MaxSocketPathBytes, path)
	}
	return nil
}

// RuntimeDir resolves and verifies the shared runtime directory:
// $XDG_RUNTIME_DIR/husk/ when XDG_RUNTIME_DIR is set, otherwise
// /tmp/husk-<uid>/ (short on purpose, so socket paths fit sun_path). The
// directory is created with mode 0700 and shared by all instances of the
// user; it is never removed. It is used only if it is a real directory
// (not a symlink) owned by the current uid with no group or world bits.
// A set-but-unusable XDG_RUNTIME_DIR is an error, never a silent fallback:
// the user pointed the session at a runtime root, and dropping to /tmp
// would strand the instance where other sessions cannot see it. The /tmp
// path is used only when XDG_RUNTIME_DIR is empty, and a fallback that
// fails the same check is an error.
func RuntimeDir() (string, error) {
	return runtimeDir(os.Getenv("XDG_RUNTIME_DIR"), os.Getuid())
}

func runtimeDir(xdg string, uid int) (string, error) {
	if xdg != "" {
		dir := filepath.Join(xdg, "husk")
		if err := ensureDir(dir, uid); err != nil {
			return "", fmt.Errorf("runtime dir: %w", err)
		}
		return dir, nil
	}
	dir := fmt.Sprintf("/tmp/husk-%d", uid)
	if err := ensureDir(dir, uid); err != nil {
		return "", fmt.Errorf("runtime dir: %w", err)
	}
	return dir, nil
}

// ensureDir creates dir with mode 0700 when it does not exist and verifies
// it either way: a real directory, owned by uid, no group or world bits.
func ensureDir(dir string, uid int) error {
	st, err := os.Lstat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if st, err = os.Lstat(dir); err != nil {
			return err
		}
	}
	return checkDir(st, dir, uid)
}

// checkDir applies the spec's runtime-directory rule to one Lstat result.
func checkDir(st os.FileInfo, dir string, uid int) error {
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("runtime dir %s is a symlink", dir)
	case !st.Mode().IsDir():
		return fmt.Errorf("runtime dir %s is not a directory", dir)
	}
	if u := ownUID(st); u >= 0 && u != uid {
		return fmt.Errorf("runtime dir %s is not private to uid %d", dir, uid)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("runtime dir %s is not private to uid %d", dir, uid)
	}
	return nil
}

func nameError(s string) error {
	return fmt.Errorf("invalid instance name %q: use [a-z0-9][a-z0-9-]*, at most %d bytes", s, MaxNameBytes)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
