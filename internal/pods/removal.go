package pods

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// unreapedError is the last_error of a process that survived SIGKILL.
const unreapedError = "did not exit within 1s of SIGKILL"

// removal is one plugin.remove in progress (pods.md "Removing a plugin"): a
// per-id entry on the Host, created at step 2 and deleted when the reload of
// step 7 completes. The Host mutates it on the loop alone. Handlers hold only
// done and read the result fields after it closes; nothing writes them later.
type removal struct {
	done    chan struct{}
	status  contracts.PodStatus
	log     string
	reports []contracts.ReloadReport
	err     error
}

// complete records the outcome, wakes the waiting handlers and is the only
// place an entry's done channel closes. Loop-only.
func (h *Host) complete(id string, rm *removal, reports []contracts.ReloadReport, err error) {
	rm.reports, rm.err = reports, err
	close(rm.done)
	delete(h.removing, id)
}

func shutdownCancelled() error {
	return contracts.NewError(protocol.CodeCancelled, "husk is shutting down", map[string]any{"reason": "shutdown"})
}

// Remove serves plugin.remove: steps 1 and 2 on the handler goroutine and the
// loop, then step 8 off the loop under ctx. A ctx that ends after step 2
// detaches this caller only; the removal runs to completion.
func (h *Host) Remove(ctx context.Context, c contracts.Caller, pluginID string) (contracts.PluginRemoval, error) {
	if c.Kind != contracts.CallerCLI && c.Kind != contracts.CallerHost {
		return contracts.PluginRemoval{}, contracts.NewError(protocol.CodeDenied, "plugin.remove unloads a plugin from the user's instance; only CLI and host callers may do it", nil)
	}
	if pluginID == "" {
		return contracts.PluginRemoval{}, contracts.NewError(protocol.CodeInvalidParams, "params.plugin is required", nil)
	}
	var rm *removal
	var refusal error
	if err := h.loop.Do(ctx, func() { rm, refusal = h.beginRemove(pluginID) }); err != nil {
		if ctx.Err() != nil {
			return contracts.PluginRemoval{}, ctx.Err() // the caller went away; the reply is dropped
		}
		return contracts.PluginRemoval{}, contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil)
	}
	if refusal != nil {
		return contracts.PluginRemoval{}, refusal
	}
	select {
	case <-rm.done:
	case <-ctx.Done():
		return contracts.PluginRemoval{}, ctx.Err()
	}
	if rm.err != nil {
		return contracts.PluginRemoval{}, rm.err
	}
	return contracts.PluginRemoval{Plugin: pluginID, Status: rm.status, Log: rm.log, Reports: rm.reports}, nil
}

// beginRemove is step 2 and the dispatch of step 3. Loop-only; it never posts.
func (h *Host) beginRemove(id string) (*removal, error) {
	if rm, ok := h.removing[id]; ok {
		return rm, nil // join: no second stop, teardown or reload
	}
	if h.closing {
		return nil, contracts.NewError(protocol.CodeUnavailable, "husk is shutting down", map[string]any{"reason": "shutdown"})
	}
	p, ok := h.pods[id]
	if !ok {
		return nil, contracts.NewError(protocol.CodeNotFound, "unknown plugin id "+id, nil)
	}
	rm := &removal{done: make(chan struct{})}
	h.removing[id] = rm
	switch p.info.Status {
	case wrapper.Live:
		h.spawn(h.stopOne(p))
	case wrapper.Starting:
		// The launch gate's completion handler tears it down without starting it.
	default:
		h.teardown(id, false) // exited or failed: no process, same tick
	}
	return rm, nil
}

// stopOne is step 4: the shutdown stop for one plugin. It is marked stopped
// by Husk on the loop so its exit classifies exited; the worker ends by
// returning TeardownMsg.
func (h *Host) stopOne(p *Pod) tea.Cmd {
	id, process := p.spec.ID, p.process
	if process != nil {
		select {
		case <-process.Done():
			process = nil
		default:
			process.MarkShutdown()
		}
	}
	return func() tea.Msg {
		msg := TeardownMsg{PluginID: id}
		if process != nil {
			msg.Unreaped = !h.stopProcesses([]wrapper.Process{process}, nil)
		}
		return msg
	}
}

