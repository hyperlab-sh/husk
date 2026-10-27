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
	"time"

	"github.com/creack/pty"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"golang.org/x/sys/unix"
)

// Child owns a session leader, its PTY, and a single process reaper.
type Child struct {
	file  *os.File
	cmd   *exec.Cmd
	done  chan struct{}
	err   error
	once  sync.Once
	grace time.Duration
	after func(time.Duration) <-chan time.Time
}

// Start launches argv on a new session's PTY; it never invokes a shell implicitly.
func Start(argv []string, o Options) (*Child, error) {
	if len(argv) == 0 || argv[0] == "" || o.Width < 1 || o.Height < 1 || o.Width > 65535 || o.Height > 65535 {
		return nil, errors.New("invalid terminal command or geometry")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = o.Dir
	parentDeath(cmd)
	if o.StopGrace <= 0 {
		o.StopGrace = 500 * time.Millisecond
	}
	if o.After == nil {
		o.After = time.After
	}
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = Environment(env)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(o.Width), Rows: uint16(o.Height)})
	if err != nil {
		return nil, err
	}
	// creack/pty returns a blocking descriptor. Rewrap a nonblocking duplicate
	// so Go's poller can interrupt both a saturated write and a pending read
	// when Close closes the master. Merely closing a blocking Unix fd does not
	// cancel a write already inside the kernel.
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
	f = os.NewFile(uintptr(fd), name)
	c := &Child{file: f, cmd: cmd, done: make(chan struct{}), grace: o.StopGrace, after: o.After}
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
	_, err = c.Write(b)
	return err
}

// Resize applies the host's inner dimensions to the child terminal.
func (c *Child) Resize(w, h int) error {
	if w < 1 || h < 1 || w > 65535 || h > 65535 {
		return errors.New("invalid terminal geometry")
	}
	return pty.Setsize(c.file, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)})
}

// PID returns the session leader's process id.
func (c *Child) PID() int { return c.cmd.Process.Pid }

// Write writes opaque encoded input to the terminal master.
func (c *Child) Write(b []byte) (int, error) { return c.file.Write(b) }

// CloseTerminal interrupts I/O without waiting for process shutdown. Callers
// managing a group themselves must still signal and reap it, or call Close.
func (c *Child) CloseTerminal() error { return c.file.Close() }

// Signal sends a signal to the entire child process group.
func (c *Child) Signal(s syscall.Signal) error { return syscall.Kill(-c.PID(), s) }

// GroupAlive reports whether the process group still exists.
func (c *Child) GroupAlive() bool {
	err := c.Signal(0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Wait returns the session leader's exit result. Concurrent callers share it.
func (c *Child) Wait() error { <-c.done; return c.err }

// Close hangs up the entire child process group, including background children,
// and closes the PTY so a blocked output pump is released. Wait reaps the leader.
func (c *Child) Close() error {
	var err error
	c.once.Do(func() {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGHUP)
		err = c.file.Close()
		select {
		case <-c.done:
			return
		default:
		}
		select {
		case <-c.done:
			return
		case <-c.after(c.grace):
		}
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		<-c.done
	})
	return err
}
