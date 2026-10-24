package shim

import (
	"encoding/json"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type descendantGroup struct {
	*testProcess
	mu    sync.Mutex
	alive bool
}

func (p *descendantGroup) GroupAlive() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.alive }
func (p *descendantGroup) Signal(s syscall.Signal) error {
	if s == syscall.SIGKILL {
		p.mu.Lock()
		p.alive = false
		p.mu.Unlock()
	}
	return p.testProcess.Signal(s)
}

func TestEOFStopsDescendantsAfterLeaderExit(t *testing.T) {
	r := newRig(t, "never", false, "")
	group := &descendantGroup{testProcess: &testProcess{pid: 42, done: make(chan int, 1), signals: make(chan syscall.Signal, 4)}, alive: true}
	r.o.Spawn = func(Command) (Process, error) { return group, nil }
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	if reply := r.invoke(t, "demo.go"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	group.done <- 0
	for {
		raw := r.host.until(t, protocol.MethodStateSet, "attached")
		var v struct{ Value state }
		_ = json.Unmarshal(raw, &v)
		if v.Value.Commands == 0 {
			break
		}
	}
	_ = r.in.Close()
	if got := take(t, group.signals); got != syscall.SIGTERM {
		t.Fatalf("descendants first signal=%v", got)
	}
	r.clock.fire(t, 2*time.Second)
	if got := take(t, group.signals); got != syscall.SIGKILL {
		t.Fatalf("descendants second signal=%v", got)
	}
}
