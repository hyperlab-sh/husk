package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// socketName is the file name of the IPC socket inside the private directory.
const socketName = "husk.sock"

// createSocketDir returns a fresh directory with mode 0700 that will hold the
// IPC socket. It lives under runtimeDir (normally $XDG_RUNTIME_DIR) when that
// is set and usable; otherwise under a mkdtemp directory in the system temp
// dir. The returned directory is always newly created, so it cannot be a
// pre-planted path owned by another user.
func createSocketDir(runtimeDir string) (string, error) {
	if runtimeDir != "" {
		if st, err := os.Stat(runtimeDir); err == nil && st.IsDir() {
			dir, err := os.MkdirTemp(runtimeDir, "husk-")
			if err == nil {
				if err := os.Chmod(dir, 0o700); err != nil {
					_ = os.RemoveAll(dir)
					return "", err
				}
				return dir, nil
			}
		}
	}
	dir, err := os.MkdirTemp("", "husk-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// listenPrivate creates the private directory and a Unix listener inside it
// with the socket file restricted to 0600. It returns the listener, the socket
// path and the directory (to be removed on shutdown).
func listenPrivate(runtimeDir string) (net.Listener, string, string, error) {
	dir, err := createSocketDir(runtimeDir)
	if err != nil {
		return nil, "", "", fmt.Errorf("create socket dir: %w", err)
	}
	path := filepath.Join(dir, socketName)
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, "", "", fmt.Errorf("chmod socket: %w", err)
	}
	return listener, path, dir, nil
}

// checkPeerUID reports an error unless the connecting peer's uid equals want.
func checkPeerUID(peer uint32, want int) error {
	if int64(peer) != int64(want) {
		return fmt.Errorf("peer uid %d does not match host uid %d", peer, want)
	}
	return nil
}

// authorizePeer verifies that conn's kernel-reported peer uid is our own.
// Anything that is not a Unix socket, or whose credentials cannot be read, is
// refused (fail closed).
func authorizePeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("not a unix connection")
	}
	uid, err := peerUID(uc)
	if err != nil {
		return fmt.Errorf("read peer credentials: %w", err)
	}
	return checkPeerUID(uid, os.Getuid())
}

// listenSocket opens the host IPC socket under $XDG_RUNTIME_DIR when set,
// otherwise under a private temp directory.
func listenSocket() (net.Listener, string, error) {
	l, path, _, err := listenPrivate(os.Getenv("XDG_RUNTIME_DIR"))
	return l, path, err
}
