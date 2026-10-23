package pods

import (
	"errors"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type droppableProcess struct {
	*fakeProcess
	tried int
}

func (p *droppableProcess) TrySend(protocol.Message) error {
	p.tried++
	return protocol.ErrSendQueueFull
}

func TestTerminalQueueSenderCapturesDroppableProcess(t *testing.T) {
	process := &droppableProcess{fakeProcess: &fakeProcess{}}
	pod := &Pod{process: process}
	send := pod.QueueSender()
	pod.process = nil // the worker closure must not read the mutable Pod again
	if err := send(protocol.Message{Method: protocol.MethodTerminalInput}); !errors.Is(err, protocol.ErrSendQueueFull) {
		t.Fatalf("offer: %v", err)
	}
	if process.tried != 1 || len(process.sent) != 0 {
		t.Fatal("terminal input used the fatal Send path")
	}
}
