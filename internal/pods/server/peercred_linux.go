//go:build linux

package server

import (
	"golang.org/x/sys/unix"
	"net"
)

// peerUID ports the existing host's SO_PEERCRED check without PID attribution.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var lookup error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
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
