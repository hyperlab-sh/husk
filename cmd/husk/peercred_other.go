//go:build !linux && !darwin

package main

import (
	"errors"
	"net"
)

func peerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("peer credential check unsupported on this platform")
}
