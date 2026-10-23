//go:build unix

package instance

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockRetry is the pause between non-blocking lock attempts.
const lockRetry = 50 * time.Millisecond

// lockDir takes an exclusive advisory flock on a read-only descriptor of dir
// itself; no lock file exists. flock has no timeout, so it retries LOCK_NB
// every lockRetry until wait elapses and returns ErrLocked then. A blocking
// flock is never started, so no abandoned call can take the lock after the
// deadline. The returned function releases the lock by closing the
// descriptor, which a dying holder does for itself.
func lockDir(dir string, wait time.Duration) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("lock runtime dir: %w", err)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("lock runtime dir %s: %w", dir, err)
		}
		retry := time.NewTimer(lockRetry)
		select {
		case <-timer.C:
			retry.Stop()
			_ = f.Close()
			return nil, ErrLocked
		case <-retry.C:
		}
	}
}
