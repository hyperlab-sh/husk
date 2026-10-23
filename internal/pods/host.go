package pods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
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
	loop   Loop
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	pods   map[string]*Pod
	// handles holds each Pod's slice owner handle from RegisterSlicesMsg
	// until TeardownMsg, for teardown and the Stem State port's write path.
	// Registration and teardown mutate it on the loop, but the Stem write
	// path resolves handles off the loop (a Server handler goroutine resolves
	// the handle before posting the write itself), so this is the one map
	// guarded by a mutex instead of loop confinement; handlesMu covers only
	// the map's own read, assignment and delete, never the handles' methods.
	handles   map[string]contracts.SliceHandle
	handlesMu sync.Mutex
	gates     map[uint64][]string
	preparing map[string]bool
	nextGate  uint64
	closing   bool
	// removing holds one entry per plugin.remove in progress (removal.go).
	removing map[string]*removal
	tokenMu  sync.RWMutex
	tokens   map[string]string
	byPlugin map[string]string
	// worker, when set, replaces the worker start of goWorker (tests).
	worker func(func())
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
	return &Host{loop: loop, opts: opts, ctx: ctx, cancel: cancel, pods: make(map[string]*Pod), handles: make(map[string]contracts.SliceHandle), gates: make(map[uint64][]string), removing: make(map[string]*removal), preparing: make(map[string]bool), tokens: make(map[string]string), byPlugin: make(map[string]string)}, nil
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
	// The local map check holds regardless of the registry: a Pod already in
	// this Host must never be replaced (its credential would outlive the pod
	// record), even if a registry lost track of the id.
	if _, ok := h.pods[spec.ID]; ok {
		return nil, errors.New("duplicate plugin id")
	}
	if h.opts.Registry != nil {
		if err := h.opts.Registry.Reserve(h.ctx, spec.ID); err != nil {
			return nil, err
		}
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
	return func() tea.Msg { return PreparedMsg{Gate: gate, Preparation: h.loop.Prepare(h.ctx, gate, specs)} }
}

