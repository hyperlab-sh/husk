package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func privateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(path)
		if _, err := os.Lstat(parent); errors.Is(err, fs.ErrNotExist) {
			if err = privateDir(parent); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("store: directory %s is not private", path)
	}
	return nil
}

func pathInside(root string, parts []string, create bool) (string, error) {
	path := root
	if err := privateExistingDir(path); err != nil {
		return "", err
	}
	for i, part := range parts {
		if !safeSegment(part) {
			return "", ErrInvalidPath
		}
		path = filepath.Join(path, part)
		if i < len(parts)-1 {
			var err error
			if create {
				err = privateDir(path)
			} else {
				err = privateExistingDir(path)
			}
			if err != nil {
				return "", err
			}
		}
	}
	return path, nil
}

func privateExistingDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("store: directory %s is not private", path)
	}
	return nil
}

// Runtime roots follow instance's ownership/no-group-or-other-bits rule;
// data/config directories retain the stricter 0700 creation/storage contract.
func privateRuntimeDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("store: runtime directory %s is not private", path)
	}
	return nil
}

// os.CreateTemp appends a decimal uint32 suffix to Store's reserved prefix.
// Similar hidden filenames are user data, not evidence of a crashed write.
func isStoreTemp(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	i := strings.LastIndex(name, ".tmp-")
	if i < 2 || !safeSegment(name[1:i]) {
		return false
	}
	suffix := name[i+len(".tmp-"):]
	if len(suffix) == 0 || len(suffix) > 10 {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") && filepath.Base(s) == s
}
func validID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	first := s[0]
	return (first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z' || first >= '0' && first <= '9') && state.ValidNamespace(s+".state") && s != "husk"
}
func validName(s string) bool { return instance.ValidName(s) == nil }

func readFile(path string, max int64, strict bool) ([]byte, fs.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() || strict && info.Mode().Perm() != 0600 {
		return nil, nil, fmt.Errorf("store: file %s is not private regular data", path)
	}
	if info.Size() > max {
		return nil, info, fmt.Errorf("store: file exceeds %d bytes", max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(data)) > max {
		err = fmt.Errorf("store: file exceeds %d bytes", max)
	}
	return data, info, err
}

func atomicWrite(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := privateExistingDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("store: refusing non-regular destination %s", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// LocalSource stores scoped records beneath the XDG State directory.
// Construct it with NewLocalSource; its files are always private and atomic.
type LocalSource struct{ root string }

// NewLocalSource opens or creates a private source root.
func NewLocalSource(root string) (*LocalSource, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrInvalidPath
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	return &LocalSource{root: root}, nil
}

// ID returns the stable local-source identifier.
func (*LocalSource) ID() string { return "local" }
func (s *LocalSource) path(k Key, create bool) (string, error) {
	var parts []string
	switch k.Scope {
	case state.ScopeUser:
		if k.Instance != "" {
			return "", ErrInvalidPath
		}
		parts = []string{"user"}
	case state.ScopeInstance, state.ScopeWorkspace:
		if !validName(k.Instance) {
			return "", ErrInvalidPath
		}
		parts = []string{"instances", k.Instance}
		if k.Scope == state.ScopeWorkspace {
			parts = append(parts, "workspace")
		}
	default:
		return "", ErrInvalidPath
	}
	parts = append(parts, k.Path...)
	if create { // Ensure each directory including a scoped root, never chmod an unsafe existing one.
		path := s.root
		for i, p := range parts {
			if !safeSegment(p) {
				return "", ErrInvalidPath
			}
			path = filepath.Join(path, p)
			if i < len(parts)-1 {
				if err := privateDir(path); err != nil {
					return "", err
				}
			}
		}
	}
	return pathInside(s.root, parts, false)
}

// Get reads one bounded private record and its metadata.
func (s *LocalSource) Get(ctx context.Context, k Key) ([]byte, Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, Entry{}, err
	}
	p, err := s.path(k, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Entry{}, ErrNotFound
	}
	if err != nil {
		return nil, Entry{}, err
	}
	b, info, err := readFile(p, state.HardMaxBytes+65536, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Entry{}, ErrNotFound
	}
	if err != nil {
		return nil, Entry{}, err
	}
	return b, Entry{Key: k, Size: info.Size(), Modified: info.ModTime()}, nil
}

// Put atomically writes one private record with file and directory fsync.
func (s *LocalSource) Put(ctx context.Context, k Key, b []byte) error {
	if len(k.Path) == 0 {
		return ErrInvalidPath
	}
	p, err := s.path(k, true)
	if err != nil {
		return err
	}
	return atomicWrite(ctx, p, b)
}

// Delete removes a file; absent records are successful no-ops.
func (s *LocalSource) Delete(ctx context.Context, k Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(k.Path) == 0 {
		return ErrInvalidPath
	}
	p, err := s.path(k, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// List lists regular records under one scoped prefix, ignoring crash temp files.
// Symlinks and unsafe directories are rejected, never followed.
func (s *LocalSource) List(ctx context.Context, prefix Key) ([]Entry, error) {
	p, err := s.path(prefix, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []Entry
	err = filepath.WalkDir(p, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("store: symlink %s", path)
		}
		if d.IsDir() {
			err := privateExistingDir(path)
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return err
		}
		if isStoreTemp(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("store: not regular %s", path)
		}
		rel, err := filepath.Rel(p, path)
		if err != nil {
			return err
		}
		k := prefix
		k.Path = append(append([]string(nil), prefix.Path...), strings.Split(rel, string(filepath.Separator))...)
		out = append(out, Entry{Key: k, Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	return out, err
}
