//go:build !linux && !darwin

package ptyhost

import (
	"errors"
	"syscall"

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

// PID returns no process on an unavailable backend.
func (*Child) PID() int { return 0 }

// Read is unavailable without a supported PTY backend.
func (*Child) Read([]byte) (int, error) { return 0, unsupported() }

// Write is unavailable without a supported PTY backend.
func (*Child) Write([]byte) (int, error) { return 0, unsupported() }

// CloseTerminal is harmless on an unavailable backend.
func (*Child) CloseTerminal() error { return nil }

// Signal is unavailable without a supported PTY backend.
func (*Child) Signal(syscall.Signal) error { return unsupported() }

// GroupAlive reports no process on an unavailable backend.
func (*Child) GroupAlive() bool { return false }

// Close is harmless on an unavailable backend.
func (*Child) Close() error { return nil }
