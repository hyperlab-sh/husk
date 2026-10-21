//go:build darwin

package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// parentPID returns the parent pid of pid (sysctl kern.proc.pid).
func parentPID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, fmt.Errorf("no process %d", pid)
	}
	return int(kp.Eproc.Ppid), nil
}