// teardown is steps 5 and 6 in one tick: the TeardownMsg handler, also called
// directly by step 3's inline paths. It runs only for an id with a removing
// entry and not after the shutdown mark.
func (h *Host) teardown(id string, unreaped bool) {
	rm, ok := h.removing[id]
	p := h.pods[id]
	if h.closing || !ok || p == nil {
		return
	}
	h.revoke(id)
	h.handlesMu.Lock()
	handle, hasHandle := h.handles[id]
	delete(h.handles, id)
	h.handlesMu.Unlock()
	if hasHandle {
		handle.Unregister()
	}
	switch {
	case unreaped:
		p.info.Status, p.info.LastError = wrapper.Failed, unreapedError
		h.loop.Liveness(LivenessMsg{Info: p.Info()})
		h.loop.Log(LogMsg{PluginID: id, Level: "warn", Message: fmt.Sprintf("plugin %s %s; removal continues", id, unreapedError)})
	case p.info.Status == wrapper.Live:
		// A Starter-owned process: Husk holds nothing to wait for, and the
		// token and pipes are already gone.
		p.info.Status = wrapper.Exited
		h.loop.Liveness(LivenessMsg{Info: p.Info()})
		h.publishExited(p.Info())
	}
	rm.status, rm.log = contracts.PodStatus(p.info.Status), p.info.LogPath
	delete(h.pods, id)
	delete(h.preparing, id)
	h.dropFromGates(id)
	if h.opts.Registry != nil {
		h.opts.Registry.Release(h.ctx, id)
	}
	h.loop.Log(LogMsg{PluginID: id, Level: "info", Message: "removed plugin " + id + ": slices unregistered, documents undeclared"})
	if h.opts.Workers != nil {
		h.opts.Workers.Undeclare(id)
	}
	if h.opts.Publisher != nil {
		h.opts.Publisher.Publish(PluginRemovedEvent, map[string]any{"plugin_id": id, "status": string(rm.status)})
	}
	h.spawn(h.reloadOne(id))
}

// dropFromGates removes id from every pending launch gate, so a later
// PreparedMsg neither starts the removed plugin nor dereferences its Pod.
func (h *Host) dropFromGates(id string) {
	for gate, ids := range h.gates {
		kept := ids[:0]
		for _, other := range ids {
			if other != id {
				kept = append(kept, other)
			}
		}
		if len(kept) == 0 {
			delete(h.gates, gate)
		} else {
			h.gates[gate] = kept
		}
	}
}

// reloadOne is step 7: one Services reload under the Host's context, never a
// request's. A reload that completed, applied or not, is a success; only one
// cut short with no report is the shutdown cancellation.
func (h *Host) reloadOne(id string) tea.Cmd {
	return func() tea.Msg {
		msg := RemoveDoneMsg{PluginID: id, Reports: []contracts.ReloadReport{}}
		if h.opts.Workers == nil {
			return msg
		}
		reports, err := h.opts.Workers.Reload(h.ctx)
		if len(reports) > 0 {
			msg.Reports = reports
		} else if err != nil {
			// No report means no reload completed: the Coordinator's context
			// ended (shutdown). Anything else is not a cancellation.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				msg.Err = shutdownCancelled()
			} else {
				msg.Err = contracts.NewError(protocol.CodeInternal, "services reload failed: "+err.Error(), nil)
			}
		}
		return msg
	}
}

// finishRemoval is the RemoveDoneMsg handler. Loop-only.
func (h *Host) finishRemoval(m RemoveDoneMsg) {
	rm, ok := h.removing[m.PluginID]
	if h.closing || !ok {
		return
	}
	h.complete(m.PluginID, rm, m.Reports, m.Err)
}

// cancelRemovals completes every removing entry with cancelled/shutdown in
// the tick that marks shutdown (invariant 7). Loop-only.
func (h *Host) cancelRemovals() {
	for id, rm := range h.removing {
		h.complete(id, rm, nil, shutdownCancelled())
	}
}

// spawn starts a worker command: through the composition root's tracked
// workers when wired, else a plain goroutine posting through the Loop.
func (h *Host) spawn(cmd tea.Cmd) {
	if h.opts.Workers != nil {
		h.opts.Workers.Go(cmd)
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			h.loop.Post(msg)
		}
	}()
}

// stopProcesses is the shutdown stop policy over a set of processes: stdin
// EOF, then (after beforeTerminate, if any) SIGTERM, SIGKILL for survivors
// after the stop grace, and at most the kill wait more. It reports whether
// every process was reaped. Workers only.
func (h *Host) stopProcesses(processes []wrapper.Process, beforeTerminate func()) bool {
	stopAt := h.opts.Wrapper.Clock.Now().Add(h.opts.Wrapper.StopGrace)
	for _, p := range processes {
		p.CloseStdin()
	}
	if beforeTerminate != nil {
		beforeTerminate()
	}
	for _, p := range processes {
		_ = p.Terminate()
	}
	if h.waitAll(processes, stopAt) {
		return true
	}
	killAt := h.opts.Wrapper.Clock.Now().Add(h.opts.Wrapper.KillWait)
	for _, p := range processes {
		select {
		case <-p.Done():
		default:
			if err := p.Kill(); !errors.Is(err, os.ErrProcessDone) {
				h.loop.Post(LogMsg{PluginID: p.PluginID(), Level: "warn", Message: fmt.Sprintf("plugin %s did not exit within %s of SIGTERM; killing it", p.PluginID(), h.opts.Wrapper.StopGrace)})
			}
		}
	}
	return h.waitAll(processes, killAt)
}
