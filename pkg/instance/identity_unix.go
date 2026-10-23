//go:build unix

package instance

import (
	"fmt"
	"os"
	"syscall"
)

// Identity is the (device, inode) pair lstat reports for a socket path. A
// Server takes it right after bind so Unregister can tell its own socket
// file from a replacement bound at the same path. The listener's file
// descriptor cannot stand in: fstat on a socket descriptor reports the
// socket filesystem, never the path's inode.
type Identity struct {
	Dev, Ino uint64
}

// SocketIdentity returns the identity of the file at path, taken with lstat
// so a symlink is identified as itself. A missing path returns an error
// matching os.ErrNotExist.
func SocketIdentity(path string) (Identity, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, fmt.Errorf("socket identity of %s: no device and inode on this platform", path)
	}
	return Identity{Dev: uint64(sys.Dev), Ino: uint64(sys.Ino)}, nil
}
