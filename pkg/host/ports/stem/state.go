package stem

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

// SliceHandles resolves the Pod-held write capability for one of a plugin's
// own slices. contracts.SliceHandle is satisfied by *state.Handle; the Pod
// owns the handle, so this wrapper never registers plugin slices itself.
type SliceHandles interface {
	Handle(pluginID, namespace string) (contracts.SliceHandle, bool)
}

// SliceCleaner deletes a slice's persisted copies for state.clear; Store
// implements it. Nil means no persisted slices to delete.
type SliceCleaner interface {
	DeleteSlice(ctx context.Context, namespace string) error
}

// BeforeStateClearHook is the id of the gate hook Clear fires before it
// deletes anything.
const BeforeStateClearHook = "husk.before-state-clear"

// HookFirer is how a host module fires a husk.* hook from inside its own
// operation; the composition root implements it over the runner. Fire is
// called off the loop (handler goroutines): it posts runner.Call onto the
// loop and waits for the outcome or ctx. It is never called on the loop,
// where the wait would deadlock.
//
// It returns the runner's refusal as a *hooks.HookError (denied, not_found,
// invalid_params, busy) when Call refused; a *hooks.HookError with code
// unavailable when the loop no longer runs; a *hooks.HookError with code
// cancelled wrapping ctx.Err() when ctx ends before the outcome; otherwise
// the Outcome and nil.
type HookFirer interface {
	Fire(ctx context.Context, hook string, data map[string]any) (hooks.Outcome, error)
}

// StatePortOptions configures NewStatePort. State and Loop are required;
// Handles is required for plugin writes (its absence fails those calls with
// internal), Cleaner, Hooks and Logf are optional. Without Hooks, Clear
// has no hooks wired and the gate passes; a nil Logf discards.
type StatePortOptions struct {
	State   *state.State
	Loop    Loop
	Handles SliceHandles
	Cleaner SliceCleaner
	Hooks   HookFirer
	Logf    func(level, format string, args ...any)
}

// State serves contracts.StatePort over the real substrate State: reads and
// the snapshot run on copy-on-write Snapshot values; plugin writes and user
// clears run on the loop; watch fan-out hangs off the single State flush
// listener installed by NewStatePort. It is safe for concurrent use — the
// Server calls handlers from reader goroutines while the loop flushes.
type State struct {
	st      *state.State
	loop    Loop
	handles SliceHandles
	cleaner SliceCleaner
	hooks   HookFirer
	logf    func(level, format string, args ...any)

	mu       sync.Mutex
	watchers map[*stateWatch]struct{}
}

// stateWatch is one registered state.watch: the notifier lives until ctx
// ends; filter nil means every slice. baseline is the State Seq the reply's
// baseline snapshot was taken at — flushes at or below it are already in
// the baseline, so fanOut skips them.
type stateWatch struct {
	ctx      context.Context
	filter   map[string]struct{}
	n        contracts.Notifier
	baseline uint64
}

// NewStatePort builds the State wrapper and installs the flush listener that
// fans state.changed notifications out to watchers. It panics on a nil State
// or Loop — those are composition bugs — and must be called before the
// Server starts serving, like every other listener registration.
func NewStatePort(opts StatePortOptions) *State {
	if opts.State == nil || opts.Loop == nil {
		panic("stem: NewStatePort requires State and Loop")
	}
	w := &State{
		st:       opts.State,
		loop:     opts.Loop,
		handles:  opts.Handles,
		cleaner:  opts.Cleaner,
		hooks:    opts.Hooks,
		logf:     opts.Logf,
		watchers: map[*stateWatch]struct{}{},
	}
	opts.State.OnFlush(w.fanOut)
	return w
}

// Get implements contracts.StatePort.Get: a loop-free read of one slice, or
// of the value a JSON Pointer resolves to inside it.
func (w *State) Get(_ context.Context, _ contracts.Caller, slice, path string) (contracts.StateReadResult, error) {
	entry, ok := w.st.Snapshot().Slices[slice]
	if !ok {
		return contracts.StateReadResult{}, contracts.NewError(protocol.CodeNotFound, "slice "+slice+" is not registered", nil)
	}
	value := entry.Value
	if path != "" {
		resolved, err := resolvePointer(entry, path)
		if err != nil {
			return contracts.StateReadResult{}, err
		}
		value = resolved
	}
	return contracts.StateReadResult{
		Slice:    slice,
		Owner:    entry.Owner,
		Revision: entry.Revision,
		Value:    value,
	}, nil
}

