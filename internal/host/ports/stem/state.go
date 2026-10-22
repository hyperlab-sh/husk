package stem

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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

// StatePortOptions configures NewStatePort. State and Loop are required;
// Handles is required for plugin writes (its absence fails those calls with
// internal), Cleaner is optional.
type StatePortOptions struct {
	State   *state.State
	Loop    Loop
	Handles SliceHandles
	Cleaner SliceCleaner
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

	mu       sync.Mutex
	watchers map[*stateWatch]struct{}
}

// stateWatch is one registered event.watch: the notifier lives until ctx
// ends; filter nil means every slice.
type stateWatch struct {
	ctx    context.Context
	filter map[string]struct{}
	n      contracts.Notifier
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
		resolved, err := resolvePointer(value, path)
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
	res, err := w.snapshot(true, filter)
	if err != nil {
		return contracts.StateSnapshotResult{}, err
	}
	reg := &stateWatch{ctx: ctx, filter: filter, n: n}
	w.mu.Lock()
	w.watchers[reg] = struct{}{}
	w.mu.Unlock()
	context.AfterFunc(ctx, func() {
		w.mu.Lock()
		delete(w.watchers, reg)
		w.mu.Unlock()
	})
	return res, nil
}

// Clear implements contracts.StatePort.Clear: a user-only reset of one
// namespace to its initial value. Persisted copies are deleted through the
// Store seam first; the in-memory clear runs on the loop.
func (w *State) Clear(ctx context.Context, c contracts.Caller, namespace string) error {
	if !c.Kind.User() {
		return contracts.NewError(protocol.CodeDenied, "state.clear clears the user's own data; only a user may do it", nil)
	}
	if w.cleaner != nil {
		if err := w.cleaner.DeleteSlice(ctx, namespace); err != nil {
			return contracts.NewError(protocol.CodeInternal, "deleting persisted slice "+namespace+": "+err.Error(), nil)
		}
	}
	_, err := w.apply(ctx, namespace, func() (uint64, error) { return w.st.Clear(namespace) })
	if err != nil {
		return err
	}
	return nil
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

// apply posts f to the loop and waits for its outcome on ctx. If ctx ends
// first the reply is cancelled; the posted closure may still run later.
// A substrate error is mapped with the namespace's current revision and cap
// so conflict and size-cap replies carry their data.
func (w *State) apply(ctx context.Context, namespace string, f func() (uint64, error)) (uint64, error) {
	type outcome struct {
		rev uint64
		err error
	}
	done := make(chan outcome, 1)
	w.loop.Post(func() {
		rev, err := f()
		done <- outcome{rev: rev, err: err}
	})
	select {
	case o := <-done:
		if o.err != nil {
			return o.rev, w.mapWriteError(namespace, o.rev, o.err)
		}
		return o.rev, nil
	case <-ctx.Done():
		return 0, contracts.NewError(protocol.CodeCancelled, "request ended before the write was applied", nil)
	}
}

// mapWriteError maps a substrate write error to the wire codes state.md
// fixes: conflict carries the current revision, a size-cap breach carries
// the cap, invalid values and patches are invalid_params. Everything else
// stays a plain error and the handler adapter reports it as internal.
func (w *State) mapWriteError(namespace string, current uint64, err error) error {
	switch {
	case errors.Is(err, state.ErrConflict):
		return contracts.NewError(protocol.CodeConflict, "if_revision does not match the current revision", map[string]any{"revision": current})
	case errors.Is(err, state.ErrTooLarge):
		data := map[string]any{}
		if cap := w.capFor(namespace); cap > 0 {
			data["limit"] = cap
		}
		return contracts.NewError(protocol.CodeInvalidParams, "value exceeds the slice size cap", data)
	case errors.Is(err, state.ErrInvalidValue):
		return contracts.NewError(protocol.CodeInvalidParams, "value or patch result is not a JSON object", nil)
	case errors.Is(err, state.ErrUnregistered):
		return contracts.NewError(protocol.CodeNotFound, "slice "+namespace+" is not registered", nil)
	}
	var coded contracts.CodedError
	if errors.As(err, &coded) {
		return err
	}
	return err
}

// capFor reports the byte cap that applies to namespace: its declared cap,
// or its class default. Zero when the namespace is not registered.
func (w *State) capFor(namespace string) int {
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

// resolvePointer resolves an RFC 6901 pointer against a JSON value. A pointer
// that does not start with "/" is a syntax error; a token that does not exist
// does not resolve.
func resolvePointer(value json.RawMessage, pointer string) (json.RawMessage, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, errPointerSyntax
	}
	var doc any
	if err := json.Unmarshal(value, &doc); err != nil {
		return nil, contracts.NewError(protocol.CodeInternal, "slice value is not valid JSON: "+err.Error(), nil)
	}
	cur := doc
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, errNoResolve
			}
			cur = next
		case []any:
			if token == "" || token == "-" || (len(token) > 1 && token[0] == '0') {
				return nil, errNoResolve
			}
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, errNoResolve
			}
			cur = node[i]
		default:
			return nil, errNoResolve
		}
	}
	if cur == nil {
		return json.RawMessage("null"), nil
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return nil, contracts.NewError(protocol.CodeInternal, "encoding pointer result: "+err.Error(), nil)
	}
	return raw, nil
}

// Compile-time interface checks.
var (
	_ contracts.SessionPort = (*Session)(nil)
	_ contracts.StatePort   = (*State)(nil)
)
