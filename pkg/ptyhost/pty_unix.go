//go:build linux || darwin

package ptyhost

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Child owns a session leader, its PTY, and a single process reaper.
type Child struct {
	file *os.File
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	once sync.Once
}

// Start launches argv on a new session's PTY; it never invokes a shell implicitly.
func Start(argv []string, o Options) (*Child, error) {
	if len(argv) == 0 || argv[0] == "" || o.Width < 1 || o.Height < 1 || o.Width > 65535 || o.Height > 65535 {
		return nil, errors.New("invalid terminal command or geometry")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = Environment(env)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(o.Width), Rows: uint16(o.Height)})
	if err != nil {
		return nil, err
	}
	c := &Child{file: f, cmd: cmd, done: make(chan struct{})}
	go func() { c.err = cmd.Wait(); close(c.done) }()
	return c, nil
}

// Pump forwards bounded base64 chunks until the PTY closes. send must preserve
// order and provide its own bounded transport policy.
func (c *Child) Pump(surface protocol.SurfaceID, send func(protocol.TerminalWriteParams) error) error {
	b := make([]byte, protocol.MaxTerminalBytes)
	for {
		n, err := c.file.Read(b)
		if n > 0 {
			if e := send(protocol.TerminalWriteParams{Surface: surface, Data: base64.StdEncoding.EncodeToString(b[:n])}); e != nil {
				return e
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

// Input writes encoded terminal.input bytes to the PTY.
func (c *Child) Input(p protocol.TerminalInputParams) error {
	b, err := (protocol.TerminalWriteParams{Surface: p.Surface, Data: p.Data}).Decode()
	if err != nil {
		return err
	}
	_, err = c.file.Write(b)
	return err
}

// Resize applies the host's inner dimensions to the child terminal.
func (c *Child) Resize(w, h int) error {
	if w < 1 || h < 1 || w > 65535 || h > 65535 {
		return errors.New("invalid terminal geometry")
	}
	return pty.Setsize(c.file, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)})
}

// Wait returns the session leader's exit result. Concurrent callers share it.
func (c *Child) Wait() error { <-c.done; return c.err }

// Close hangs up the entire child process group, including background children,
// and closes the PTY so a blocked output pump is released. Wait reaps the leader.
func (c *Child) Close() error {
	var err error
	c.once.Do(func() { _ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGHUP); err = c.file.Close() })
	return err
}