// Set implements contracts.StatePort.Set: a plugin write to one of its own
// slices, applied through the Pod-held handle on the loop.
func (w *State) Set(ctx context.Context, c contracts.Caller, slice string, value json.RawMessage) (uint64, error) {
	h, err := w.writeHandle(c, slice)
	if err != nil {
		return 0, err
	}
	rev, err := w.apply(ctx, slice, func() (uint64, error) { return h.Set(value) })
	if err != nil {
		return 0, err
	}
	return rev, nil
}

// Patch implements contracts.StatePort.Patch: a merge patch on the plugin's
// own slice, with an optional optimistic-concurrency revision.
func (w *State) Patch(ctx context.Context, c contracts.Caller, slice string, patch json.RawMessage, ifRevision uint64) (uint64, error) {
	h, err := w.writeHandle(c, slice)
	if err != nil {
		return 0, err
	}
	rev, err := w.apply(ctx, slice, func() (uint64, error) { return h.Patch(patch, ifRevision) })
	if err != nil {
		return 0, err
	}
	return rev, nil
}

// Snapshot implements contracts.StatePort.Snapshot: every registered slice,
// values included only on request.
func (w *State) Snapshot(_ context.Context, _ contracts.Caller, values bool) (contracts.StateSnapshotResult, error) {
	return w.snapshot(values, nil)
}

// Watch implements contracts.StatePort.Watch. The reply is the baseline
// snapshot with values, restricted to the watched slices; afterwards the
// flush listener pushes one state.changed notification per changed watched
// slice. Registration lives until ctx ends.
//
// The watcher registers before its baseline is taken, carrying the Seq the
// baseline will read, so no change can fall between the two: a flush at or
// below that Seq is already in the baseline (fanOut skips it), a later one
// is delivered.
func (w *State) Watch(ctx context.Context, _ contracts.Caller, slices []string, n contracts.Notifier) (contracts.StateSnapshotResult, error) {
	if n == nil {
		return contracts.StateSnapshotResult{}, contracts.NewError(protocol.CodeInternal, "state.watch needs a notifier", nil)
	}
	var filter map[string]struct{}
	if len(slices) > 0 {
		filter = make(map[string]struct{}, len(slices))
		for _, s := range slices {
			filter[s] = struct{}{}
		}
	}
	reg := &stateWatch{ctx: ctx, filter: filter, n: n, baseline: w.st.Snapshot().Seq}
	w.mu.Lock()
	w.watchers[reg] = struct{}{}
	w.mu.Unlock()
	unregister := func() {
		w.mu.Lock()
		delete(w.watchers, reg)
		w.mu.Unlock()
	}
	context.AfterFunc(ctx, unregister)
	res, err := w.snapshot(true, filter)
	if err != nil {
		unregister()
		return contracts.StateSnapshotResult{}, err
	}
	return res, nil
}

// Clear implements contracts.StatePort.Clear: a reset of one namespace to its
// initial value. CLI-only: clear is the user's own action, and a Stem caller
// over the port is not the user. Unless force is set, husk.before-state-clear
// gates the delete. An ended ctx before the Store delete stops the clear:
// shutdown answers unavailable, any other cause cancelled. Once deletion starts,
// it is not interrupted; after it succeeds the clear answers its real outcome,
// ignoring caller cancellation. The reset uses Loop.Do's claimed semantics:
// shutdown abandons a queued reset, but waits for a running reset. A skipped
// reset still succeeds because the persisted copies are gone and the live value
// dies with the host. A wedged loop holds the handler until shutdown, like other
// loop-bound ports.
func (w *State) Clear(ctx context.Context, c contracts.Caller, namespace string, force bool) error {
	if c.Kind != contracts.CallerCLI {
		return contracts.NewError(protocol.CodeDenied, "state.clear clears the user's own data; only the user's CLI may do it", nil)
	}
	// An unregistered namespace changes nothing (state.md): checked before
	// the hook and before any persisted copy is touched.
	if _, ok := w.st.Snapshot().Slices[namespace]; !ok {
		return errNotRegistered(namespace)
	}
	if !force {
		if err := w.beforeClear(ctx, namespace); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), contracts.ErrShutdown) {
			return coded(protocol.CodeUnavailable, "the host is shutting down")
		}
		return coded(protocol.CodeCancelled, "request ended before deletion; nothing was cleared")
	}
	// The hook wait or a plugin removal may have unregistered the slice since
	// the first check; its persisted copies are kept (state.md).
	if _, ok := w.st.Snapshot().Slices[namespace]; !ok {
		return errNotRegistered(namespace)
	}
	if w.cleaner != nil {
		// Persisted copies go first, mirroring store.md's documented clear
		// sequence — Store.ClearSlice deletes the shared user and instance
		// copies, then the handler resets State — so both orders agree. A
		// failed delete aborts with State untouched and the retry is
		// idempotent.
		if err := w.cleaner.DeleteSlice(context.WithoutCancel(ctx), namespace); err != nil {
			return contracts.NewError(protocol.CodeInternal, "deleting persisted slice "+namespace+": "+err.Error(), nil)
		}
	}
	return w.applyClear(ctx, namespace)
}

