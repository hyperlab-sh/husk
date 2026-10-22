package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
)

// State is one live slice tree. Construct it with New; do not copy it.
// Only Snapshot may be called outside the loop goroutine.
type State struct {
	opts       Options
	records    map[string]*record
	tombstones map[string]uint64
	pending    map[string]Change
	listeners  []Listener
	published  atomic.Pointer[Snapshot]
}

type record struct {
	entry Entry
}

// New creates an empty State. Persistent registrations consult opts.Hydrator
// synchronously; other scopes and persistence decisions remain Store's concern.
func New(opts Options) *State {
	s := &State{
		opts:       opts,
		records:    make(map[string]*record),
		tombstones: make(map[string]uint64),
		pending:    make(map[string]Change),
	}
	s.published.Store(&Snapshot{Slices: make(map[string]Entry)})
	return s
}

// Register validates and registers spec, returning the sole write capability.
// The initial or hydrated value is visible in Snapshot before this returns.
// Invalid hydration fails atomically with ErrInvalidValue or ErrTooLarge;
// typed Register additionally handles incompatible persisted owner types.
func (s *State) Register(spec Spec) (*Handle, error) {
	return s.register(spec, nil)
}

// acceptHydrated, used by typed Register, validates the owner's decoded type.
// A rejected hydration there falls back before publishing any registration.
func (s *State) register(spec Spec, acceptHydrated func(json.RawMessage) error) (*Handle, error) {
	if _, exists := s.records[spec.Namespace]; exists {
		return nil, fmt.Errorf("%w: %s", ErrDuplicate, spec.Namespace)
	}
	spec, err := validateSpec(spec, s.opts.WorkspaceOwner)
	if err != nil {
		return nil, err
	}
	if len(s.records) >= MaxSlices {
		return nil, ErrTooMany
	}
	value := spec.Initial
	hydrated := false
	var discarded error
	if spec.Persistent && s.opts.Hydrator != nil {
		// Do not give the Hydrator aliases into our immutable registration.
		lookup := spec
		lookup.Initial = copyJSON(spec.Initial)
		if loaded, ok := s.opts.Hydrator.Hydrate(lookup); ok {
			err := validateValue(loaded, spec.MaxBytes)
			if err == nil && acceptHydrated != nil {
				err = acceptHydrated(loaded)
			}
			if err != nil {
				if acceptHydrated == nil {
					return nil, fmt.Errorf("state: hydrate %s: %w", spec.Namespace, err)
				}
				discarded = fmt.Errorf("state: discarded persisted %s: %w", spec.Namespace, err)
			} else {
				value = loaded
				hydrated = true
			}
		}
	}
	r := &record{entry: Entry{
		Descriptor: Descriptor{
			Spec:     spec,
			Revision: s.tombstones[spec.Namespace] + 1,
			Bytes:    len(value),
			Hydrated: hydrated,
		},
		Value: copyJSON(value),
	}}
	s.records[spec.Namespace] = r
	s.publish(spec.Namespace, r)
	return &Handle{state: s, record: r}, discarded
}

// Get returns an independent copy of a slice's current entry. Loop only;
// elsewhere, read Snapshot().Slices instead.
func (s *State) Get(namespace string) (Entry, bool) {
	r, ok := s.records[namespace]
	if !ok {
		return Entry{}, false
	}
	entry := r.entry
	entry.Initial = copyJSON(entry.Initial)
	entry.Value = copyJSON(entry.Value)
	return entry, true
}

// Snapshot returns the latest read-only full view by atomic load. It is safe
// on any goroutine, never blocks, and includes every completed mutation.
func (s *State) Snapshot() *Snapshot {
	return s.published.Load()
}

// OnFlush registers a non-blocking loop listener. Nil listeners are ignored.
func (s *State) OnFlush(l Listener) {
	if l != nil {
		s.listeners = append(s.listeners, l)
	}
}

