//go:build !unix

package instance

import (
	"errors"
	"os"
)

// Identity is the (device, inode) pair of a socket path; unavailable on this
// platform, where Unix sockets are out of scope for v1.
type Identity struct {
	Dev, Ino uint64
}

// SocketIdentity returns an error: this platform reports no inode.
func SocketIdentity(path string) (Identity, error) {
	if _, err := os.Lstat(path); err != nil {
		return Identity{}, err
	}
	return Identity{}, errors.New("socket identity is not supported on this platform")
}
