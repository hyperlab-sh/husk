// Package state holds an instance's live, owner-registered JSON slice tree.
//
// Register, Get, Handle methods, Clear, OnFlush and Flush are loop-only: a
// single goroutine must serialize them. Snapshot is the only cross-goroutine
// read API. Each mutation publishes a new snapshot before returning. Published
// maps and byte slices are read-only and are never mutated by this package.
// Listeners run synchronously on the loop and must neither block nor mutate State.
// State does no I/O and imports no other Husk package.
package state

import (
	"encoding/json"
	"errors"
	"time"
)

// Scope determines which instances share a slice's persisted value.
type Scope string

const (
	// ScopeUser is shared by every instance of this user.
	ScopeUser Scope = "user"
	// ScopeInstance belongs to this instance, keyed by instance name.
	ScopeInstance Scope = "instance"
	// ScopeWorkspace belongs to the named instance's workspace snapshot.
	ScopeWorkspace Scope = "workspace"
)

// Policy tells Store how to retain persisted copies, never in-memory values.
type Policy string

const (
	// KeepLatest retains only the latest persisted value.
	KeepLatest Policy = "keep-latest"
	// KeepLastN retains the newest N persisted generations.
	KeepLastN Policy = "keep-last-n"
	// TTL expires persisted values after a duration since the last write.
	TTL Policy = "ttl"
	// Manual retains persisted values until explicitly deleted.
	Manual Policy = "manual"
)

// Retention is the persistence policy; it is zero for non-persistent slices.
type Retention struct {
	// Policy selects Store's retention behavior for persisted copies.
	Policy Policy
	// N is the generation count for KeepLastN only: 2..20.
	N int
	// TTL is the expiration duration for TTL only: 1m..8760h.
	TTL time.Duration
}

// Spec is a slice's immutable registration. Initial must be a JSON object;
// nil means {}. MaxBytes zero selects the class cap; a positive value lowers it.
type Spec struct {
	// Namespace is <owner>.<name>, with the final dot separating the name.
	Namespace string
	// Owner is husk for core modules, otherwise a plugin or workspace owner id.
	Owner string
	// Scope determines how Store shares or isolates persisted values.
	Scope Scope
	// Persistent asks Store to hydrate and persist the slice.
	Persistent bool
	// Retention is set if and only if Persistent is true.
	Retention Retention
	// MaxBytes is the normalized value cap; zero selects the class default.
	// A registration may lower, but never raise, the class default.
	MaxBytes int
	// Initial is a JSON object; nil means {}. State stores a normalized copy.
	Initial json.RawMessage
}

// Descriptor is a slice's metadata as readers see it.
type Descriptor struct {
	// Spec is the immutable, normalized registration metadata.
	Spec
	// Revision starts at 1 and increases per committed write; tombstones keep
	// re-registrations above their predecessor within this process.
	Revision uint64
	// Bytes is the encoded size of the normalized Value.
	Bytes int
	// Hydrated reports that registration started from a persisted value.
	Hydrated bool
}

// Entry is one slice in a snapshot. Value and Spec.Initial are read-only.
type Entry struct {
	// Descriptor is the slice metadata at this snapshot's revision.
	Descriptor
	// Value is a normalized JSON object, never mutated after publication.
	Value json.RawMessage
}

// Snapshot is a read-only full view, safe on any goroutine. Neither Slices nor
// its entries' byte slices may be modified. Retaining a snapshot is safe: State
// never mutates it, even after subsequent writes or unregistration.
type Snapshot struct {
	// Seq increments on every committed write or registration/unregistration.
	Seq uint64
	// Slices is the full read-only namespace-to-entry map at Seq.
	Slices map[string]Entry
}

// Change describes one slice's latest change in an Update call.
type Change struct {
	// Namespace is the changed slice's registered name.
	Namespace string
	// Owner is the capability owner of the changed slice.
	Owner string
	// Revision is the latest value revision in this Update call.
	Revision uint64
	// Removed reports that the slice was unregistered this tick.
	Removed bool
}

// Hydrator supplies persisted values inside Register. It must be a preloaded
// in-memory lookup: no blocking or I/O is allowed on the loop. It is called only
// for persistent slices, including user-scope slices in anonymous instances.
type Hydrator interface {
	// Hydrate returns the preloaded persisted value for spec, if present.
	Hydrate(spec Spec) (value json.RawMessage, ok bool)
}

// Listener receives a nonempty, namespace-ordered batch during Flush at the end
// of an Update call. It must not block, write to State, modify the arguments,
// or retain changes past the call. It may retain the read-only snapshot.
type Listener func(changes []Change, snap *Snapshot)

// Options supplies preloaded hydration and the distribution's workspace owner.
type Options struct {
	// Hydrator supplies preloaded saved values; nil disables hydration.
	// Anonymous instances still hydrate user scope when one is provided.
	Hydrator Hydrator
	// WorkspaceOwner is named by the distribution configuration; empty
	// permits only husk to register workspace scope.
	WorkspaceOwner string
}

var (
	// ErrDuplicate means the namespace is already registered.
	ErrDuplicate = errors.New("state: namespace already registered")
	// ErrInvalidSpec means a registration field is invalid.
	ErrInvalidSpec = errors.New("state: invalid slice spec")
	// ErrTooMany means the registered slice limit was reached.
	ErrTooMany = errors.New("state: slice limit reached")
	// ErrTooLarge means an encoded value exceeds the slice cap.
	ErrTooLarge = errors.New("state: value exceeds slice size cap")
	// ErrInvalidValue means a value or patch result is not a JSON object.
	ErrInvalidValue = errors.New("state: value is not a JSON object")
	// ErrConflict means a patch's expected revision is stale.
	ErrConflict = errors.New("state: revision mismatch")
	// ErrUnregistered means a handle's registration no longer exists.
	ErrUnregistered = errors.New("state: slice was unregistered")
)
