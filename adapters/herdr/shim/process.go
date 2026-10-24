package shim

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Command is the complete spawn input, with no shell interpolation.
type Command struct {
	Argv, Env []string
	Dir       string
}

// Process is an owned process group, including any descendants of its leader.
type Process interface {
	PID() int
	Wait() (status int, stderr string)
	Signal(syscall.Signal) error
}

// Spawn starts a process in a new group and drains bounded stderr. Stdout is
// discarded, never mixed into the shim's stdout protocol channel.
func Spawn(c Command) (Process, error) {
	if len(c.Argv) == 0 {
		return nil, os.ErrInvalid
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Env, cmd.Dir = c.Env, c.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tail := &tailBuffer{}
	cmd.Stderr = tail
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	cmd.Stdout = null
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	return &process{cmd: cmd, tail: tail}, nil
}

type process struct {
	cmd  *exec.Cmd
	tail *tailBuffer
}

func (p *process) PID() int                      { return p.cmd.Process.Pid }
func (p *process) GroupAlive() bool              { return syscall.Kill(-p.PID(), 0) != syscall.ESRCH }
func (p *process) Signal(s syscall.Signal) error { return syscall.Kill(-p.PID(), s) }
func (p *process) Wait() (int, string) {
	err := p.cmd.Wait()
	status := 0
	if err != nil {
		status = p.cmd.ProcessState.ExitCode()
		if status < 0 {
			status = 128 + int(p.cmd.ProcessState.Sys().(syscall.WaitStatus).Signal())
		}
	}
	return status, p.tail.String()
}

type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 4096 {
		b.data = b.data[len(b.data)-4096:]
	}
	return len(p), nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

var _ io.Writer = (*tailBuffer)(nil)