// Flush delivers each nonempty tick's namespace-ordered net changes once to
// each listener. The loop calls it at the end of every Update, before flushing
// the Event bus. Follow-up messages belong to separate ticks. Listeners may not
// mutate State or retain changes; the snapshot can be retained read-only.
func (s *State) Flush() {
	if len(s.pending) == 0 {
		return
	}
	changes := make([]Change, 0, len(s.pending))
	for _, namespace := range slices.Sorted(maps.Keys(s.pending)) {
		changes = append(changes, s.pending[namespace])
	}
	clear(s.pending)
	snap := s.Snapshot()
	for _, listener := range s.listeners {
		listener(changes, snap)
	}
}

// Clear resets a registered slice to its Initial value, with a revision bump
// and change even if the value is already Initial. This is the only write API
// without a Handle. Only the user-only state.clear Server handler may call it,
// on the loop, after Store has deleted persisted copies. Authorization and disk
// deletion belong to that handler, not to this in-memory capability package.
func (s *State) Clear(namespace string) (uint64, error) {
	r, ok := s.records[namespace]
	if !ok {
		return 0, ErrUnregistered
	}
	return s.commit(r, r.entry.Initial), nil
}

// Handle is an opaque capability for one registration, not a namespace lookup.
// Keeping an old handle across Unregister never authorizes a new registration.
// A zero Handle cannot write. Handle methods are loop-only.
type Handle struct {
	state  *State
	record *record
}

// Namespace returns this handle's namespace (also after unregistration), or
// the empty string for an uninitialized handle.
func (h *Handle) Namespace() string {
	if h == nil || h.record == nil {
		return ""
	}
	return h.record.entry.Namespace
}

func (h *Handle) registered() bool {
	return h != nil && h.state != nil && h.record != nil && h.state.records[h.Namespace()] == h.record
}

// Set atomically replaces the slice with a JSON object. Identical encoded bytes
// are a no-op. The input is copied; its caller may reuse it after Set returns.
func (h *Handle) Set(value json.RawMessage) (uint64, error) {
	if !h.registered() {
		return 0, ErrUnregistered
	}
	r := h.record
	if err := validateValue(value, r.entry.MaxBytes); err != nil {
		return r.entry.Revision, err
	}
	if bytes.Equal(value, r.entry.Value) {
		return r.entry.Revision, nil
	}
	return h.state.commit(r, value), nil
}

// Patch applies RFC 7396 JSON Merge Patch, comparing ifRevision first when it
// is nonzero. The fully merged object must fit the cap; failures change nothing.
func (h *Handle) Patch(patch json.RawMessage, ifRevision uint64) (uint64, error) {
	if !h.registered() {
		return 0, ErrUnregistered
	}
	if ifRevision != 0 && ifRevision != h.record.entry.Revision {
		return h.record.entry.Revision, ErrConflict
	}
	value, err := mergePatch(h.record.entry.Value, patch)
	if err != nil {
		return h.record.entry.Revision, fmt.Errorf("%w: patch: %v", ErrInvalidValue, err)
	}
	return h.Set(value)
}

// Unregister removes this handle's registration. Repeated calls and stale or
// zero handles are harmless. The last value revision is retained as a tombstone
// so re-registration starts above it. The removal has its own Snapshot sequence.
func (h *Handle) Unregister() {
	if !h.registered() {
		return
	}
	s, entry := h.state, h.record.entry
	delete(s.records, entry.Namespace)
	s.tombstones[entry.Namespace] = entry.Revision
	s.publish(entry.Namespace, nil)
	s.pending[entry.Namespace] = Change{
		Namespace: entry.Namespace, Owner: entry.Owner,
		Revision: entry.Revision, Removed: true,
	}
}

func (s *State) commit(r *record, value json.RawMessage) uint64 {
	r.entry.Value = copyJSON(value)
	r.entry.Revision++
	r.entry.Bytes = len(value)
	s.publish(r.entry.Namespace, r)
	return r.entry.Revision
}

func (s *State) publish(namespace string, r *record) {
	old := s.Snapshot()
	next := &Snapshot{Seq: old.Seq + 1, Slices: maps.Clone(old.Slices)}
	if r == nil {
		delete(next.Slices, namespace)
	} else {
		next.Slices[namespace] = r.entry
		s.pending[namespace] = Change{
			Namespace: namespace, Owner: r.entry.Owner, Revision: r.entry.Revision,
		}
	}
	s.published.Store(next)
}
