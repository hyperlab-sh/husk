//go:build linux || darwin

package wrapper

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func brokenPipe(err error) bool { return errors.Is(err, syscall.EPIPE) }
func groupAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func configureGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}
func signalGroup(pid int, kill bool) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// logOpenNoFollow refuses to open the stderr log through a symlink.
const logOpenNoFollow = syscall.O_NOFOLLOW
