//go:build !linux && !darwin

package ptyhost

import (
	"strings"
	"syscall"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPTYUnsupportedErrors(t *testing.T) {
	c := &Child{}
	t.Run("Start", func(t *testing.T) {
		child, err := Start([]string{"unused"}, Options{Width: 40, Height: 10, Dir: "unused"})
		if child != nil {
			t.Fatal("unsupported Start returned a child")
		}
		requireUnsupported(t, err)
	})
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Pump", func() error {
			return c.Pump(protocol.SurfaceMainStage, func(protocol.TerminalWriteParams) error { t.Error("unsupported Pump called sender"); return nil })
		}},
		{"Input", func() error { return c.Input(protocol.TerminalInputParams{}) }},
		{"Resize", func() error { return c.Resize(40, 10) }},
		{"Wait", c.Wait},
		{"Signal", func() error { return c.Signal(syscall.SIGTERM) }},
	} {
		t.Run(tc.name, func(t *testing.T) { requireUnsupported(t, tc.call()) })
	}
	t.Run("Write", func(t *testing.T) {
		n, err := c.Write([]byte("input"))
		if n != 0 {
			t.Fatalf("unsupported Write count=%d", n)
		}
		requireUnsupported(t, err)
	})
}

func requireUnsupported(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "PTY hosting supports Linux and macOS") {
		t.Fatalf("unsupported error=%v", err)
	}
}

func TestPTYUnsupportedInertValues(t *testing.T) {
	c := &Child{}
	if c.PID() != 0 {
		t.Fatal("unsupported backend has a process id")
	}
	if c.GroupAlive() {
		t.Fatal("unsupported backend has a live group")
	}
	if err := c.CloseTerminal(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
