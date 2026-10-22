package server

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/gofrs/flock"
	"github.com/hyperlab-sh/husk/internal/instance"
)

// Lock the existing directory read-only: gofrs/flock uses its directory FD on
// Linux and macOS. O_CREATE is deliberately absent; no runtime lock file exists.
func lockRecovery(dir string) (func() error, error) {
	lock := flock.New(dir, flock.SetFlag(os.O_RDONLY))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return lock.Close, nil
}

// recoverStale serializes only recovery, not normal name claims. A second
// probe inside the directory lock cannot unlink another recovery's new socket.
func (s *SocketServer) recoverStale(path string) (*net.UnixListener, error) {
	unlock, err := s.recoveryLock(s.cfg.RuntimeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlock() }()
	err = s.probe(path)
	if err == nil {
		return nil, fmt.Errorf("instance %q is already running", s.cfg.InstanceID)
	}
	if errors.Is(err, instance.ErrSocketMissing) {
		return net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	}
	if !errors.Is(err, instance.ErrSocketRefused) {
		return nil, fmt.Errorf("instance %q cannot be claimed: %w", s.cfg.InstanceID, err)
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("server: refusing to remove a non-socket claim")
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	return net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
}
