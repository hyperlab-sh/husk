package shim

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
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
	// A descendant can retain stderr after its leader exits. Bound exec's copy
	// join so leader completion releases the command slot independently of it.
	cmd.WaitDelay = 2 * time.Second
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
	cmd         *exec.Cmd
	tail        *tailBuffer
	groupMu     sync.Mutex
	groupEmpty  bool
	groupSignal func(int, syscall.Signal) error
}

func (p *process) PID() int { return p.cmd.Process.Pid }
func (p *process) GroupAlive() bool {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	return p.groupAlive()
}
func (p *process) groupAlive() bool {
	if p.groupEmpty {
		return false
	}
	if p.groupSignal == nil {
		p.groupSignal = syscall.Kill
	}
	if p.groupSignal(-p.PID(), 0) == syscall.ESRCH {
		p.groupEmpty = true
	}
	return !p.groupEmpty
}
func (p *process) Signal(s syscall.Signal) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	// Once observed empty, this group identity is retired forever, including
	// after shutdown stops the observer. Never signal a subsequently reused id.
	if !p.groupAlive() {
		return nil
	}
	err := p.groupSignal(-p.PID(), s)
	if err == syscall.ESRCH {
		p.groupEmpty = true
	}
	return err
}
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
