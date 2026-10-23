//go:build !linux && !darwin

package ptyhost

import (
	"errors"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Child is unavailable on platforms without the supported Unix PTY backend.
type Child struct{}

func unsupported() error {
	return errors.New("PTY hosting supports Linux and macOS; Windows ConPTY is not implemented")
}

// Start returns an explicit unsupported-platform error.
func Start([]string, Options) (*Child, error) { return nil, unsupported() }

// Pump is unavailable without a supported PTY backend.
func (*Child) Pump(protocol.SurfaceID, func(protocol.TerminalWriteParams) error) error {
	return unsupported()
}

// Input is unavailable without a supported PTY backend.
func (*Child) Input(protocol.TerminalInputParams) error { return unsupported() }

// Resize is unavailable without a supported PTY backend.
func (*Child) Resize(int, int) error { return unsupported() }

// Wait is unavailable without a supported PTY backend.
func (*Child) Wait() error { return unsupported() }

// Close is harmless on an unavailable backend.
func (*Child) Close() error { return nil }
