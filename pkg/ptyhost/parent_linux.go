package ptyhost

import (
	"os/exec"
	"syscall"
)

// The kernel terminates the session leader even if the plugin is killed before
// it can run its normal stdin-EOF or signal cleanup.
func parentDeath(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
