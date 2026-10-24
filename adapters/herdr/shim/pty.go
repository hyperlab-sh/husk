package shim

import (
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// TerminalProcess is a process group whose terminal master is interruptible on close.
type TerminalProcess interface {
	Process
	io.ReadWriteCloser
	Resize(int, int) error
}

type terminalProcess struct {
	*process
	file *os.File
}

// SpawnTerminal starts argv in its own PTY session, using the supplied environment.
func SpawnTerminal(c Command, width, height int) (TerminalProcess, error) {
	if len(c.Argv) == 0 || width < 1 || height < 1 || width > 65535 || height > 65535 {
		return nil, os.ErrInvalid
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Env = c.Env
	cmd.Dir = c.Dir
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	if err != nil {
		return nil, err
	}
	// Go's poller must own the master: closing a blocking descriptor cannot
	// interrupt a reader or a writer already blocked inside the kernel.
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		err = syscall.SetNonblock(fd, true)
	}
	if err != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		_ = f.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, err
	}
	name := f.Name()
	_ = f.Close()
	return &terminalProcess{process: &process{cmd: cmd, tail: &tailBuffer{}}, file: os.NewFile(uintptr(fd), name)}, nil
}
func (p *terminalProcess) Read(b []byte) (int, error)  { return p.file.Read(b) }
func (p *terminalProcess) Write(b []byte) (int, error) { return p.file.Write(b) }
func (p *terminalProcess) Close() error                { return p.file.Close() }
func (p *terminalProcess) Resize(w, h int) error {
	if w < 1 || h < 1 || w > 65535 || h > 65535 {
		return os.ErrInvalid
	}
	return pty.Setsize(p.file, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)})
}