// Start is launch step 6's built-in WrapperStarter implementation, over the
// Factory and the Wrapper: build the process, register its token before Start
// so even an immediate exit revokes it, then report live when the Wrapper says
// so. Start is synchronous: nil is returned only once the process actually
// started (liveness `live` is written exactly then), and a Start failure is
// returned as the error — the caller reports the plugin failed from it, so a
// never-started Pod never reads live and is not left `starting`. The Wrapper
// still posts its own LaunchFailedMsg after the Closed notice for the same
// failure; the Host applies the first report and ignores duplicates of the
// same error. Loop-only.
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
		// The Wrapper opened the stderr log before cmd.Start, so even a
		// failed start carries its path; keep it for the failure liveness
		// (the async duplicate LaunchFailedMsg that would carry it is
		// dropped by the once-only rule).
		p.info.LogPath = process.Info().LogPath
		h.revoke(id)
		return err
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
		// Slice registrations were already applied on the loop through
		// RegisterSlicesMsg before the reload was requested; their failures
		// arrive in Failed and are handled once here, with the declaration
		// failures.
		for _, id := range ids {
			if _, removing := h.removing[id]; removing {
				// A removing Pod is never started: the gate has settled, so
				// step 3 hands it straight to the teardown, in this tick.
				h.teardown(id, false)
				continue
			}
			p, ok := h.pods[id]
			if !ok {
				// Torn down while this gate was pending: the starter must not
				// run for a removed Pod, and the dereference below would panic.
				continue
			}
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
	case RegisterSlicesMsg:
		// Step 4 on the loop, before the worker requests the reload: each
		// closure is a State write. The handle is kept for teardown and for
		// the Stem State port's write path; a failure fails only that plugin.
		if m.Done == nil {
			return true // nothing to acknowledge; a miswired worker
		}
		registered := Registered{
			Handles: make(map[string]contracts.SliceHandle, len(m.Register)),
		}
		fail := func(id string, err error) {
			if registered.Failed == nil {
				registered.Failed = make(map[string]error)
			}
			registered.Failed[id] = err
		}
		gateIDs, gateLive := h.gates[m.Gate]
		for _, id := range slices.Sorted(maps.Keys(m.Register)) {
			if h.closing {
				// Shutdown: no State writes from a late registration.
				fail(id, errors.New("shut down before slice registration"))
				continue
			}
			if !gateLive || !slices.Contains(gateIDs, id) {
				// The registration must belong to the launch gate that is still
				// pending. A Pod id alone proves nothing: the same id can be
				// re-added and re-prepared while a torn-down launch's worker is
				// still in flight, and running that stale closure would register
				// a slice the new launch owns — its own registration would then
				// fail as a duplicate — or leak a handle when the Pod is gone.
				// Refuse instead; the worker reports the failure, and the gate
				// that owns the id answers for it.
				fail(id, fmt.Errorf("slice registration for %s is not from the pending launch", id))
				continue
			}
			handle, err := m.Register[id]()
			if err != nil {
				fail(id, fmt.Errorf("slice registration: %w", err))
				continue
			}
			h.handlesMu.Lock()
			h.handles[id] = handle
			h.handlesMu.Unlock()
			registered.Handles[id] = handle
		}
		select {
		case m.Done <- registered:
		default: // the worker left (shutdown); its wait ended via ctx
		}
		return true
	case TeardownMsg:
		h.teardown(m.PluginID, m.Unreaped)
		return true
	case LegacyHelloMsg:
		h.failLegacy(m.PluginID)
		return true
	case RemoveDoneMsg:
		h.finishRemoval(m)
		return true
	case LaunchFailedMsg:
		p := h.pods[m.PluginID]
		if p == nil {
			return true
		}
		// A start failure may be reported by both the synchronous starter
		// path and the process's own async notice; apply the first report only.
		if p.info.Status == wrapper.Failed && p.info.LastError == m.Err.Error() {
			return true
		}
		h.revoke(m.PluginID)
		p.info.Status = wrapper.Failed
		if m.LogPath != "" {
			// Only a notice that actually carries a path overwrites one; the
			// synchronous failure report has none and must not clobber the
			// path Start copied from the Wrapper.
			p.info.LogPath = m.LogPath
		}
		p.info.LastError = m.Err.Error()
		h.loop.Liveness(LivenessMsg{Info: p.Info()})
		h.loop.Log(LogMsg{PluginID: m.PluginID, Level: "error", Message: fmt.Sprintf("failed to launch plugin %s: %v", m.PluginID, m.Err)})
		h.publishExited(p.Info())
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
		h.publishExited(p.Info())
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

// publishExited publishes pods.plugin-exited for a Pod that left live or never
// got there. The payload is the Pod's husk.pods entry (status, pid,
// started_at, last_error, log; absent fields omitted) plus plugin_id.
func (h *Host) publishExited(info wrapper.Info) {
	if h.opts.Publisher == nil {
		return
	}
	payload := map[string]any{"plugin_id": info.PluginID, "status": string(info.Status)}
	if info.PID != 0 {
		payload["pid"] = info.PID
	}
	if !info.StartedAt.IsZero() {
		payload["started_at"] = info.StartedAt.Format(time.RFC3339Nano)
	}
	if info.LastError != "" {
		payload["last_error"] = info.LastError
	}
	if info.LogPath != "" {
		payload["log"] = info.LogPath
	}
	h.opts.Publisher.Publish(PluginExitedEvent, payload)
}

// Pod returns a loop-owned Pod for per-subscriber notification delivery.
func (h *Host) Pod(id string) (*Pod, bool) { p, ok := h.pods[id]; return p, ok }

// Removing reports whether a plugin.remove for id is in progress. Loop-only.
func (h *Host) Removing(id string) bool { _, ok := h.removing[id]; return ok }

// PodSlices adapts the Host's stored slice handles to the host's
// SliceHandles lookup seam: the Stem State port resolves a plugin's write
// capability by namespace here. The lookup runs off the loop on the Stem
// write path, so it reads the guarded map (handlesMu); a handle exists only
// while the Pod is registered (from RegisterSlicesMsg until the removal's teardown).
type PodSlices struct{ host *Host }

// NewPodSlices wraps h. The adapter exists because Host already has a
// Handle(tea.Msg) method and cannot also carry the lookup signature.
func NewPodSlices(h *Host) PodSlices { return PodSlices{host: h} }

// Handle returns the plugin's owner handle for its state namespace,
// <plugin_id>.state (state.md). Other namespaces are not registered by a
// Pod and resolve to false.
func (s PodSlices) Handle(pluginID, namespace string) (contracts.SliceHandle, bool) {
	if s.host == nil || namespace != pluginID+".state" {
		return nil, false
	}
	s.host.handlesMu.Lock()
	handle, ok := s.host.handles[pluginID]
	s.host.handlesMu.Unlock()
	return handle, ok
}

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
	h.cancelRemovals()
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
		if h.stopProcesses(processes, h.cancel) {
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
