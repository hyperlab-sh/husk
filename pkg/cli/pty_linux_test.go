//go:build linux

package cli

import (
	"fmt"
	"os"

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
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = m.Close()
		return nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	// The master stays open for the life of the process (a finalizer would
	// close it, hanging up the slave).
	ptyMasters = append(ptyMasters, m)
	return s, nil
}