// applyClear completes an already-deleted clear, abandoning a queued reset only
// on shutdown. Loop.Do waits for a claimed closure even if its context ends.
func (w *State) applyClear(ctx context.Context, namespace string) error {
	applyCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(context.Cause(ctx), contracts.ErrShutdown) {
			cancel()
		}
	})
	defer stop()
	limit := w.limitFor(namespace)
	var rev uint64
	var writeErr error
	if err := w.loop.Do(applyCtx, func() {
		rev, writeErr = w.st.Clear(namespace)
	}); err != nil {
		w.log("warn", "state.clear: %s: in-memory reset skipped because the host is closing; persisted copies are deleted", namespace)
		return nil
	}
	if writeErr != nil {
		return w.mapWriteError(namespace, rev, limit, writeErr)
	}
	return nil
}

// beforeClear fires husk.before-state-clear for a registered slice and maps
// what Fire returns to nil (proceed) or the error Clear answers with. Clear has
// already refused an unregistered slice, so a missing entry here only means
// it was unregistered since; nothing is asked. The hook data carries the slice
// name and owner, never the value.
func (w *State) beforeClear(ctx context.Context, namespace string) error {
	if w.hooks == nil {
		return nil
	}
	entry, ok := w.st.Snapshot().Slices[namespace]
	if !ok {
		return nil
	}
	out, err := w.hooks.Fire(ctx, BeforeStateClearHook, map[string]any{"slice": namespace, "owner": entry.Owner})
	if err != nil {
		var he *hooks.HookError
		if !errors.As(err, &he) {
			w.log("error", "state.clear: firing %s failed: %v", BeforeStateClearHook, err)
			return coded(protocol.CodeInternal, "firing "+BeforeStateClearHook+" failed")
		}
		switch he.Code {
		case protocol.CodeBusy:
			w.log("warn", "state.clear: %s skipped, clearing %s anyway: %s", BeforeStateClearHook, namespace, he.Message)
			return nil
		case protocol.CodeUnavailable:
			return coded(protocol.CodeUnavailable, "the host is shutting down")
		case protocol.CodeCancelled:
			if errors.Is(context.Cause(ctx), contracts.ErrShutdown) {
				return coded(protocol.CodeUnavailable, "the host is shutting down")
			}
			return coded(protocol.CodeCancelled, "request ended before the hook answered; nothing was cleared")
		default:
			w.log("error", "state.clear: BUG: %s refused with %s: %s", BeforeStateClearHook, he.Code, he.Message)
			return coded(protocol.CodeInternal, "firing "+BeforeStateClearHook+" failed")
		}
	}
	for _, r := range out.Results {
		if r.Err != nil && r.Err.Code == protocol.CodeCancelled && r.Err.Data["reason"] == "shutdown" {
			return coded(protocol.CodeUnavailable, "the host is shutting down")
		}
	}
	for _, r := range out.Results {
		if r.Err != nil {
			w.log("info", "state.clear: %s: subscriber %s counted as allow: %s", BeforeStateClearHook, r.Plugin, r.Err.Error())
		}
	}
	if !out.Allowed {
		vetoes := make([]map[string]any, len(out.Vetoes))
		for i, v := range out.Vetoes {
			vetoes[i] = map[string]any{"plugin_id": v.Plugin, "reason": v.Reason}
		}
		return contracts.NewError(protocol.CodeDenied, "clearing "+namespace+" was vetoed",
			map[string]any{"reason": "vetoed", "vetoes": vetoes})
	}
	return nil
}

// log reports through the injected logger; nil discards.
func (w *State) log(level, format string, args ...any) {
	if w.logf != nil {
		w.logf(level, format, args...)
	}
}

