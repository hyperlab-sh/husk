//go:build !linux && !darwin

package shim

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureProcessGroup(*exec.Cmd) error {
	return errors.New("command process groups support Linux and macOS only")
}
func signalProcessGroup(int, syscall.Signal) error { return syscall.ESRCH }
