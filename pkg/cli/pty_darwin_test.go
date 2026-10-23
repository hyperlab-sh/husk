//go:build darwin

package cli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

var ptyMasters []*os.File

// openPTY returns the slave end of a new pseudo-terminal.
func openPTY() (*os.File, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		_ = m.Close()
		return nil, err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		_ = m.Close()
		return nil, err
	}
	var buf [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		_ = m.Close()
		return nil, errno
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	s, err := os.OpenFile(string(buf[:n]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	// The master stays open for the life of the process (a finalizer would
	// close it, hanging up the slave).
	ptyMasters = append(ptyMasters, m)
	return s, nil
}