// writeHandle authorizes a plugin write and resolves the Pod-held handle.
// Only a plugin may write, and only to its own <plugin>.state or
// <plugin>.workspace namespace (state.md); the CLI and Stem act for the user
// and never write.
func (w *State) writeHandle(c contracts.Caller, namespace string) (contracts.SliceHandle, error) {
	if c.Kind != contracts.CallerPlugin {
		return nil, contracts.NewError(protocol.CodeDenied, "state writes come from a plugin's own slice", nil)
	}
	if namespace != c.PluginID+".state" && namespace != c.PluginID+".workspace" {
		return nil, contracts.NewError(protocol.CodeDenied, namespace+" is not the caller's own slice", nil)
	}
	if w.handles == nil {
		return nil, contracts.NewError(protocol.CodeInternal, "no slice handle source is wired", nil)
	}
	h, ok := w.handles.Handle(c.PluginID, namespace)
	if !ok {
		return nil, contracts.NewError(protocol.CodeInternal, "no write handle for "+namespace+"; the caller's Pod owns it", nil)
	}
	return h, nil
}

// apply posts a Set or Patch to the loop and waits for its outcome on ctx. A Post that
// reports the loop no longer running answers unavailable at once; if ctx
// ends first the reply is cancelled, and the posted closure may still run
// later. limit is the size cap captured before the write was posted — the
// cap the substrate checked the value against — so the reply carries it
// even if the slice is unregistered while the write is in flight. A
// substrate error is mapped with the namespace's current revision and that
// cap so conflict and size-cap replies carry their data.
func (w *State) apply(ctx context.Context, namespace string, f func() (uint64, error)) (uint64, error) {
	limit := w.limitFor(namespace)
	type outcome struct {
		rev uint64
		err error
	}
	done := make(chan outcome, 1)
	if !w.loop.Post(func() {
		rev, err := f()
		done <- outcome{rev: rev, err: err}
	}) {
		return 0, contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil)
	}
	o, ok := await(ctx, done)
	if !ok {
		if errors.Is(context.Cause(ctx), contracts.ErrShutdown) {
			return 0, ctxError(ctx)
		}
		return 0, contracts.NewError(protocol.CodeCancelled, "request ended before the write was applied", nil)
	}
	if o.err != nil {
		return o.rev, w.mapWriteError(namespace, o.rev, limit, o.err)
	}
	return o.rev, nil
}

// mapWriteError maps a substrate write error to the wire codes state.md
// fixes: conflict carries the current revision, a size-cap breach carries
// the cap the write was checked against (captured before it was posted, so
// it survives a mid-write unregistration), invalid values and patches are
// invalid_params. Everything else stays a plain error and the handler
// adapter reports it as internal.
func (w *State) mapWriteError(namespace string, current uint64, limit int, err error) error {
	switch {
	case errors.Is(err, state.ErrConflict):
		return contracts.NewError(protocol.CodeConflict, "if_revision does not match the current revision", map[string]any{"revision": current})
	case errors.Is(err, state.ErrTooLarge):
		data := map[string]any{}
		if limit > 0 {
			data["limit"] = limit
		}
		return contracts.NewError(protocol.CodeInvalidParams, "value exceeds the slice size cap", data)
	case errors.Is(err, state.ErrInvalidValue):
		return contracts.NewError(protocol.CodeInvalidParams, "value or patch result is not a JSON object", nil)
	case errors.Is(err, state.ErrUnregistered):
		return contracts.NewError(protocol.CodeNotFound, "slice "+namespace+" is not registered", nil)
	}
	return err
}

// limitFor reports the byte cap that applies to namespace: its declared
// cap, or its class default. Zero when the namespace is not registered.
func (w *State) limitFor(namespace string) int {
	entry, ok := w.st.Snapshot().Slices[namespace]
	if !ok {
		return 0
	}
	if entry.Spec.MaxBytes > 0 {
		return entry.Spec.MaxBytes
	}
	switch {
	case entry.Owner == state.OwnerHusk:
		return state.CoreMaxBytes
	case entry.Spec.Scope == state.ScopeWorkspace:
		return state.WorkspaceMaxBytes
	}
	return state.PluginMaxBytes
}

