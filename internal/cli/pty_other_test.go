//go:build !linux && !darwin

package cli

import (
	"errors"
	"os"
)

// openPTY is only implemented on Linux; the pty case is skipped elsewhere.
func openPTY() (*os.File, error) { return nil, errors.New("no pty on this platform") }
