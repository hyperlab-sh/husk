//go:build !linux && !darwin

package cli

import (
	"errors"
	"os"
)

// openPTY is implemented on Linux and darwin; the pty case is skipped elsewhere.
func openPTY() (*os.File, error) { return nil, errors.New("no pty on this platform") }
