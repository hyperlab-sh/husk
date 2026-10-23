//go:build !unix

package instance

import (
	"errors"
	"time"
)

// lockDir is unavailable here: advisory directory locks are Unix-only, as
// are the sockets they protect.
func lockDir(string, time.Duration) (func(), error) {
	return nil, errors.New("runtime dir lock is not supported on this platform")
}
