package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"

	"filippo.io/age"
	"github.com/godbus/dbus/v5"
	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
)

// Bound plaintext independently of per-value limits; ciphertext gets room for
// age's framing overhead, so every accepted write can be read back.
const maxSecretNamespaceBytes = 64 * 1024 * 1024

type osKeyring struct{}

func (osKeyring) Get(service, account string) (string, error) { return keyring.Get(service, account) }
func (osKeyring) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

type ageSealer struct{ identity *age.X25519Identity }

func (s ageSealer) Seal(plain []byte) ([]byte, error) {
	var out bytes.Buffer
	writer, err := age.Encrypt(&out, s.identity.Recipient())
	if err != nil {
		return nil, err
	}
	if _, err = writer.Write(plain); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func (s ageSealer) Open(sealed []byte) ([]byte, error) {
	reader, err := age.Decrypt(bytes.NewReader(sealed), s.identity)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(reader, maxSecretNamespaceBytes+1))
}

func withFileLock(path string, f func() error) error {
	// gofrs/flock owns the portable advisory-lock implementation. Precreate and
	// validate its file so its default permissions cannot expose Store files.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if _, _, err = readFile(path, 4096, true); err != nil {
		return err
	}
	lock := flock.New(path)
	if err = lock.Lock(); err != nil {
		return err
	}
	defer lock.Close()
	return f()
}

// keyringAbsent accepts only evidence that no identity/backend/session bus is
// available. In particular, a locked collection, NoReply or an arbitrary process
// exit does not prove absence and must not replace a potentially existing key.
func keyringAbsent(err error) bool {
	if errors.Is(err, keyring.ErrNotFound) || errors.Is(err, keyring.ErrUnsupportedPlatform) {
		return true
	}
	serviceAbsent := func(name string) bool {
		return name == "org.freedesktop.DBus.Error.ServiceUnknown" || name == "org.freedesktop.DBus.Error.NameHasNoOwner"
	}
	var value dbus.Error
	if errors.As(err, &value) {
		return serviceAbsent(value.Name)
	}
	var pointer *dbus.Error
	if errors.As(err, &pointer) && pointer != nil {
		return serviceAbsent(pointer.Name)
	}
	// These are the concrete discovery failures returned by godbus: no bus
	// address, no autolaunch executable, or an absent configured Unix socket.
	// Do not equate permission errors or connection refusal with absence.
	var launcher *exec.Error
	if errors.As(err, &launcher) && launcher.Name == "dbus-launch" && errors.Is(launcher.Err, exec.ErrNotFound) {
		return true
	}
	var dial *net.OpError
	if errors.As(err, &dial) && dial.Op == "dial" && dial.Net == "unix" && errors.Is(dial.Err, fs.ErrNotExist) {
		return true
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if cause.Error() == "dbus: couldn't determine address of session bus" {
			return true
		}
	}
	return false
}

// ensureNoSealedNamespaces fails closed before generating a replacement key.
// Backend absence can mean a provider has not started yet, not an empty store.
func (s *Store) ensureNoSealedNamespaces() error {
	reason := ""
	dir, err := pathInside(s.paths.Data, []string{"secrets"}, false)
	if err == nil {
		err = privateExistingDir(dir)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		reason = "cannot verify sealed namespaces; no new identity was created"
	} else {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			reason = "cannot inspect sealed namespaces; no new identity was created"
		} else {
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".age") {
					reason = "sealed namespaces already exist; restore the keyring or reset-secrets before creating an identity"
					break
				}
			}
		}
	}
	if reason == "" {
		return nil
	}
	s.diagnostic(Problem{Code: "unavailable", Message: reason}, "")
	return fmt.Errorf("%w: %s", ErrUnavailable, reason)
}

func (s *Store) identity(create bool) (Sealer, error) {
	if s.sealer != nil {
		return s.sealer, nil
	}
	path, err := pathInside(s.paths.Data, []string{"keys", "identity.txt"}, create)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var identity *age.X25519Identity
	if err == nil {
		b, _, readErr := readFile(path, 4096, true)
		if readErr == nil {
			identity, err = age.ParseX25519Identity(strings.TrimSpace(string(b)))
			if err != nil {
				return nil, fmt.Errorf("store: invalid identity file; reset-secrets required")
			}
			s.markFallback()
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			return nil, readErr
		}
	}
	if identity == nil {
		ring := s.opts.Keyring
		if ring == nil {
			ring = osKeyring{}
		}
		text, ringErr := ring.Get("husk", "store-identity")
		if ringErr == nil {
			identity, err = age.ParseX25519Identity(text)
			if err != nil {
				return nil, fmt.Errorf("store: invalid keyring identity; reset-secrets required")
			}
		} else {
			if !keyringAbsent(ringErr) {
				s.diagnostic(Problem{Code: "unavailable", Message: "OS keyring could not be read; no new identity was created"}, "")
				return nil, fmt.Errorf("%w: OS keyring could not be read", ErrUnavailable)
			}
			if !create {
				return nil, ErrNotFound
			}
			if err = s.ensureNoSealedNamespaces(); err != nil {
				return nil, err
			}
			identity, err = age.GenerateX25519Identity()
			if err != nil {
				return nil, err
			}
			ringErr = ring.Set("husk", "store-identity", identity.String())
			if ringErr != nil {
				path, err = pathInside(s.paths.Data, []string{"keys", "identity.txt"}, true)
				if err != nil {
					return nil, err
				}
				if err = atomicWrite(context.Background(), path, []byte(identity.String()+"\n")); err != nil {
					return nil, err
				}
				s.markFallback()
			}
		}
	}
	s.sealer = ageSealer{identity}
	return s.sealer, nil
}

