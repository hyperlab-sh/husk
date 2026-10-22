package pods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/pods/decoration"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Host is the loop-owned set of Pods. Only token resolution/revocation and the
// ReplySink posting methods are worker-safe; all other methods run on the loop.
// Stop returns a command that performs bounded waits away from the loop.
type Host struct {
	loop      Loop
	opts      Options
	ctx       context.Context
	cancel    context.CancelFunc
	pods      map[string]*Pod
	gates     map[uint64][]string
	preparing map[string]bool
	nextGate  uint64
	closing   bool
	tokenMu   sync.RWMutex
	tokens    map[string]string
	byPlugin  map[string]string
}

// New constructs a Host without launching processes or writing State.
func New(loop Loop, opts Options) (*Host, error) {
	if loop == nil {
		return nil, errors.New("pods: Loop is required")
	}
	ctx := opts.Wrapper.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	if opts.Wrapper.Clock == nil {
		opts.Wrapper.Clock = wrapper.SystemClock{}
	}
	if opts.Wrapper.StopGrace == 0 {
		opts.Wrapper.StopGrace = wrapper.StopGrace
	}
	if opts.Wrapper.KillWait == 0 {
		opts.Wrapper.KillWait = wrapper.KillWait
	}
	if opts.Wrapper.StopGrace < 0 || opts.Wrapper.KillWait < 0 {
		cancel()
		return nil, errors.New("pods: negative shutdown grace")
	}
	if opts.Factory == nil {
		opts.Factory = func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) { return wrapper.New(s, o) }
	}
	return &Host{loop: loop, opts: opts, ctx: ctx, cancel: cancel, pods: make(map[string]*Pod), gates: make(map[uint64][]string), preparing: make(map[string]bool), tokens: make(map[string]string), byPlugin: make(map[string]string)}, nil
}