// snapshot builds the wire snapshot, restricted to filter when non-nil,
// with values on request. A values snapshot that cannot fit one protocol
// line is refused up front rather than dropped by the writer.
func (w *State) snapshot(values bool, filter map[string]struct{}) (contracts.StateSnapshotResult, error) {
	snap := w.st.Snapshot()
	names := make([]string, 0, len(snap.Slices))
	for ns := range snap.Slices {
		if filter != nil {
			if _, ok := filter[ns]; !ok {
				continue
			}
		}
		names = append(names, ns)
	}
	sort.Strings(names)
	out := contracts.StateSnapshotResult{
		Seq:    snap.Seq,
		Slices: make([]contracts.StateSlice, 0, len(names)),
	}
	for _, ns := range names {
		e := snap.Slices[ns]
		s := contracts.StateSlice{
			Slice:      ns,
			Owner:      e.Owner,
			Scope:      string(e.Spec.Scope),
			Persistent: e.Persistent,
			Revision:   e.Revision,
			Bytes:      e.Bytes,
			Hydrated:   e.Hydrated,
		}
		if e.Persistent {
			s.Retention = &contracts.StateRetention{
				Policy: string(e.Retention.Policy),
				N:      e.Retention.N,
			}
		}
		if values {
			s.Value = e.Value
		}
		out.Slices = append(out.Slices, s)
	}
	if values {
		if raw, err := json.Marshal(out); err == nil && len(raw) > protocol.MaxLineBytes {
			return contracts.StateSnapshotResult{}, contracts.NewError(protocol.CodeInvalidParams,
				"snapshot with values exceeds one protocol line", map[string]any{"limit": protocol.MaxLineBytes})
		}
	}
	return out, nil
}

// fanOut is the single State flush listener: one state.changed notification
// per changed slice to each live watcher subscribed to that slice. It never
// blocks and never returns errors to State: the notifier goes to the
// connection's bounded queue, and a closed or full connection reports itself
// through its own diagnostics.
func (w *State) fanOut(changes []state.Change, snap *state.Snapshot) {
	w.mu.Lock()
	live := make([]*stateWatch, 0, len(w.watchers))
	for reg := range w.watchers {
		if reg.ctx.Err() != nil {
			delete(w.watchers, reg)
			continue
		}
		if snap.Seq <= reg.baseline {
			// The flush raced the watcher's baseline: its changes are
			// already in the baseline snapshot the reply carries.
			continue
		}
		live = append(live, reg)
	}
	w.mu.Unlock()
	for _, ch := range changes {
		var value json.RawMessage
		if !ch.Removed {
			if e, ok := snap.Slices[ch.Namespace]; ok {
				value = e.Value
			}
		}
		raw, err := json.Marshal(changedParams{
			Slice:    ch.Namespace,
			Owner:    ch.Owner,
			Revision: ch.Revision,
			Value:    value,
			Removed:  ch.Removed,
		})
		if err != nil {
			continue
		}
		for _, reg := range live {
			if reg.filter != nil {
				if _, ok := reg.filter[ch.Namespace]; !ok {
					continue
				}
			}
			_ = reg.n.Notify(protocol.Message{Method: protocol.MethodStateChanged, Params: raw})
		}
	}
}

// changedParams is the state.changed notification payload: the slice, its
// owner, the new revision, the new value, and removed for unregistrations.
type changedParams struct {
	Slice    string          `json:"slice"`
	Owner    string          `json:"owner"`
	Revision uint64          `json:"revision"`
	Value    json.RawMessage `json:"value,omitempty"`
	Removed  bool            `json:"removed,omitempty"`
}

var (
	errPointerSyntax = contracts.NewError(protocol.CodeInvalidParams, "path must be empty or an RFC 6901 pointer starting with /", nil)
	errNoResolve     = contracts.NewError(protocol.CodeNotFound, "path does not resolve in this slice", nil)
)

func errNotRegistered(namespace string) error {
	return contracts.NewError(protocol.CodeNotFound, "slice "+namespace+" is not registered", nil)
}

// resolvePointer resolves an RFC 6901 pointer against a JSON value. A pointer
// that does not start with "/" is a syntax error; a token that does not exist
// does not resolve.
func resolvePointer(entry state.Entry, pointer string) (json.RawMessage, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, errPointerSyntax
	}
	raw, ok := entry.At(pointer)
	if !ok {
		return nil, errNoResolve
	}
	return raw, nil
}

// Compile-time interface checks.
var (
	_ contracts.SessionPort = (*Session)(nil)
	_ contracts.StatePort   = (*State)(nil)
)