// detectFallback observes existing private identity metadata at startup without
// consulting the OS keyring or eagerly loading secret key material.
func (s *Store) detectFallback() {
	path, err := pathInside(s.paths.Data, []string{"keys", "identity.txt"}, false)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		var info fs.FileInfo
		info, err = os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || int(stat.Uid) != os.Getuid() {
				err = fmt.Errorf("fallback identity is not private regular data")
			}
		}
	}
	if err != nil {
		s.diagnostic(Problem{Code: "unavailable", Message: "fallback identity metadata is unavailable"}, "")
		return
	}
	s.markFallback()
}

func (s *Store) markFallback() {
	s.mu.Lock()
	first := !s.health.KeyringFallback
	s.health.KeyringFallback = true
	s.mu.Unlock()
	if first {
		s.diagnostic(Problem{Code: "keyring_fallback", Message: "OS keyring unavailable; identity is stored on disk"}, "")
	}
}
func validSecretName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

type secretDoc struct {
	store     *Store
	namespace string
}

// Secrets returns the encrypted user namespace API. No identity is created until
// the first write. A bad existing encrypted document is never replaced.
func (s *Store) Secrets(namespace string) (SecretDoc, error) {
	if !validSecretName(namespace) {
		return nil, ErrInvalidPath
	}
	return &secretDoc{store: s, namespace: namespace}, nil
}
func (d *secretDoc) access(write bool, fn func(map[string][]byte) (bool, error)) error {
	s := d.store
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()
	s.mu.Lock()
	closed := s.closing
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	// Serialize read-modify-write across instances, not merely goroutines.
	lockPath, err := pathInside(s.paths.Data, []string{"secrets", "." + d.namespace + ".lock"}, true)
	if err != nil {
		return err
	}
	return withFileLock(lockPath, func() error {
		path, err := pathInside(s.paths.Data, []string{"secrets", d.namespace + ".age"}, false)
		if err != nil {
			return err
		}
		sealed, _, err := readFile(path, maxSecretNamespaceBytes+65536, true)
		exists := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: encrypted namespace unreadable; consider husk store reset-secrets", ErrUnavailable)
		}
		values := map[string][]byte{}
		if exists {
			sealer, err := s.identity(false)
			if err != nil {
				return fmt.Errorf("%w: encrypted namespace unreadable; consider husk store reset-secrets", ErrUnavailable)
			}
			plain, err := sealer.Open(sealed)
			if err != nil || len(plain) > maxSecretNamespaceBytes {
				return fmt.Errorf("%w: encrypted namespace unreadable; consider husk store reset-secrets", ErrUnavailable)
			}
			if json.Unmarshal(plain, &values) != nil || values == nil {
				return fmt.Errorf("%w: encrypted namespace unreadable; consider husk store reset-secrets", ErrUnavailable)
			}
		}
		changed, err := fn(values)
		if err != nil || !write || !changed {
			return err
		}
		keyLock, err := pathInside(s.paths.Data, []string{"keys", ".identity.lock"}, true)
		if err != nil {
			return err
		}
		return withFileLock(keyLock, func() error {
			sealer, err := s.identity(true)
			if err != nil {
				return err
			}
			plain, err := json.Marshal(values)
			if err != nil {
				return err
			}
			if len(plain) > maxSecretNamespaceBytes {
				return fmt.Errorf("store: secret namespace exceeds 64 MiB")
			}
			sealed, err := sealer.Seal(plain)
			if err != nil {
				return err
			}
			return atomicWrite(context.Background(), path, sealed)
		})
	})
}

// Get reads an independent value without creating a missing identity.
func (d *secretDoc) Get(key string) ([]byte, bool, error) {
	if !validSecretName(key) {
		return nil, false, ErrInvalidPath
	}
	var value []byte
	var found bool
	err := d.access(false, func(values map[string][]byte) (bool, error) {
		v, ok := values[key]
		found = ok
		value = bytes.Clone(v)
		return false, nil
	})
	return value, found, err
}

// Put encrypts the updated namespace under an inter-instance file lock.
func (d *secretDoc) Put(key string, value []byte) error {
	if !validSecretName(key) {
		return ErrInvalidPath
	}
	if len(value) > 64*1024 {
		return fmt.Errorf("store: secret value exceeds 64 KiB")
	}
	return d.access(true, func(values map[string][]byte) (bool, error) { values[key] = bytes.Clone(value); return true, nil })
}

// Delete removes a key, leaving unreadable encrypted data untouched.
func (d *secretDoc) Delete(key string) error {
	if !validSecretName(key) {
		return ErrInvalidPath
	}
	return d.access(true, func(values map[string][]byte) (bool, error) {
		_, exists := values[key]
		delete(values, key)
		return exists, nil
	})
}

// Keys returns sorted secret names, never their values.
func (d *secretDoc) Keys() ([]string, error) {
	var keys []string
	err := d.access(false, func(values map[string][]byte) (bool, error) {
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return false, nil
	})
	return keys, err
}
