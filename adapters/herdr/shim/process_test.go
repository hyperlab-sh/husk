package shim

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The descendant holds inherited stderr until its control connection closes.
// It blocks on I/O, not a sleep, and stays in the spawned leader's group.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 {
		switch os.Args[1] {
		case "--stderr-leader":
			child := exec.Command(os.Args[0], "--stderr-holder", os.Args[2])
			child.Stderr, child.Stdout = os.Stderr, os.Stdout
			if child.Start() != nil {
				os.Exit(2)
			}
			os.Exit(0)
		case "--stderr-holder":
			c, err := net.Dial("unix", os.Args[2])
			if err != nil {
				os.Exit(3)
			}
			_, _ = c.Write([]byte{1})
			_, _ = io.Copy(io.Discard, c)
			_ = c.Close()
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestSpawnWaitIsBoundedWhenDescendantRetainsStderr(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "c.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	exe, _ := os.Executable()
	p, err := Spawn(Command{Argv: []string{exe, "--stderr-leader", listener.Addr().String()}, Env: []string{"GORACE=atexit_sleep_ms=0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Signal(syscall.SIGKILL)
	c := take(t, accepted)
	defer c.Close()
	var ready [1]byte
	if _, err := io.ReadFull(c, ready[:]); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { status, _ := p.Wait(); done <- status }()
	select {
	case status := <-done:
		if status != 0 {
			t.Fatalf("successful leader exit became %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader wait blocked on a descendant's inherited stderr")
	}
}

func TestExitedLeaderReleasesCommandCapWhileGroupIsTracked(t *testing.T) {
	r := newRig(t, "never", false, "")
	original := r.o.Spawn
	group := &descendantGroup{testProcess: &testProcess{pid: 42, done: make(chan int, 1), signals: make(chan syscall.Signal, 4)}, alive: true}
	calls := 0
	r.o.Spawn = func(c Command) (Process, error) {
		calls++
		if calls == 32 {
			return group, nil
		}
		return original(c)
	}
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	for range 32 {
		if reply := r.invoke(t, "demo.go"); reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	for {
		var s struct{ Value state }
		_ = json.Unmarshal(r.host.until(t, protocol.MethodStateSet, "attached"), &s)
		if s.Value.Commands == 32 {
			break
		}
	}
	group.done <- 0
	for {
		var s struct{ Value state }
		_ = json.Unmarshal(r.host.until(t, protocol.MethodStateSet, "attached"), &s)
		if s.Value.Commands == 31 {
			break
		}
	}
	defer func() {
		group.mu.Lock()
		group.alive = false
		group.mu.Unlock()
		r.clock.fire(t, 50*time.Millisecond)
	}()
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatalf("exited leader retained command-cap slot: %v", reply.Error)
	}
}

func TestObservedEmptyProcessGroupIsNeverSignalledAgain(t *testing.T) {
	alive := true
	probes, signals := 0, 0
	p := &process{cmd: &exec.Cmd{Process: &os.Process{Pid: 42}}, groupSignal: func(pgid int, signal syscall.Signal) error {
		if pgid != -42 {
			t.Fatalf("wrong group %d", pgid)
		}
		if signal != 0 {
			signals++
			return nil
		}
		probes++
		if !alive {
			return syscall.ESRCH
		}
		return nil
	}}
	if !p.GroupAlive() {
		t.Fatal("live group reported empty")
	}
	alive = false
	if p.GroupAlive() {
		t.Fatal("empty group reported live")
	}
	alive = true // Simulate the kernel reusing the numeric process-group id.
	_ = p.Signal(syscall.SIGTERM)
	_ = p.Signal(syscall.SIGKILL)
	if p.GroupAlive() || probes != 2 || signals != 0 {
		t.Fatalf("retired group reused: probes=%d signals=%d", probes, signals)
	}
}
