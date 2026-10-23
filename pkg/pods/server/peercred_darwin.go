//go:build darwin

package server

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID ports the existing host's LOCAL_PEERCRED check, never peer PID.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var lookup error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			lookup = err
			return
		}
		uid = cred.Uid
	}); err != nil {
		return 0, err
	}
	return uid, lookup
}
