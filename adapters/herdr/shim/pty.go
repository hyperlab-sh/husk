package shim

import (
	"io"
	"os/exec"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/ptyhost"
)

// TerminalProcess is a process group whose terminal master is interruptible on
// close. Pump is its sole output reader; input is already decoded before Write.
type TerminalProcess interface {
	Process
	io.WriteCloser
	Pump(protocol.SurfaceID, func(protocol.TerminalWriteParams) error) error
	Resize(int, int) error
}
type terminalProcess struct{ *ptyhost.Child }

// SpawnTerminal starts argv through the shared plugin-side PTY implementation.
func SpawnTerminal(c Command, width, height int) (TerminalProcess, error) {
	child, err := ptyhost.Start(c.Argv, ptyhost.Options{Width: width, Height: height, Env: c.Env, Dir: c.Dir})
	if err != nil {
		return nil, err
	}
	return &terminalProcess{child}, nil
}
func (p *terminalProcess) Close() error { return p.Child.CloseTerminal() }
func (p *terminalProcess) Wait() (int, string) {
	err := p.Child.Wait()
	if err == nil {
		return 0, ""
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), ""
	}
	return 1, err.Error()
}