// Add registers a starting Pod but never starts it. Duplicate ids fail without
// replacing the first Pod; with a PodRegistry the Reserve conflict is that
// answer. Call Prepare once after adding a startup batch.
func (h *Host) Add(spec manifest.Spec) (*Pod, error) {
	if h.closing {
		return nil, errors.New("pods: host is stopping")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if h.opts.Registry != nil {
		if err := h.opts.Registry.Reserve(h.ctx, spec.ID); err != nil {
			return nil, err
		}
	} else if _, ok := h.pods[spec.ID]; ok {
		return nil, errors.New("duplicate plugin id")
	}
	p := &Pod{spec: spec.Clone(), info: wrapper.Info{PluginID: spec.ID, Status: wrapper.Starting}, Decoration: decoration.PassThrough}
	h.pods[spec.ID] = p
	h.loop.Liveness(LivenessMsg{Info: p.Info()})
	return p, nil
}

// Prepare snapshots all unprepared starting Pods into one reload gate. The
// returned worker may block, but only Handle(PreparedMsg) starts wrappers on the
// loop. Repeated calls while a gate is in flight do not duplicate preparation.
func (h *Host) Prepare() tea.Cmd {
	if h.closing {
		return nil
	}
	var ids []string
	for id, p := range h.pods {
		if p.info.Status == wrapper.Starting && !h.preparing[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	var specs []manifest.Spec
	for _, id := range ids {
		h.preparing[id] = true
		specs = append(specs, h.pods[id].spec.Clone())
	}
	h.nextGate++
	gate := h.nextGate
	h.gates[gate] = ids
	return func() tea.Msg { return PreparedMsg{Gate: gate, Preparation: h.loop.Prepare(h.ctx, specs)} }
}

// Start is launch step 6's built-in WrapperStarter implementation, over the
// Factory and the Wrapper: build the process, register its token before Start
// so even an immediate exit revokes it, then report live when the Wrapper says
// so. A Start failure is reported by the Wrapper's async LaunchFailedMsg, so
// Start returns nil then; only construction failures return synchronously.
// Loop-only.
func (h *Host) Start(id string, opening []protocol.Message) error {
	p, ok := h.pods[id]
	if !ok {
		return fmt.Errorf("pods: unknown plugin %q", id)
	}
	opts := h.opts.Wrapper
	opts.Context = h.ctx
	opts.Post = h.postWorker
	opts.Replies = h
	process, err := h.opts.Factory(p.spec.Clone(), opts)
	if err != nil {
		return err
	}
	p.process = process
	h.register(id, process.Token()) // before Start: even an immediate exit revokes it
	if err := process.Start(opening); err != nil {
		h.revoke(id)
		// Real wrappers post LaunchFailedMsg after Closed; fakes must obey
		// the same contract. Do not bypass that message ordering here.
		return nil
	}
	p.info = process.Info()
	if p.info.Status == wrapper.Live {
		h.loop.Liveness(LivenessMsg{Info: p.Info()})
	}
	return nil
}

// Handle applies lifecycle messages on the loop. ClosedMsg and calls.ReplyMsg
// remain the caller's responsibility: fail/deliver Calls before applying a later
// exit message. The ordering is enforced by the Wrapper's once-only Closed post.
func (h *Host) Handle(msg tea.Msg) bool {
	switch m := msg.(type) {
	case PreparedMsg:
		ids, ok := h.gates[m.Gate]
		if !ok {
			return true
		}
		delete(h.gates, m.Gate)
		if h.closing {
			return true
		}
		if m.Preparation.ReloadError != nil {
			h.loop.Log(LogMsg{Level: "warn", Message: "plugin preparation reload completed with errors: " + m.Preparation.ReloadError.Error()})
		}
		// Step 4 on the loop: apply the State writes the worker marshaled
		// back. A write failure fails that plugin; the batch continues.
		if len(m.Preparation.Register) > 0 {
			for _, id := range slices.Sorted(maps.Keys(m.Preparation.Register)) {
				if err := m.Preparation.Register[id](); err != nil {
					if m.Preparation.Failed == nil {
						m.Preparation.Failed = make(map[string]error)
					}
					m.Preparation.Failed[id] = fmt.Errorf("slice registration: %w", err)
					h.Handle(LaunchFailedMsg{PluginID: id, Err: m.Preparation.Failed[id]})
				}
			}
		}
		for _, id := range ids {
			p := h.pods[id]
			if err := m.Preparation.Failed[id]; err != nil {
				h.Handle(LaunchFailedMsg{PluginID: id, Err: err})
				continue
			}
			if h.opts.Starter != nil {
				// An injected Starter owns the whole step: a nil error means the
				// process started, so write live here because there is no
				// Wrapper to report it.
				if err := h.opts.Starter.Start(id, h.loop.Opening(id)); err != nil {
					h.Handle(LaunchFailedMsg{PluginID: id, Err: err})
					continue
				}
				p.info.Status = wrapper.Live
				h.loop.Liveness(LivenessMsg{Info: p.Info()})
				continue
			}
			if err := h.Start(id, h.loop.Opening(id)); err != nil {
				h.Handle(LaunchFailedMsg{PluginID: id, Err: err})
			}
		}
		return true
	case LaunchFailedMsg:
		p := h.pods[m.PluginID]
		if p == nil {
			return true
		}
		h.revoke(m.PluginID)
		p.info.Status = wrapper.Failed
		p.info.LogPath = m.LogPath
		p.info.LastError = m.Err.Error()
		h.loop.Liveness(LivenessMsg{Info: p.Info()})
		h.loop.Log(LogMsg{PluginID: m.PluginID, Level: "error", Message: fmt.Sprintf("failed to launch plugin %s: %v", m.PluginID, m.Err)})
		return true
	case ExitMsg:
		p := h.pods[m.Info.PluginID]
		if p == nil {
			return true
		}
		h.revoke(m.Info.PluginID)
		p.info = m.Info
		p.info.StderrTail = slices.Clone(m.Info.StderrTail)
		h.loop.Liveness(LivenessMsg{Info: p.Info()})
		level := "info"
		if p.info.Status == wrapper.Failed {
			level = "warn"
		}
		h.loop.Log(LogMsg{PluginID: m.Info.PluginID, Level: level, Message: fmt.Sprintf("plugin %s exited (%s); stderr log: %s", m.Info.PluginID, m.Info.ExitStatus, m.Info.LogPath)})
		for _, line := range m.Info.StderrTail {
			h.loop.Log(LogMsg{PluginID: m.Info.PluginID, Level: level, Message: "plugin " + m.Info.PluginID + " stderr: " + line})
		}
		return true
	case wrapper.ReapedMsg, wrapper.ChannelClosedMsg:
		return true // authentication was revoked by the worker-safe bridge
	case LogMsg:
		h.loop.Log(m)
		return true
	case StoppedMsg:
		if m.Err != nil {
			h.loop.Log(LogMsg{Level: "warn", Message: m.Err.Error()})
		}
		return true
	default:
		return false
	}
}

// Pod returns a loop-owned Pod for per-subscriber notification delivery.
func (h *Host) Pod(id string) (*Pod, bool) { p, ok := h.pods[id]; return p, ok }

// Send implements calls.Outbox without importing or mutating its pending table.
func (h *Host) Send(plugin calls.PluginID, m protocol.Message) error {
	p := h.pods[string(plugin)]
	if p == nil {
		return wrapper.ErrNotLive
	}
	return p.send(m)
}

// Notify broadcasts a notification to every live Pod in stable id order. A Pod
// itself is the targeted Sink normally handed to the Event bus.
func (h *Host) Notify(method string, params any) error {
	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			return err
		}
	}
	var ids []string
	for id, p := range h.pods {
		if p.info.Status == wrapper.Live {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var errs []error
	for _, id := range ids {
		if err := h.pods[id].send(protocol.Message{Method: method, Params: raw}); err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// Resolve is worker-safe and implements the Server's token-resolver seam.
func (h *Host) Resolve(token string) (string, bool) {
	h.tokenMu.RLock()
	defer h.tokenMu.RUnlock()
	id, ok := h.tokens[token]
	return id, ok
}
func (h *Host) register(id, token string) {
	if token == "" {
		return
	}
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	h.tokens[token] = id
	h.byPlugin[id] = token
}
func (h *Host) revoke(id string) {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	if token, ok := h.byPlugin[id]; ok {
		delete(h.tokens, token)
		delete(h.byPlugin, id)
	}
}

func (h *Host) postWorker(msg tea.Msg) {
	switch m := msg.(type) {
	case wrapper.ReapedMsg:
		h.revoke(m.PluginID)
	case wrapper.ChannelClosedMsg:
		h.revoke(m.PluginID)
	}
	h.loop.Post(msg)
}

// Reply is the worker-safe ReplySink bridge: it only posts loop input.
func (h *Host) Reply(id string, m protocol.Message) {
	h.loop.Post(calls.ReplyMsg{Plugin: calls.PluginID(id), Reply: m})
}

// Closed revokes the credential immediately and posts a once-only wrapper
// notice; the loop, not this worker callback, calls Calls.DropPlugin.
func (h *Host) Closed(id string, reason error) {
	h.revoke(id)
	h.loop.Post(ClosedMsg{PluginID: id, Reason: reason})
}

// Stop marks shutdown on the loop, then returns one worker command that flushes
// stdin, cancels the shared context, signals groups and uses one 2s grace plus
// one 1s kill wait across all wrappers. The caller closes Calls and Server first.
func (h *Host) Stop() tea.Cmd {
	if h.closing {
		return nil
	}
	h.closing = true
	var processes []wrapper.Process
	for _, p := range h.pods {
		if p.process != nil {
			select {
			case <-p.process.Done():
				continue
			default:
			}
			p.process.MarkShutdown()
			processes = append(processes, p.process)
		}
	}
	return func() tea.Msg {
		stopAt := h.opts.Wrapper.Clock.Now().Add(h.opts.Wrapper.StopGrace)
		for _, p := range processes {
			p.CloseStdin()
		}
		h.cancel()
		for _, p := range processes {
			_ = p.Terminate()
		}
		if h.waitAll(processes, stopAt) {
			return StoppedMsg{}
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
		if h.waitAll(processes, killAt) {
			return StoppedMsg{}
		}
		return StoppedMsg{Err: errors.New("plugins did not exit within the shared kill wait")}
	}
}
func (h *Host) waitAll(processes []wrapper.Process, until time.Time) bool {
	var pending []wrapper.Process
	for _, p := range processes {
		select {
		case <-p.Done():
		default:
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return true
	}
	delay := until.Sub(h.opts.Wrapper.Clock.Now())
	if delay <= 0 {
		return false
	}
	deadline := make(chan struct{})
	timer := h.opts.Wrapper.Clock.AfterFunc(delay, func() { close(deadline) })
	defer timer.Stop()
	for _, p := range pending {
		select {
		case <-p.Done():
			continue
		case <-deadline:
			return false
		}
	}
	return true
}

var _ calls.Outbox = (*Host)(nil)
var _ wrapper.ReplySink = (*Host)(nil)
