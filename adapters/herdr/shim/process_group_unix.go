//go:build linux || darwin

package shim

import (
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}
func signalProcessGroup(pid int, signal syscall.Signal) error { return syscall.Kill(pid, signal) }
