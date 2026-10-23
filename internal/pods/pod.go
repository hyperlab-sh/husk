package pods

import (
	"encoding/json"
	"github.com/hyperlab-sh/husk/internal/pods/decoration"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Pod ties immutable configuration, one wrapper and an undecided decoration
// slot together. Mutable metadata is confined to the owning Host's loop.
type Pod struct {
	spec    manifest.Spec
	process wrapper.Process
	info    wrapper.Info
	// openingFailed keeps the first terminal fact when a spawned child never
	// becomes live; its eventual ExitMsg only enriches process metadata.
	openingFailed bool
	// Decoration defaults to the pass-through boundary; no renderer is chosen.
	Decoration decoration.Function
}

// Spec returns detached configuration.
func (p *Pod) Spec() manifest.Spec { return p.spec.Clone() }

// Info returns detached loop-owned lifecycle metadata.
func (p *Pod) Info() wrapper.Info {
	v := p.info
	v.StderrTail = append([]string(nil), v.StderrTail...)
	return v
}

// ProcessDone is closed when the Pod's process has exited; nil before it was
// started. A test and frontend observation seam. Loop-only, like the Pod.
func (p *Pod) ProcessDone() <-chan struct{} {
	if p.process == nil {
		return nil
	}
	return p.process.Done()
}

// QueueSender captures the current process queue on the loop. The returned
// function is worker-safe and never consults mutable Pod metadata; a terminal
// input drain can therefore enqueue without reading the loop-owned Pod map.
func (p *Pod) QueueSender() func(protocol.Message) error {
	process := p.process
	return func(m protocol.Message) error {
		if process == nil {
			return wrapper.ErrNotLive
		}
		if sender, ok := process.(interface{ TrySend(protocol.Message) error }); ok {
			return sender.TrySend(m)
		}
		return protocol.ErrSendQueueFull
	}
}

// Notify implements the Message kinds Sink for this plugin alone.
func (p *Pod) Notify(method string, params any) error {
	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			return err
		}
	}
	return p.send(protocol.Message{Method: method, Params: raw})
}
func (p *Pod) send(m protocol.Message) error {
	if p.process == nil || p.info.Status != wrapper.Live {
		return wrapper.ErrNotLive
	}
	return p.process.Send(m)
}
