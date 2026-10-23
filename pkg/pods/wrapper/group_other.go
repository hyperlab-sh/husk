//go:build !linux && !darwin

package wrapper

import (
	"errors"
	"os/exec"
)

func brokenPipe(error) bool { return false }
func groupAlive(int) bool   { return false }

func configureGroup(*exec.Cmd) error {
	return errors.New("wrapper: process groups unsupported on this platform")
}
func signalGroup(int, bool) error {
	return errors.New("wrapper: process groups unsupported on this platform")
}

// logOpenNoFollow has no portable equivalent here.
const logOpenNoFollow = 0
